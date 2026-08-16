package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/authctx"
	endpoints "agent-orchestrator/server/controllers"
)

func setupTeamTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.EnsureSchema(database))
	return database
}

func setupTeamRouter(database *gorm.DB) chi.Router {
	api := endpoints.NewAPI(database, nil, nil)
	r := chi.NewRouter()
	r.Get("/invite-info", api.InviteInfo)
	r.Group(func(r chi.Router) {
		r.Use(api.RequireAuth)
		r.Get("/team", api.GetTeam)
		r.Put("/team", api.UpdateTeam)
		r.Post("/team/invites", api.CreateTeamInvite)
		r.Delete("/team/invites/{id}", api.DeleteTeamInvite)
		r.Get("/companies", api.ListCompanies)
		// Mirror production: creating a company is owner-only.
		r.With(api.RequireTeamOwner).Post("/companies", api.CreateCompany)
		// Mirror production: per-user export/import is owner-only too (it
		// covers the whole team's subtree).
		r.Route("/data", func(r chi.Router) {
			r.Use(api.RequireTeamOwner)
			r.Get("/export", api.ExportMyData)
			r.Post("/import", api.ImportMyData)
		})
	})
	return r
}

// tryRegister mirrors the passkey RegisterFinish account setup (validate
// invite → create user → accept invite or create team) without the WebAuthn
// ceremony, and returns a session cookie. Used by the team tests, which
// exercise team logic rather than the auth mechanism.
func tryRegister(database *gorm.DB, email, inviteToken string) (*http.Cookie, error) {
	q := db.New(database)
	ctx := context.Background()
	if _, err := q.GetUserByEmail(ctx, email); err == nil {
		return nil, fmt.Errorf("an account with this email already exists")
	}
	if inviteToken != "" {
		inv, err := q.GetTeamInviteByTokenHash(ctx, authctx.HashToken(inviteToken))
		if err != nil {
			return nil, err
		}
		user, err := q.CreateUser(ctx, email)
		if err != nil {
			return nil, err
		}
		if err := q.AcceptTeamInvite(ctx, inv, user.ID); err != nil {
			return nil, err
		}
		return sessionCookieForUser(database, user.ID), nil
	}
	user, err := q.CreateUser(ctx, email)
	if err != nil {
		return nil, err
	}
	if err := q.EnsureTeamForUser(ctx, user); err != nil {
		return nil, err
	}
	return sessionCookieForUser(database, user.ID), nil
}

func registerHelper(t *testing.T, database *gorm.DB, email, inviteToken string) *http.Cookie {
	t.Helper()
	c, err := tryRegister(database, email, inviteToken)
	require.NoError(t, err)
	return c
}

func sessionCookieForUser(database *gorm.DB, userID int32) *http.Cookie {
	raw, _ := authctx.NewToken()
	_, _ = db.New(database).CreateSession(context.Background(), userID, authctx.HashToken(raw), time.Now().Add(db.SessionAbsoluteCap()))
	return &http.Cookie{Name: authctx.CookieName, Value: raw}
}

func getJSON(t *testing.T, r chi.Router, path string, cookie *http.Cookie) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w, body
}

var inviteURLRe = regexp.MustCompile(`register\?invite=([A-Za-z0-9_-]+)`)

func TestTeamCreatedAtRegistrationWithOwner(t *testing.T) {
	database := setupTeamTestDB(t)
	r := setupTeamRouter(database)

	cookie := registerHelper(t, database, "boss@corp.io", "")

	tw, team := getJSON(t, r, "/team", cookie)
	require.Equal(t, http.StatusOK, tw.Code, tw.Body.String())
	assert.Equal(t, "boss's team", team["name"])
	assert.Equal(t, "owner", team["role"])
	members := team["members"].([]any)
	require.Len(t, members, 1)
	assert.Equal(t, "owner", members[0].(map[string]any)["role"])
	_, hasInvites := team["invites"]
	assert.True(t, hasInvites, "owner sees the invites list")
}

