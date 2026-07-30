package deploysync

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/nacl/box"
)

// ── Vercel ────────────────────────────────────────────────────────────────

func TestVercelPushAllUpsertsWithCorrectTypes(t *testing.T) {
	var gotPath, gotAuth, gotQuery string
	var gotItems []vercelEnvItem
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &gotItems))
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"created":[],"failed":[]}`))
	}))
	defer srv.Close()
	t.Setenv("VERCEL_API_URL", srv.URL)

	target := NewVercelTarget("vtok-123", "prj_abc", "production", "team_1")
	err := target.PushAll(context.Background(), []Entry{
		{Name: "API_KEY", Value: "s3cret", Kind: "secret"},
		{Name: "NODE_ENV", Value: "production", Kind: "variable"},
	})
	require.NoError(t, err)

	assert.Equal(t, "/v10/projects/prj_abc/env", gotPath)
	assert.Contains(t, gotQuery, "upsert=true")
	assert.Contains(t, gotQuery, "teamId=team_1")
	assert.Equal(t, "Bearer vtok-123", gotAuth)
	require.Len(t, gotItems, 2)
	byKey := map[string]vercelEnvItem{}
	for _, it := range gotItems {
		byKey[it.Key] = it
	}
	assert.Equal(t, "sensitive", byKey["API_KEY"].Type, "secrets push as write-only sensitive vars")
	assert.Equal(t, "plain", byKey["NODE_ENV"].Type, "variables push as plain vars")
	assert.Equal(t, []string{"production"}, byKey["API_KEY"].Target)
}

func TestVercelSecretTypeFallsBackForDevelopment(t *testing.T) {
	v := &VercelTarget{Environment: "development"}
	assert.Equal(t, "encrypted", v.vercelType("secret"), "sensitive is rejected on development — fall back to encrypted")
	v = &VercelTarget{Environment: "env_custom123"}
	assert.Equal(t, "sensitive", v.vercelType("secret"))
}

func TestVercelCustomEnvironmentUsesCustomIDs(t *testing.T) {
	var gotItems []vercelEnvItem
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &gotItems)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	t.Setenv("VERCEL_API_URL", srv.URL)

	target := NewVercelTarget("t", "p", "env_custom42", "")
	require.NoError(t, target.PushAll(context.Background(), []Entry{{Name: "A", Value: "v", Kind: "variable"}}))
	require.Len(t, gotItems, 1)
	assert.Equal(t, []string{"env_custom42"}, gotItems[0].CustomEnvironmentIDs)
	assert.Empty(t, gotItems[0].Target)
}

func TestVercelPushReportsPerItemFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"created":[],"failed":[{"error":{"key":"API_KEY","message":"invalid target"}}]}`))
	}))
	defer srv.Close()
	t.Setenv("VERCEL_API_URL", srv.URL)

	target := NewVercelTarget("t", "p", "production", "")
	err := target.PushAll(context.Background(), []Entry{{Name: "API_KEY", Value: "v", Kind: "secret"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid target")
}

func TestVercelDeleteFindsByKeyAndEnvironment(t *testing.T) {
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			w.Write([]byte(`{"envs":[
				{"id":"e1","key":"API_KEY","target":["production"]},
				{"id":"e2","key":"API_KEY","target":["preview"]},
				{"id":"e3","key":"OTHER","target":["production"]}
			]}`))
		case r.Method == http.MethodDelete:
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()
	t.Setenv("VERCEL_API_URL", srv.URL)

	target := NewVercelTarget("t", "p", "production", "")
	require.NoError(t, target.Delete(context.Background(), "API_KEY"))
	require.Len(t, deleted, 1, "only the production-target row is deleted")
	assert.Equal(t, "/v9/projects/p/env/e1", deleted[0])
}

func TestListVercelProjectsAndEnvironments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v10/projects":
			w.Write([]byte(`{"projects":[{"id":"prj_1","name":"web"},{"id":"prj_2","name":"api"}]}`))
		case "/v9/projects/prj_1/custom-environments":
			w.Write([]byte(`{"environments":[{"id":"env_c1","slug":"staging"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	t.Setenv("VERCEL_API_URL", srv.URL)

	projects, err := ListVercelProjects(context.Background(), "t", "")
	require.NoError(t, err)
	require.Len(t, projects, 2)

	envs, err := ListVercelEnvironments(context.Background(), "t", "", "prj_1")
	require.NoError(t, err)
	ids := make([]string, 0, len(envs))
	for _, e := range envs {
		ids = append(ids, e.ID)
	}
	assert.Equal(t, []string{"production", "preview", "development", "env_c1"}, ids)
}

// ── GitHub ────────────────────────────────────────────────────────────────

func TestGitHubPushSecretSealsWithEnvPublicKey(t *testing.T) {
	pub, priv, err := box.GenerateKey(rand.Reader)
	require.NoError(t, err)

	var gotSecretBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/acme/web/environments/production/secrets/public-key":
			json.NewEncoder(w).Encode(map[string]string{
				"key_id": "key-1",
				"key":    base64.StdEncoding.EncodeToString(pub[:]),
			})
		case r.Method == http.MethodPut && r.URL.Path == "/repos/acme/web/environments/production/secrets/API_KEY":
			body, _ := io.ReadAll(r.Body)
			require.NoError(t, json.Unmarshal(body, &gotSecretBody))
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	t.Setenv("GITHUB_API_URL", srv.URL)

	target := NewGitHubTarget("ghtok", "acme/web", "production")
	require.NoError(t, target.PushAll(context.Background(), []Entry{
		{Name: "API_KEY", Value: "the-secret-value", Kind: "secret"},
	}))

	require.NotNil(t, gotSecretBody)
	assert.Equal(t, "key-1", gotSecretBody["key_id"])
	// The sealed box must decrypt back to the original value with the
	// environment private key — proving real crypto_box_seal semantics.
	sealed, err := base64.StdEncoding.DecodeString(gotSecretBody["encrypted_value"])
	require.NoError(t, err)
	plain, ok := box.OpenAnonymous(nil, sealed, pub, priv)
	require.True(t, ok, "sealed box must open with the recipient key")
	assert.Equal(t, "the-secret-value", string(plain))
}

func TestGitHubPushVariablePostThenPatchOnConflict(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	t.Setenv("GITHUB_API_URL", srv.URL)

	target := NewGitHubTarget("t", "acme/web", "production")
	require.NoError(t, target.PushAll(context.Background(), []Entry{
		{Name: "NODE_ENV", Value: "production", Kind: "variable"},
	}))
	require.Equal(t, []string{
		"POST /repos/acme/web/environments/production/variables",
		"PATCH /repos/acme/web/environments/production/variables/NODE_ENV",
	}, methods)
}

func TestGitHubDeleteRemovesBothKindsIgnoring404(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	t.Setenv("GITHUB_API_URL", srv.URL)

	target := NewGitHubTarget("t", "acme/web", "production")
	require.NoError(t, target.Delete(context.Background(), "API_KEY"))
	assert.Equal(t, []string{
		"/repos/acme/web/environments/production/secrets/API_KEY",
		"/repos/acme/web/environments/production/variables/API_KEY",
	}, paths)
}

func TestGitHubDiscovery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user/repos":
			w.Write([]byte(`[{"full_name":"acme/web"},{"full_name":"acme/api"}]`))
		case "/repos/acme/web/environments":
			w.Write([]byte(`{"environments":[{"name":"production"},{"name":"staging"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	t.Setenv("GITHUB_API_URL", srv.URL)

	repos, err := ListGitHubRepos(context.Background(), "t")
	require.NoError(t, err)
	require.Len(t, repos, 2)

	envs, err := ListGitHubEnvironments(context.Background(), "t", "acme/web")
	require.NoError(t, err)
	assert.Equal(t, []string{"production", "staging"}, envs)
}

func TestGitHubPushSecretErrorSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()
	t.Setenv("GITHUB_API_URL", srv.URL)

	target := NewGitHubTarget("bad", "acme/web", "production")
	err := target.PushAll(context.Background(), []Entry{{Name: "A", Value: "v", Kind: "secret"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Bad credentials")
}
