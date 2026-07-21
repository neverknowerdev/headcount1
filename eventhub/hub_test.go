package eventhub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

var testUpgrader = websocket.Upgrader{
	CheckOrigin: func(_ *http.Request) bool { return true },
}

// newTestServer starts an httptest server that upgrades connections and
// registers them with the provided hub.
func newTestServer(t *testing.T, hub *Hub) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("upgrade error: %v", err)
			return
		}
		hub.Serve(conn, 1)
	}))
}

// dialTestServer connects a WebSocket client to the test server and waits
// for the hub to register the client.
func dialTestServer(t *testing.T, srv *httptest.Server, hub *Hub) *websocket.Conn {
	t.Helper()
	before := hub.ClientCount()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for hub.ClientCount() <= before {
		if time.Now().After(deadline) {
			t.Fatal("hub never registered the client")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return conn
}

// TestHubDeliversEachMessageAsSeparateFrame verifies that when multiple events
// are broadcast in rapid succession each one arrives as a distinct WebSocket
// frame containing exactly one valid JSON object.
func TestHubDeliversEachMessageAsSeparateFrame(t *testing.T) {
	hub := NewHub()
	defer hub.Close()

	srv := newTestServer(t, hub)
	defer srv.Close()

	conn := dialTestServer(t, srv, hub)
	defer conn.Close()

	const n = 10
	for i := 0; i < n; i++ {
		hub.BroadcastEvent("test_event", map[string]interface{}{"seq": i})
	}

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))

	received := 0
	for received < n {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("ReadMessage after %d/%d messages: %v", received, n, err)
		}
		var msg map[string]interface{}
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("frame %d is not valid JSON: %v\nraw: %s", received, err, data)
		}
		if msg["type"] != "test_event" {
			t.Fatalf("frame %d: unexpected type %v", received, msg["type"])
		}
		received++
	}
}

// TestHubBroadcastReachesAllClients verifies that a single broadcast event
// is delivered to every connected client.
func TestHubBroadcastReachesAllClients(t *testing.T) {
	hub := NewHub()
	defer hub.Close()

	srv := newTestServer(t, hub)
	defer srv.Close()

	const clients = 3
	conns := make([]*websocket.Conn, clients)
	for i := range conns {
		conns[i] = dialTestServer(t, srv, hub)
		defer conns[i].Close()
	}

	hub.BroadcastEvent("ping", map[string]interface{}{"ok": true})

	for i, conn := range conns {
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("client %d ReadMessage: %v", i, err)
		}
		var msg map[string]interface{}
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("client %d invalid JSON: %v", i, err)
		}
		if msg["type"] != "ping" {
			t.Fatalf("client %d got type %v, want ping", i, msg["type"])
		}
	}
}