func TestInviteFlowJoinsTeamAsMember(t *testing.T) {
	database := setupTeamTestDB(t)
	r := setupTeamRouter(database)
	rec := &recordingMailer{}
	t.Setenv("APP_BASE_URL", "http://localhost:8080")
	endpoints.SetMailer(rec)

	ownerCookie := registerHelper(t, database, "boss@corp.io", "")

	iw := postJSON(t, r, "/team/invites", map[string]string{"email": "dev@corp.io"}, ownerCookie)
	require.Equal(t, http.StatusCreated, iw.Code, iw.Body.String())
	var created struct {
		InviteURL string `json:"invite_url"`
	}
	require.NoError(t, json.Unmarshal(iw.Body.Bytes(), &created))
	match := inviteURLRe.FindStringSubmatch(created.InviteURL)
	require.NotNil(t, match, "invite response carries the link")
	token := match[1]

	// The public invite-info endpoint names the team for the register page.
	infoW, info := getJSON(t, r, "/invite-info?token="+token, nil)
	require.Equal(t, http.StatusOK, infoW.Code)
	assert.Equal(t, "boss's team", info["team_name"])
	assert.Equal(t, "dev@corp.io", info["email"])

	// Owner creates a company before the teammate joins.
	cw := postJSON(t, r, "/companies", map[string]string{"name": "Acme", "short_name": "acme"}, ownerCookie)
	require.Equal(t, http.StatusCreated, cw.Code)

	// Teammate registers through the invite → member of the same team.
	memberCookie := registerHelper(t, database, "dev@corp.io", token)

	tw, team := getJSON(t, r, "/team", memberCookie)
	require.Equal(t, http.StatusOK, tw.Code)
	assert.Equal(t, "boss's team", team["name"])
	assert.Equal(t, "member", team["role"])
	assert.Len(t, team["members"].([]any), 2)
	_, hasInvites := team["invites"]
	assert.False(t, hasInvites, "members don't see the invites list")

	// The token is single-use.
	_, err := tryRegister(database, "other@corp.io", token)
	require.Error(t, err, "a used invite token must not work again")
}

func TestOnlyOwnerCanCreateCompany(t *testing.T) {
	database := setupTeamTestDB(t)
	r := setupTeamRouter(database)
	t.Setenv("APP_BASE_URL", "http://localhost:8080")
	endpoints.SetMailer(&recordingMailer{})

	// Owner (registered without an invite) can create a company.
	ownerCookie := registerHelper(t, database, "boss@corp.io", "")
	cw := postJSON(t, r, "/companies", map[string]string{"name": "Acme", "short_name": "acme"}, ownerCookie)
	require.Equal(t, http.StatusCreated, cw.Code, cw.Body.String())

	// Invite a teammate, who joins as a member.
	iw := postJSON(t, r, "/team/invites", map[string]string{"email": "dev@corp.io"}, ownerCookie)
	require.Equal(t, http.StatusCreated, iw.Code)
	var created struct {
		InviteURL string `json:"invite_url"`
	}
	require.NoError(t, json.Unmarshal(iw.Body.Bytes(), &created))
	token := inviteURLRe.FindStringSubmatch(created.InviteURL)[1]
	memberCookie := registerHelper(t, database, "dev@corp.io", token)

	// The member is blocked from creating a company (owner-only) — 403, not 404.
	mw := postJSON(t, r, "/companies", map[string]string{"name": "Sneaky", "short_name": "sneak"}, memberCookie)
	require.Equal(t, http.StatusForbidden, mw.Code, mw.Body.String())
}

// TestOnlyOwnerCanExportOrImportData verifies that /data/export and
// /data/import — which cover the whole team's subtree, not just the caller's
// own rows — are gated the same way as creating a company: a member is
// rejected with 403 before the handler runs any business logic.
func TestOnlyOwnerCanExportOrImportData(t *testing.T) {
	database := setupTeamTestDB(t)
	r := setupTeamRouter(database)
	t.Setenv("APP_BASE_URL", "http://localhost:8080")
	endpoints.SetMailer(&recordingMailer{})

	ownerCookie := registerHelper(t, database, "boss@corp.io", "")

	iw := postJSON(t, r, "/team/invites", map[string]string{"email": "dev@corp.io"}, ownerCookie)
	require.Equal(t, http.StatusCreated, iw.Code)
	var created struct {
		InviteURL string `json:"invite_url"`
	}
	require.NoError(t, json.Unmarshal(iw.Body.Bytes(), &created))
	token := inviteURLRe.FindStringSubmatch(created.InviteURL)[1]
	memberCookie := registerHelper(t, database, "dev@corp.io", token)

	// A member is blocked from exporting or importing team data — 403, not 404.
	ew := getReq(t, r, "/data/export", memberCookie)
	require.Equal(t, http.StatusForbidden, ew.Code, ew.Body.String())

	iwReq := httptest.NewRequest(http.MethodPost, "/data/import", nil)
	iwReq.AddCookie(memberCookie)
	iwRec := httptest.NewRecorder()
	r.ServeHTTP(iwRec, iwReq)
	require.Equal(t, http.StatusForbidden, iwRec.Code, iwRec.Body.String())
}

