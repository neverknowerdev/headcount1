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
)

// Subscriber is an in-process event handler (for testing and internal use).
type Subscriber func(eventType string, data interface{})

type Hub struct {
	clients     map[*Client]bool
	broadcast   chan []byte
	Register    chan *Client
	unregister  chan *Client
	mu          sync.Mutex
	subscribers map[string]Subscriber
}

type Client struct {
	Hub  *Hub
	Conn *websocket.Conn
	Send chan []byte
}

func NewHub() *Hub {
	return &Hub{
		broadcast:   make(chan []byte, 256),
		Register:    make(chan *Client),
		unregister:  make(chan *Client),
		clients:     make(map[*Client]bool),
		subscribers: make(map[string]Subscriber),
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

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.Register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.Send)
			}
			h.mu.Unlock()
		case message := <-h.broadcast:
			h.mu.Lock()
			for client := range h.clients {
				select {
				case client.Send <- message:
				default:
					close(client.Send)
					delete(h.clients, client)
				}
			}
			h.mu.Unlock()
		}
	}
}

func (h *Hub) BroadcastEvent(eventType string, payload interface{}) {
	// Invoke in-process subscribers first (used by tests and internal listeners).
	h.mu.Lock()
	for _, fn := range h.subscribers {
		fn(eventType, payload)
	}
	h.mu.Unlock()

	data, err := json.Marshal(map[string]interface{}{
		"type":    eventType,
		"payload": payload,
	})
	if err != nil {
		log.Printf("error marshaling event: %v", err)
		return
	}
	// Non-blocking send: drop the WebSocket message if the buffer is full or
	// hub.Run() is not active (e.g., in unit tests).
	select {
	case h.broadcast <- data:
	default:
	}
}

func (c *Client) ReadPump() {
	defer func() {
		c.Hub.unregister <- c
		c.Conn.Close()
	}()
	c.Conn.SetReadDeadline(time.Now().Add(pongWait))
	c.Conn.SetPongHandler(func(string) error {
		c.Conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		_, _, err := c.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			break
		}
	}
}

// WritePump pumps messages from the hub to the WebSocket connection.
// Each application message is sent as its own WebSocket frame so that the
// client-side JSON.parse() always receives exactly one JSON object per event.
// A ping ticker keeps the connection alive through proxies and load balancers.
func (c *Client) WritePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.Conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.Send:
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
