// Package eventhub broadcasts server events to WebSocket clients and
// in-process subscribers.
//
// Design notes (durability):
//   - BroadcastEvent fans out directly to per-client buffered channels under a
//     read lock — there is no intermediate broadcast channel that can silently
//     drop events when full.
//   - Every client gets a large send buffer. A client that cannot keep up is
//     disconnected (and logged) so its browser reconnects and re-syncs via
//     HTTP, instead of silently reading a stale stream.
//   - The hub emits an application-level "hub_heartbeat" event every
//     HeartbeatPeriod. Browsers cannot observe WebSocket protocol pings from
//     JavaScript, so this is the only way the frontend can detect a half-open
//     (silently dead) connection and force a reconnect.
//   - In-process subscribers are invoked outside the hub lock and with panic
//     recovery, so a slow or faulty subscriber can never stall broadcasting
//     or deadlock the hub (e.g. by calling Subscribe/BroadcastEvent itself).
package eventhub

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10 // 54 s — must be less than pongWait

	// HeartbeatPeriod is how often the hub broadcasts an application-level
	// heartbeat event. The frontend watchdog assumes the connection is dead
	// if it sees no message for a few multiples of this period.
	HeartbeatPeriod = 25 * time.Second

	// sendBufferSize is the per-client send queue. Sized for heavy run_log
	// bursts; a client that falls this far behind is disconnected so it can
	// reconnect and re-sync from the database via HTTP.
	sendBufferSize = 1024
)

// Subscriber is an in-process event handler (for testing and internal use).
type Subscriber func(eventType string, data interface{})

type Hub struct {
	mu          sync.RWMutex
	clients     map[*Client]struct{}
	subscribers map[string]Subscriber

	// companyRecipients resolves a company to the users allowed to receive
	// its events — the owning team's members (installed from main.go; nil in
	// local/legacy mode).
	companyRecipients func(companyID int32) ([]int32, bool)

	done      chan struct{}
	closeOnce sync.Once
}

// Client is one WebSocket connection managed by the hub.
type Client struct {
	hub    *Hub
	conn   *websocket.Conn
	send   chan []byte
	userID int32 // authenticated user this connection belongs to

	closed    chan struct{}
	closeOnce sync.Once
}

func NewHub() *Hub {
	return &Hub{
		clients:     make(map[*Client]struct{}),
		subscribers: make(map[string]Subscriber),
		done:        make(chan struct{}),
	}
}

// Subscribe registers an in-process callback that is invoked for every event.
// Use unique id values; re-registering the same id replaces the prior callback.
func (h *Hub) Subscribe(id string, fn Subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subscribers[id] = fn
}

// Unsubscribe removes a previously registered in-process callback.
func (h *Hub) Unsubscribe(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subscribers, id)
}

// Run emits periodic application-level heartbeats until Close is called.
// Broadcasting does not depend on Run — BroadcastEvent fans out directly —
// but without Run browsers cannot detect half-open connections.
func (h *Hub) Run() {
	ticker := time.NewTicker(HeartbeatPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			data, _ := json.Marshal(map[string]interface{}{
				"type":    "hub_heartbeat",
				"payload": map[string]interface{}{"ts": time.Now().Unix()},
			})
			h.broadcastRaw(data)
		case <-h.done:
			return
		}
	}
}

// Close stops the heartbeat loop and disconnects all clients.
func (h *Hub) Close() {
	h.closeOnce.Do(func() { close(h.done) })
	h.mu.RLock()
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()
	for _, c := range clients {
		c.close()
	}
}

// ClientCount returns the number of currently connected WebSocket clients.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Serve registers conn with the hub and starts its read/write pumps in
// background goroutines. It returns immediately. The hub owns conn from this
// point and closes it when the client disconnects or falls behind.
func (h *Hub) Serve(conn *websocket.Conn, userID int32) *Client {
	c := &Client{
		hub:    h,
		conn:   conn,
		send:   make(chan []byte, sendBufferSize),
		userID: userID,
		closed: make(chan struct{}),
	}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	go c.writePump()
	go c.readPump()
	return c
}

// SetCompanyRecipientsResolver installs the company → member-users lookup
// used by BroadcastEventForCompany. Installed once from main.go before the
// server starts accepting connections.
func (h *Hub) SetCompanyRecipientsResolver(fn func(companyID int32) ([]int32, bool)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.companyRecipients = fn
}