func getReq(t *testing.T, r chi.Router, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestOnlyOwnerCanInviteAndRevoke(t *testing.T) {
	database := setupTeamTestDB(t)
	r := setupTeamRouter(database)
	rec := &recordingMailer{}
	t.Setenv("APP_BASE_URL", "http://localhost:8080")
	endpoints.SetMailer(rec)

	ownerCookie := registerHelper(t, database, "boss@corp.io", "")
	iw := postJSON(t, r, "/team/invites", map[string]string{"email": "dev@corp.io"}, ownerCookie)
	require.Equal(t, http.StatusCreated, iw.Code)
	var created struct {
		InviteURL string `json:"invite_url"`
	}
	require.NoError(t, json.Unmarshal(iw.Body.Bytes(), &created))
	token := inviteURLRe.FindStringSubmatch(created.InviteURL)[1]

	memberCookie := registerHelper(t, database, "dev@corp.io", token)

	// A member cannot invite, revoke, or rename.
	fw := postJSON(t, r, "/team/invites", map[string]string{"email": "third@corp.io"}, memberCookie)
	require.Equal(t, http.StatusForbidden, fw.Code)
	req := httptest.NewRequest(http.MethodDelete, "/team/invites/1", nil)
	req.AddCookie(memberCookie)
	dw := httptest.NewRecorder()
	r.ServeHTTP(dw, req)
	require.Equal(t, http.StatusForbidden, dw.Code)
	b, _ := json.Marshal(map[string]string{"name": "Renamed"})
	req = httptest.NewRequest(http.MethodPut, "/team", bytes.NewReader(b))
	req.AddCookie(memberCookie)
	uw := httptest.NewRecorder()
	r.ServeHTTP(uw, req)
	require.Equal(t, http.StatusForbidden, uw.Code)

	// Inviting an email that already has an account is rejected.
	cw := postJSON(t, r, "/team/invites", map[string]string{"email": "dev@corp.io"}, ownerCookie)
	require.Equal(t, http.StatusConflict, cw.Code)

	// Owner revokes a pending invite; registering with it then fails.
	iw2 := postJSON(t, r, "/team/invites", map[string]string{"email": "late@corp.io"}, ownerCookie)
	require.Equal(t, http.StatusCreated, iw2.Code)
	var created2 struct {
		InviteURL string `json:"invite_url"`
		Invite    struct {
			ID int32 `json:"id"`
		} `json:"invite"`
	}
	require.NoError(t, json.Unmarshal(iw2.Body.Bytes(), &created2))
	req = httptest.NewRequest(http.MethodDelete, "/team/invites/"+strconv.Itoa(int(created2.Invite.ID)), nil)
	req.AddCookie(ownerCookie)
	rw := httptest.NewRecorder()
	r.ServeHTTP(rw, req)
	require.Equal(t, http.StatusOK, rw.Code)
	token2 := inviteURLRe.FindStringSubmatch(created2.InviteURL)[1]
	_, err := tryRegister(database, "late@corp.io", token2)
	require.Error(t, err, "revoked invite must not work")
}

func TestTeamMembersShareCompanies(t *testing.T) {
	t.Setenv("E2E_HEADCOUNT1_HOME", t.TempDir()) // isolated home for company dirs
	database := setupTeamTestDB(t)
	r := setupTeamRouter(database)
	rec := &recordingMailer{}
	t.Setenv("APP_BASE_URL", "http://localhost:8080")
	endpoints.SetMailer(rec)

	ownerCookie := registerHelper(t, database, "boss@corp.io", "")
	cw := postJSON(t, r, "/companies", map[string]string{"name": "Acme", "short_name": "acme"}, ownerCookie)
	require.Equal(t, http.StatusCreated, cw.Code)

	iw := postJSON(t, r, "/team/invites", map[string]string{"email": "dev@corp.io"}, ownerCookie)
	var created struct {
		InviteURL string `json:"invite_url"`
	}
	require.NoError(t, json.Unmarshal(iw.Body.Bytes(), &created))
	token := inviteURLRe.FindStringSubmatch(created.InviteURL)[1]
	memberCookie := registerHelper(t, database, "dev@corp.io", token)

	// The member sees the team's company (created before they joined)...
	req := httptest.NewRequest(http.MethodGet, "/companies", nil)
	req.AddCookie(memberCookie)
	lw := httptest.NewRecorder()
	r.ServeHTTP(lw, req)
	require.Equal(t, http.StatusOK, lw.Code)
	var companies []db.Company
	require.NoError(t, json.Unmarshal(lw.Body.Bytes(), &companies))
	require.Len(t, companies, 1)
	assert.Equal(t, "Acme", companies[0].Name)

	// ...while an unrelated user does not.
	rivalCookie := registerHelper(t, database, "rival@else.io", "")
	req = httptest.NewRequest(http.MethodGet, "/companies", nil)
	req.AddCookie(rivalCookie)
	rw := httptest.NewRecorder()
	r.ServeHTTP(rw, req)
	var rivalCompanies []db.Company
	require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &rivalCompanies))
	assert.Empty(t, rivalCompanies)
}