// TestHubInProcessSubscriber verifies that Subscribe/Unsubscribe work for
// in-process event listeners (used by integration tests and internal code).
func TestHubInProcessSubscriber(t *testing.T) {
	hub := NewHub()
	defer hub.Close()

	received := make(chan map[string]interface{}, 4)
	hub.Subscribe("test-sub", func(eventType string, data interface{}) {
		if m, ok := data.(map[string]interface{}); ok {
			received <- m
		}
	})

	hub.BroadcastEvent("task_done", map[string]interface{}{"id": 42})

	select {
	case msg := <-received:
		if msg["id"] != 42 {
			t.Errorf("got id=%v, want 42", msg["id"])
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscriber")
	}

	hub.Unsubscribe("test-sub")
	hub.BroadcastEvent("task_done", map[string]interface{}{"id": 99})

	select {
	case msg := <-received:
		t.Errorf("received event after unsubscribe: %v", msg)
	case <-time.After(100 * time.Millisecond):
		// expected: no message
	}
}

// TestHubBurstNoDrops verifies that a large burst of events (well within the
// per-client send buffer) is delivered completely and in order — the old hub
// dropped events silently when its shared broadcast channel filled up.
func TestHubBurstNoDrops(t *testing.T) {
	hub := NewHub()
	defer hub.Close()

	srv := newTestServer(t, hub)
	defer srv.Close()

	conn := dialTestServer(t, srv, hub)
	defer conn.Close()

	const n = 1000
	for i := 0; i < n; i++ {
		hub.BroadcastEvent("run_log", map[string]interface{}{"seq": i})
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for i := 0; i < n; i++ {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("ReadMessage after %d/%d messages: %v", i, n, err)
		}
		var msg struct {
			Payload struct {
				Seq int `json:"seq"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("frame %d invalid JSON: %v", i, err)
		}
		if msg.Payload.Seq != i {
			t.Fatalf("out-of-order or dropped event: got seq %d, want %d", msg.Payload.Seq, i)
		}
	}
}

// TestHubSlowClientDisconnectedNotBlocking verifies that a client that stops
// reading is eventually kicked (rather than blocking broadcasts) and that a
// healthy client keeps receiving events throughout.
func TestHubSlowClientDisconnectedNotBlocking(t *testing.T) {
	hub := NewHub()
	defer hub.Close()

	srv := newTestServer(t, hub)
	defer srv.Close()

	slow := dialTestServer(t, srv, hub) // never reads
	defer slow.Close()
	healthy := dialTestServer(t, srv, hub)
	defer healthy.Close()

	// Overflow the slow client's send buffer. Payloads must be large enough
	// that kernel TCP buffers fill and backpressure reaches the send channel.
	const msgCount = sendBufferSize + 200
	bigPayload := strings.Repeat("x", 16*1024)

	done := make(chan error, 1)
	go func() {
		healthy.SetReadDeadline(time.Now().Add(30 * time.Second))
		for i := 0; i < msgCount; i++ {
			if _, _, err := healthy.ReadMessage(); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()

	start := time.Now()
	for i := 0; i < msgCount; i++ {
		hub.BroadcastEvent("flood", map[string]interface{}{"seq": i, "data": bigPayload})
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("broadcasting blocked on slow client: took %v", elapsed)
	}

	if err := <-done; err != nil {
		t.Fatalf("healthy client stopped receiving: %v", err)
	}

	// The slow client is disconnected either by send-queue overflow (fast) or
	// by the write deadline once TCP backpressure blocks its writePump (10s).
	deadline := time.Now().Add(writeWait + 5*time.Second)
	for hub.ClientCount() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("slow client was not disconnected; %d clients still registered", hub.ClientCount())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestHubSubscriberCannotDeadlockHub verifies that a subscriber that calls
// back into the hub (Subscribe / BroadcastEvent) does not deadlock — the old
// implementation invoked subscribers while holding the hub mutex.
func TestHubSubscriberCannotDeadlockHub(t *testing.T) {
	hub := NewHub()
	defer hub.Close()

	got := make(chan string, 4)
	hub.Subscribe("reentrant", func(eventType string, data interface{}) {
		if eventType == "outer" {
			hub.Subscribe("added-from-callback", func(string, interface{}) {})
			hub.BroadcastEvent("inner", nil)
		}
		got <- eventType
	})

	finished := make(chan struct{})
	go func() {
		hub.BroadcastEvent("outer", nil)
		close(finished)
	}()

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("BroadcastEvent deadlocked with a reentrant subscriber")
	}
}

// TestHubSubscriberPanicIsRecovered verifies that a panicking subscriber does
// not crash the broadcaster and other subscribers still run.
func TestHubSubscriberPanicIsRecovered(t *testing.T) {
	hub := NewHub()
	defer hub.Close()

	hub.Subscribe("a-panics", func(string, interface{}) { panic("boom") })
	okCh := make(chan struct{}, 1)
	hub.Subscribe("b-ok", func(string, interface{}) { okCh <- struct{}{} })

	hub.BroadcastEvent("evt", nil)

	select {
	case <-okCh:
	case <-time.After(time.Second):
		t.Fatal("second subscriber did not run after first panicked")
	}
}

// TestHubHeartbeat verifies that Run() emits application-level heartbeat
// events that a browser client can observe to detect half-open connections.
func TestHubHeartbeat(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	go hub.Run()

	srv := newTestServer(t, hub)
	defer srv.Close()

	conn := dialTestServer(t, srv, hub)
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(HeartbeatPeriod + 5*time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("no heartbeat within %v: %v", HeartbeatPeriod+5*time.Second, err)
	}
	var msg map[string]interface{}
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("heartbeat is not valid JSON: %v", err)
	}
	if msg["type"] != "hub_heartbeat" {
		t.Fatalf("got type %v, want hub_heartbeat", msg["type"])
	}
}

// TestHubClientDisconnectCleansUp verifies that a client closing its side is
// removed from the hub and later broadcasts do not error or leak.
func TestHubClientDisconnectCleansUp(t *testing.T) {
	hub := NewHub()
	defer hub.Close()

	srv := newTestServer(t, hub)
	defer srv.Close()

	conn := dialTestServer(t, srv, hub)
	conn.Close()

	deadline := time.Now().Add(3 * time.Second)
	for hub.ClientCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("client not removed after disconnect; %d still registered", hub.ClientCount())
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Broadcasting with no clients must not panic or block.
	hub.BroadcastEvent("after_disconnect", nil)
}

// TestHubConcurrentBroadcastAndConnect exercises concurrent broadcasts,
// connects, and disconnects (run with -race).
func TestHubConcurrentBroadcastAndConnect(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	go hub.Run()

	srv := newTestServer(t, hub)
	defer srv.Close()

	stop := make(chan struct{})
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				hub.BroadcastEvent("evt", map[string]interface{}{"seq": i})
			}
		}
	}()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	for i := 0; i < 20; i++ {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Fatalf("conn %d read: %v", i, err)
		}
		conn.Close()
	}
	close(stop)
}

// newTestServerForUser is newTestServer with a controllable client user ID,
// for tenant-scoped delivery tests.
func newTestServerForUser(t *testing.T, hub *Hub, userID int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("upgrade error: %v", err)
			return
		}
		hub.Serve(conn, userID)
	}))
}

// TestBroadcastEventForCompanyDeliversOnlyToOwner verifies tenant scoping:
// an event for company 10 (owned by user 1) reaches user 1's client and
// never user 2's; with an unknown owner it reaches nobody.
func TestBroadcastEventForCompanyDeliversOnlyToOwner(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	hub.SetCompanyRecipientsResolver(func(companyID int32) ([]int32, bool) {
		if companyID == 10 {
			return []int32{1}, true
		}
		return nil, false
	})

	srvOwner := newTestServerForUser(t, hub, 1)
	defer srvOwner.Close()
	srvOther := newTestServerForUser(t, hub, 2)
	defer srvOther.Close()

	ownerConn := dialTestServer(t, srvOwner, hub)
	defer ownerConn.Close()
	otherConn := dialTestServer(t, srvOther, hub)
	defer otherConn.Close()

	hub.BroadcastEventForCompany(10, "task_updated", map[string]interface{}{"id": 1})
	hub.BroadcastEventForCompany(99, "task_updated", map[string]interface{}{"id": 2}) // unknown owner → nobody

	ownerConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := ownerConn.ReadMessage()
	if err != nil {
		t.Fatalf("owner never received the event: %v", err)
	}
	var ev struct {
		Type    string                 `json:"type"`
		Payload map[string]interface{} `json:"payload"`
	}
	if err := json.Unmarshal(msg, &ev); err != nil || ev.Type != "task_updated" || ev.Payload["id"].(float64) != 1 {
		t.Fatalf("owner got wrong event: %s", msg)
	}

	// The other user must receive nothing (a short deadline read must time out).
	otherConn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, msg, err := otherConn.ReadMessage(); err == nil {
		t.Fatalf("non-owner received a tenant event: %s", msg)
	}
}
