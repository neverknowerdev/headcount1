package aicli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// FixtureEntry is one recorded HTTP interaction: a request body and the
// corresponding response status + body that should be replayed.
type FixtureEntry struct {
	RequestBody  json.RawMessage `json:"request_body,omitempty"`
	ResponseBody json.RawMessage `json:"response_body"`
	Status       int             `json:"status"`
}

// FixtureTransport implements http.RoundTripper. In record mode it forwards
// requests to the real provider and saves each exchange to a fixture file; in
// playback mode it returns entries from the file in sequence.
//
// Usage — record:
//
//	os.Setenv("RECORD", "true")
//	client.HTTPClient.Transport = NewFixtureTransport("testdata/fixtures/my_test.json", http.DefaultTransport)
//
// Usage — playback (default):
//
//	client.HTTPClient.Transport = NewFixtureTransport("testdata/fixtures/my_test.json", nil)
type FixtureTransport struct {
	path     string
	mu       sync.Mutex
	entries  []FixtureEntry
	index    int
	record   bool
	upstream http.RoundTripper
}

// NewFixtureTransport creates a FixtureTransport.
// When RECORD=true it forwards to upstream and saves responses.
// When RECORD is unset it replays from the file at path.
// upstream is only used in record mode; pass nil for playback-only use.
func NewFixtureTransport(path string, upstream http.RoundTripper) *FixtureTransport {
	ft := &FixtureTransport{
		path:     path,
		record:   os.Getenv("RECORD") == "true",
		upstream: upstream,
	}
	if !ft.record {
		ft.loadFixtures()
	}
	return ft
}

func (ft *FixtureTransport) loadFixtures() {
	data, err := os.ReadFile(ft.path)
	if err != nil {
		// No fixture file yet — will fail on first call with a clear message.
		return
	}
	if err := json.Unmarshal(data, &ft.entries); err != nil {
		panic(fmt.Sprintf("FixtureTransport: corrupt fixture %s: %v", ft.path, err))
	}
}

func (ft *FixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))

	if ft.record {
		return ft.recordRoundTrip(req, body)
	}
	return ft.playbackRoundTrip(body)
}

func (ft *FixtureTransport) recordRoundTrip(req *http.Request, body []byte) (*http.Response, error) {
	// Clone request so we can re-set the body.
	cloned := req.Clone(req.Context())
	cloned.Body = io.NopCloser(bytes.NewReader(body))

	resp, err := ft.upstream.RoundTrip(cloned)
	if err != nil {
		return nil, err
	}

	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(respBody))

	entry := FixtureEntry{
		RequestBody:  json.RawMessage(body),
		ResponseBody: json.RawMessage(respBody),
		Status:       resp.StatusCode,
	}

	ft.mu.Lock()
	ft.entries = append(ft.entries, entry)
	ft.saveFixtures()
	ft.mu.Unlock()

	return resp, nil
}

func (ft *FixtureTransport) playbackRoundTrip(_ []byte) (*http.Response, error) {
	ft.mu.Lock()
	defer ft.mu.Unlock()

	if ft.index >= len(ft.entries) {
		return nil, fmt.Errorf(
			"FixtureTransport: no more responses in %s (index=%d, len=%d). "+
				"Run with RECORD=true to re-record.",
			ft.path, ft.index, len(ft.entries),
		)
	}
	entry := ft.entries[ft.index]
	ft.index++

	return &http.Response{
		StatusCode: entry.Status,
		Body:       io.NopCloser(bytes.NewReader(entry.ResponseBody)),
		Header:     http.Header{"Content-Type": {"application/json"}},
	}, nil
}

func (ft *FixtureTransport) saveFixtures() {
	if err := os.MkdirAll(filepath.Dir(ft.path), 0755); err != nil {
		return
	}
	data, err := json.MarshalIndent(ft.entries, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(ft.path, data, 0644)
}

// Reset rewinds the playback index to 0. Useful for sub-tests that share a
// fixture transport.
func (ft *FixtureTransport) Reset() {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.index = 0
}
