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
		client := &Client{Hub: hub, Conn: conn, Send: make(chan []byte, 256)}
		client.Hub.Register <- client
		go client.WritePump()
		client.ReadPump()
	}))
}

// dialTestServer connects a WebSocket client to the test server and waits
// briefly for the hub to register the client.
func dialTestServer(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// Give the hub goroutine time to register the client before we broadcast.
	time.Sleep(30 * time.Millisecond)
	return conn
}

// TestHubDeliversEachMessageAsSeparateFrame verifies that when multiple events
// are broadcast in rapid succession each one arrives as a distinct WebSocket
// frame containing exactly one valid JSON object. This guards against the
// WritePump batching bug where several messages were concatenated into a
// single frame and client-side JSON.parse() would fail.
func TestHubDeliversEachMessageAsSeparateFrame(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	srv := newTestServer(t, hub)
	defer srv.Close()

	conn := dialTestServer(t, srv)
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
		// Each frame must be exactly one valid JSON object.
		var msg map[string]interface{}
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("frame %d is not valid JSON: %v\nraw: %s", received, err, data)
		}
		if msg["type"] != "test_event" {
			t.Fatalf("frame %d: unexpected type %v", received, msg["type"])
		}
		received++
	}

	if received != n {
		t.Errorf("got %d frames, want %d", received, n)
	}
}

// TestHubBroadcastReachesAllClients verifies that a single broadcast event
// is delivered to every connected client.
func TestHubBroadcastReachesAllClients(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	srv := newTestServer(t, hub)
	defer srv.Close()

	const clients = 3
	conns := make([]*websocket.Conn, clients)
	for i := range conns {
		conns[i] = dialTestServer(t, srv)
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
	go hub.Run()

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
