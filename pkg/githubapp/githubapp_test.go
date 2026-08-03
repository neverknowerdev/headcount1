package githubapp

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestExchangeCodeIncludesAuthorizationRedirectURI(t *testing.T) {
	client := &Client{config: Config{ClientID: "client", ClientSecret: "secret"}}
	client.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		form := string(body)
		require.Contains(t, form, "redirect_uri=https%3A%2F%2Fstagingapp.headcount1.ai%2Fapi%2Fgithub%2Fcallback")
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"token"}`))}, nil
	})}

	token, err := client.ExchangeCode(context.Background(), "code", "https://stagingapp.headcount1.ai/api/github/callback")
	require.NoError(t, err)
	require.Equal(t, "token", token)
}
