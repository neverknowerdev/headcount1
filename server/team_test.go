package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"agent-orchestrator/db"
	endpoints "agent-orchestrator/server/controllers"
)

func setupTeamTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(
		&db.User{}, &db.Session{}, &db.PasswordResetToken{},
		&db.Team{}, &db.TeamMember{}, &db.TeamInvite{},
		&db.Company{},
	))
	return database
}

func setupTeamRouter(database *gorm.DB) chi.Router {
	api := endpoints.NewAPI(database, nil, nil)
	r := chi.NewRouter()
	r.Post("/auth/register", api.Register)
	r.Get("/invite-info", api.InviteInfo)
	r.Group(func(r chi.Router) {
		r.Use(api.RequireAuth)
		r.Get("/team", api.GetTeam)
		r.Put("/team", api.UpdateTeam)
		r.Post("/team/invites", api.CreateTeamInvite)
		r.Delete("/team/invites/{id}", api.DeleteTeamInvite)
		r.Get("/companies", api.ListCompanies)
		r.Post("/companies", api.CreateCompany)
	})
	return r
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

	w := postJSON(t, r, "/auth/register", map[string]string{"email": "boss@corp.io", "password": "password-boss"})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	cookie := sessionCookie(t, w)

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
	endpoints.SetMailer(rec)

	// Owner registers and invites a teammate.
	w := postJSON(t, r, "/auth/register", map[string]string{"email": "boss@corp.io", "password": "password-boss"})
	require.Equal(t, http.StatusCreated, w.Code)
	ownerCookie := sessionCookie(t, w)

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
	mw := postJSON(t, r, "/auth/register", map[string]string{
		"email": "dev@corp.io", "password": "password-dev", "invite_token": token,
	})
	require.Equal(t, http.StatusCreated, mw.Code, mw.Body.String())
	memberCookie := sessionCookie(t, mw)

	tw, team := getJSON(t, r, "/team", memberCookie)
	require.Equal(t, http.StatusOK, tw.Code)
	assert.Equal(t, "boss's team", team["name"])
	assert.Equal(t, "member", team["role"])
	assert.Len(t, team["members"].([]any), 2)
	_, hasInvites := team["invites"]
	assert.False(t, hasInvites, "members don't see the invites list")

	// The token is single-use.
	rw := postJSON(t, r, "/auth/register", map[string]string{
		"email": "other@corp.io", "password": "password-oth", "invite_token": token,
	})
	require.Equal(t, http.StatusBadRequest, rw.Code)
}

func TestOnlyOwnerCanInviteAndRevoke(t *testing.T) {
	database := setupTeamTestDB(t)
	r := setupTeamRouter(database)
	rec := &recordingMailer{}
	endpoints.SetMailer(rec)

	w := postJSON(t, r, "/auth/register", map[string]string{"email": "boss@corp.io", "password": "password-boss"})
	ownerCookie := sessionCookie(t, w)
	iw := postJSON(t, r, "/team/invites", map[string]string{"email": "dev@corp.io"}, ownerCookie)
	require.Equal(t, http.StatusCreated, iw.Code)
	var created struct {
		InviteURL string `json:"invite_url"`
		Invite    struct {
			ID int32 `json:"id"`
		} `json:"invite"`
	}
	require.NoError(t, json.Unmarshal(iw.Body.Bytes(), &created))
	token := inviteURLRe.FindStringSubmatch(created.InviteURL)[1]

	mw := postJSON(t, r, "/auth/register", map[string]string{
		"email": "dev@corp.io", "password": "password-dev", "invite_token": token,
	})
	memberCookie := sessionCookie(t, mw)

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
	xw := postJSON(t, r, "/auth/register", map[string]string{
		"email": "late@corp.io", "password": "password-late", "invite_token": token2,
	})
	require.Equal(t, http.StatusBadRequest, xw.Code, "revoked invite must not work")
}

func TestTeamMembersShareCompanies(t *testing.T) {
	t.Setenv("E2E_HEADCOUNT1_HOME", t.TempDir()) // isolated home for company dirs
	database := setupTeamTestDB(t)
	r := setupTeamRouter(database)
	rec := &recordingMailer{}
	endpoints.SetMailer(rec)

	w := postJSON(t, r, "/auth/register", map[string]string{"email": "boss@corp.io", "password": "password-boss"})
	ownerCookie := sessionCookie(t, w)
	cw := postJSON(t, r, "/companies", map[string]string{"name": "Acme", "short_name": "acme"}, ownerCookie)
	require.Equal(t, http.StatusCreated, cw.Code)

	iw := postJSON(t, r, "/team/invites", map[string]string{"email": "dev@corp.io"}, ownerCookie)
	var created struct {
		InviteURL string `json:"invite_url"`
	}
	require.NoError(t, json.Unmarshal(iw.Body.Bytes(), &created))
	token := inviteURLRe.FindStringSubmatch(created.InviteURL)[1]
	mw := postJSON(t, r, "/auth/register", map[string]string{
		"email": "dev@corp.io", "password": "password-dev", "invite_token": token,
	})
	memberCookie := sessionCookie(t, mw)

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
	ow := postJSON(t, r, "/auth/register", map[string]string{"email": "rival@else.io", "password": "password-rival"})
	rivalCookie := sessionCookie(t, ow)
	req = httptest.NewRequest(http.MethodGet, "/companies", nil)
	req.AddCookie(rivalCookie)
	rw := httptest.NewRecorder()
	r.ServeHTTP(rw, req)
	var rivalCompanies []db.Company
	require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &rivalCompanies))
	assert.Empty(t, rivalCompanies)
}
