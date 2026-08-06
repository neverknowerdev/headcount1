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

func TestAuthorizeURLCanForceGitHubAccountPicker(t *testing.T) {
	client := &Client{config: Config{ClientID: "client"}}
	authorizeURL := client.AuthorizeURL("state", "https://app.example/api/github/callback", AuthorizeOptions{SelectAccount: true})
	require.Contains(t, authorizeURL, "prompt=select_account")
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

func TestUserReadsStableGitHubIdentity(t *testing.T) {
	client := &Client{}
	client.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "/user", r.URL.Path)
		require.Equal(t, "Bearer user-token", r.Header.Get("Authorization"))
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":42,"login":"octocat"}`))}, nil
	})}
	user, err := client.User(context.Background(), "user-token")
	require.NoError(t, err)
	require.Equal(t, int64(42), user.ID)
	require.Equal(t, "octocat", user.Login)
}

func TestRepositorySlugValidatesGitHubHTTPSURL(t *testing.T) {
	slug, err := RepositorySlug("https://github.com/acme/widgets.git")
	require.NoError(t, err)
	require.Equal(t, "acme/widgets", slug)
	_, err = RepositorySlug("git@github.com:acme/widgets.git")
	require.Error(t, err)
}

func TestFindOpenPullRequestByHead(t *testing.T) {
	client := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "/repos/acme/widgets/pulls", r.URL.Path)
		require.Equal(t, "open", r.URL.Query().Get("state"))
		require.Equal(t, "acme:headcount1/task-7", r.URL.Query().Get("head"))
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`[{"number":12,"html_url":"https://github.com/acme/widgets/pull/12"}]`))}, nil
	})}}
	pull, found, err := client.FindOpenPullRequestByHead(t.Context(), "token", "acme/widgets", "acme:headcount1/task-7")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 12, pull.Number)
}

func TestUserInstallationsFollowsPagination(t *testing.T) {
	var paths []string
	client := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.RequestURI())
		header := make(http.Header)
		if len(paths) == 1 {
			header.Set("Link", `<https://api.github.com/user/installations?page=2&per_page=100>; rel="next"`)
			return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(`{"installations":[{"id":1,"account":{"login":"personal"}}]}`))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(`{"installations":[{"id":2,"account":{"login":"work"}}]}`))}, nil
	})}}

	installations, err := client.UserInstallations(context.Background(), "user-token")
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2}, []int64{installations[0].ID, installations[1].ID})
	require.Equal(t, []string{"/user/installations?per_page=100", "/user/installations?page=2&per_page=100"}, paths)
}

func TestUserRepositoriesFollowsPagination(t *testing.T) {
	requests := 0
	client := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		header := make(http.Header)
		if requests == 1 {
			header.Set("Link", `<https://api.github.com/user/installations/7/repositories?page=2&per_page=100>; rel="next"`)
			return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(`{"repositories":[{"id":1,"full_name":"a/one"}]}`))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(`{"repositories":[{"id":2,"full_name":"a/two"}]}`))}, nil
	})}}

	repositories, err := client.UserRepositories(context.Background(), "user-token", 7)
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2}, []int64{repositories[0].ID, repositories[1].ID})
	require.Equal(t, 2, requests)
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
