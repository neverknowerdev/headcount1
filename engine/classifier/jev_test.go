package classifier

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAskSendsTypedQuestionsAndReadsEachAnswer(t *testing.T) {
	var received map[string]interface{}
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/systemone", r.URL.Path)
		authorization = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &received))
		w.Write([]byte(`{"model":"jev-1.13.0","answers":{
			"abandoned":{"type":"noul","noul":0.92},
			"same_as":{"type":"choice","choice":"r7","confidence":0.81,"probabilities":{"r7":0.81,"none":0.19}}},
			"usage":{"input_tokens":120,"output_tokens":2}}`))
	}))
	defer server.Close()

	// A base URL given with or without /v1 addresses the same endpoint.
	client := New(server.URL+"/v1/", "secret-key", "")
	answers, result, err := client.Ask(context.Background(), "the agent tried x and gave up", map[string]Question{
		"abandoned": Noul("Did the agent give up on an approach?"),
		"same_as":   Choice("Which record says the same?", map[string]string{"r7": "Use sessions", "none": "None of them"}),
	})
	require.NoError(t, err)

	assert.Equal(t, "Bearer secret-key", authorization)
	assert.Equal(t, "the agent tried x and gave up", received["state"])
	assert.Equal(t, DefaultModel, received["model"])
	questions := received["questions"].(map[string]interface{})
	assert.Equal(t, map[string]interface{}{"type": "noul", "instructions": "Did the agent give up on an approach?"}, questions["abandoned"])
	assert.Equal(t, map[string]interface{}{"r7": "Use sessions", "none": "None of them"}, questions["same_as"].(map[string]interface{})["criteria"])

	assert.InDelta(t, 0.92, answers["abandoned"].Noul, 0.0001)
	assert.Equal(t, "r7", answers["same_as"].Choice)
	assert.InDelta(t, 0.81, answers["same_as"].Confidence, 0.0001)
	assert.Equal(t, "jev-1.13.0", result.Model)
	assert.Equal(t, 120, result.InputTokens)
	assert.Equal(t, 2, result.OutputTokens)
}

func TestAskRetriesWhenRateLimitedOrOverloadedAndGivesUpOnOtherErrors(t *testing.T) {
	var calls atomic.Int32
	statuses := []int{http.StatusTooManyRequests, 529, http.StatusOK}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := statuses[calls.Add(1)-1]
		w.WriteHeader(status)
		if status == http.StatusOK {
			w.Write([]byte(`{"answers":{"q":{"noul":0.1}}}`))
		}
	}))
	defer server.Close()
	client := New(server.URL, "k", "jev-latest")
	client.Backoff = []time.Duration{time.Millisecond, time.Millisecond}

	answers, _, err := client.Ask(context.Background(), "state", map[string]Question{"q": Noul("?")})
	require.NoError(t, err)
	assert.Equal(t, int32(3), calls.Load())
	assert.InDelta(t, 0.1, answers["q"].Noul, 0.0001)

	// Still overloaded after every retry: the error is returned, not looped on.
	calls.Store(0)
	statuses = []int{529, 529, 529, 529}
	_, _, err = client.Ask(context.Background(), "state", map[string]Question{"q": Noul("?")})
	var status *StatusError
	require.ErrorAs(t, err, &status)
	assert.Equal(t, 529, status.Status)
	assert.Equal(t, int32(3), calls.Load(), "one attempt and two retries")

	// A bad key is not retried.
	calls.Store(0)
	statuses = []int{http.StatusUnauthorized, http.StatusOK}
	_, _, err = client.Ask(context.Background(), "state", map[string]Question{"q": Noul("?")})
	require.ErrorAs(t, err, &status)
	assert.Equal(t, http.StatusUnauthorized, status.Status)
	assert.Equal(t, int32(1), calls.Load())
}

func TestAskKeepsTheEndOfAStateThatIsTooLong(t *testing.T) {
	var state string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			State string `json:"state"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		state = body.State
		w.Write([]byte(`{"answers":{}}`))
	}))
	defer server.Close()

	long := strings.Repeat("old ", MaxStateChars) + "THE LATEST EVENT"
	_, _, err := New(server.URL, "k", "").Ask(context.Background(), long, map[string]Question{"q": Noul("?")})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(state), MaxStateChars+40)
	assert.True(t, strings.HasSuffix(state, "THE LATEST EVENT"), "the most recent events are what a gate is asked about")
	assert.True(t, strings.HasPrefix(state, "[earlier part omitted]"))
}

func TestAskWithNoQuestionsMakesNoRequest(t *testing.T) {
	client := New("http://127.0.0.1:1", "k", "")
	answers, _, err := client.Ask(context.Background(), "state", nil)
	require.NoError(t, err)
	assert.Empty(t, answers)
}