// BroadcastEventForCompany delivers the event to in-process subscribers
// (server-internal — they always see everything) and to the WebSocket clients
// of the owning team's members only. With no resolver installed (local/legacy
// mode) it behaves like BroadcastEvent; with a resolver installed but an
// unknown company it fails closed — no WS client receives the event.
func (h *Hub) BroadcastEventForCompany(companyID int32, eventType string, payload interface{}) {
	h.mu.RLock()
	resolve := h.companyRecipients
	h.mu.RUnlock()

	if resolve == nil {
		h.BroadcastEvent(eventType, payload)
		return
	}
	recipients, ok := resolve(companyID)
	if !ok {
		recipients = nil // matches no client
	}
	h.notifySubscribers(eventType, payload)
	if data, err := marshalEvent(eventType, payload); err == nil {
		h.broadcastRawToUsers(recipients, data)
	}
}

// ScopedBroadcaster is a Hub pre-bound to one company — it satisfies the
// `BroadcastEvent(string, interface{})` interface the proxy logger and LLM
// gateway expect, while delivering only to the company owner's clients.
type ScopedBroadcaster struct {
	hub       *Hub
	companyID int32
}

func (h *Hub) ForCompany(companyID int32) *ScopedBroadcaster {
	return &ScopedBroadcaster{hub: h, companyID: companyID}
}

func (b *ScopedBroadcaster) BroadcastEvent(eventType string, payload interface{}) {
	b.hub.BroadcastEventForCompany(b.companyID, eventType, payload)
}

// BroadcastEvent delivers the event to all in-process subscribers and ALL
// connected WebSocket clients regardless of user — reserved for genuinely
// global events (hub heartbeats); tenant data must go through
// BroadcastEventForCompany. It never blocks on a slow client: a client whose
// send queue is full is disconnected so it can reconnect and re-sync.
func (h *Hub) BroadcastEvent(eventType string, payload interface{}) {
	h.notifySubscribers(eventType, payload)
	data, err := marshalEvent(eventType, payload)
	if err != nil {
		log.Printf("eventhub: error marshaling %q event: %v", eventType, err)
		return
	}
	h.broadcastRaw(data)
}

// notifySubscribers invokes in-process subscribers outside the hub lock — a
// subscriber may be slow, panic, or call back into the hub.
func (h *Hub) notifySubscribers(eventType string, payload interface{}) {
	h.mu.RLock()
	subs := make([]Subscriber, 0, len(h.subscribers))
	for _, fn := range h.subscribers {
		subs = append(subs, fn)
	}
	h.mu.RUnlock()
	for _, fn := range subs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("eventhub: subscriber panic on %q: %v", eventType, r)
				}
			}()
			fn(eventType, payload)
		}()
	}
}

func marshalEvent(eventType string, payload interface{}) ([]byte, error) {
	return json.Marshal(map[string]interface{}{
		"type":    eventType,
		"payload": payload,
	})
}

func (h *Hub) broadcastRaw(data []byte) {
	h.sendAll(data)
}

// broadcastRawToUsers sends to the clients of the given users only.
func (h *Hub) broadcastRawToUsers(userIDs []int32, data []byte) {
	if len(userIDs) == 0 {
		return
	}
	allowed := make(map[int32]struct{}, len(userIDs))
	for _, id := range userIDs {
		allowed[id] = struct{}{}
	}
	h.mu.RLock()
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		if _, ok := allowed[c.userID]; ok {
			clients = append(clients, c)
		}
	}
	h.mu.RUnlock()
	h.deliver(clients, data)
}

// sendAll delivers to every connected client (heartbeats, legacy mode).
func (h *Hub) sendAll(data []byte) {
	h.mu.RLock()
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()
	h.deliver(clients, data)
}

func (h *Hub) deliver(clients []*Client, data []byte) {

	for _, c := range clients {
		select {
		case c.send <- data:
		default:
			// The client is too far behind to catch up. Disconnect it so the
			// browser reconnects and re-syncs full state over HTTP, rather
			// than letting it silently read an incomplete stream.
			log.Printf("eventhub: client send queue full (%d messages behind); disconnecting slow client", sendBufferSize)
			c.close()
		}
	}
}

// close is idempotent and safe to call from any goroutine.
func (c *Client) close() {
	c.closeOnce.Do(func() {
		c.hub.mu.Lock()
		delete(c.hub.clients, c)
		c.hub.mu.Unlock()
		close(c.closed) // signals writePump to send a Close frame and exit
	})
}

func (c *Client) readPump() {
	defer func() {
		c.close()
		c.conn.Close()
	}()
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure, websocket.CloseNormalClosure) {
				log.Printf("eventhub: read error: %v", err)
			}
			return
		}
	}
}

// writePump pumps messages from the hub to the WebSocket connection.
// Each application message is sent as its own WebSocket frame so that the
// client-side JSON.parse() always receives exactly one JSON object per event.
// A protocol-level ping ticker keeps the connection alive through proxies and
// lets the server side detect dead peers via the pong read deadline.
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.close()
		c.conn.Close()
	}()
	for {
		select {
		case message := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.closed:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			c.conn.WriteMessage(websocket.CloseMessage, []byte{})
			return
		}
	}
}
