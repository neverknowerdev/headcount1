package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNormalizePrivateKey(t *testing.T) {
	value := "  -----BEGIN RSA PRIVATE KEY-----\\nabc\\n-----END RSA PRIVATE KEY-----  "
	require.Equal(t, "-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----", normalizePrivateKey(value))
}

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

func TestInstallationTokenScopesToSelectedRepository(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	encoded := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	client := &Client{config: Config{AppID: "123", PrivateKey: string(encoded)}}
	client.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/app/installations/77/access_tokens", r.URL.Path)
		require.Contains(t, r.Header.Get("Authorization"), "Bearer ")
		body, readErr := io.ReadAll(r.Body)
		require.NoError(t, readErr)
		require.JSONEq(t, `{"repository_ids":[88]}`, string(body))
		return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"token":"short-lived"}`))}, nil
	})}

	token, err := client.InstallationToken(context.Background(), 77, 88)
	require.NoError(t, err)
	require.Equal(t, "short-lived", token)
}
