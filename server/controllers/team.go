package endpoints

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/authctx"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// requireMembership resolves the ctx user's team membership (every account
// has exactly one under current product rules).
func (api *API) requireMembership(r *http.Request) (db.TeamMember, error) {
	return api.q.GetTeamMembership(r.Context(), api.currentUserID(r))
}

// GetTeam returns the caller's team with members, the caller's role, and —
// for owners — the pending invites.
func (api *API) GetTeam(w http.ResponseWriter, r *http.Request) {
	membership, err := api.requireMembership(r)
	if err != nil {
		api.respondError(w, http.StatusNotFound, "no team membership")
		return
	}
	team, err := api.q.GetTeam(r.Context(), membership.TeamID)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	members, err := api.q.ListTeamMembers(r.Context(), team.ID)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := map[string]interface{}{
		"id":      team.ID,
		"name":    team.Name,
		"role":    membership.Role,
		"members": members,
	}
	if membership.Role == db.TeamRoleOwner {
		invites, _ := api.q.ListPendingTeamInvites(r.Context(), team.ID)
		if invites == nil {
			invites = []db.TeamInvite{}
		}
		resp["invites"] = invites
	}
	api.respondJSON(w, http.StatusOK, resp)
}

// UpdateTeam renames the team. Owner only.
func (api *API) UpdateTeam(w http.ResponseWriter, r *http.Request) {
	membership, err := api.requireMembership(r)
	if err != nil || membership.Role != db.TeamRoleOwner {
		api.respondError(w, http.StatusForbidden, "only the team owner can rename the team")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		api.respondError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := api.q.UpdateTeamName(r.Context(), membership.TeamID, strings.TrimSpace(req.Name)); err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// CreateTeamInvite emails an invite link. Owner only — the single capability
// that distinguishes roles today.
func (api *API) CreateTeamInvite(w http.ResponseWriter, r *http.Request) {
	membership, err := api.requireMembership(r)
	if err != nil || membership.Role != db.TeamRoleOwner {
		api.respondError(w, http.StatusForbidden, "only the team owner can invite members")
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid payload")
		return
	}
	email := db.NormalizeEmail(req.Email)
	if !looksLikeEmail(email) {
		api.respondError(w, http.StatusBadRequest, "a valid email address is required")
		return
	}
	if _, err := api.q.GetUserByEmail(r.Context(), email); err == nil {
		api.respondError(w, http.StatusConflict, "this email already has an account")
		return
	} else if err != gorm.ErrRecordNotFound {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Resolve the public base URL before creating the invite. When a real
	// mailer is configured this refuses to derive the link from an untrusted
	// Host header (see appBaseURL) — fail here rather than emailing a
	// Host-poisoned invite link.
	base, err := appBaseURL(r)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "server email is misconfigured: set APP_BASE_URL to send invites")
		return
	}

	token, err := authctx.NewToken()
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	invite, err := api.q.CreateTeamInvite(r.Context(), membership.TeamID,
		email, db.TeamRoleMember, authctx.HashToken(token), membership.UserID)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	team, _ := api.q.GetTeam(r.Context(), membership.TeamID)
	inviteURL := fmt.Sprintf("%s/register?invite=%s", base, token)
	body := fmt.Sprintf(
		"You've been invited to join the team %q on headcount1.\n\n"+
			"Create your account within 7 days:\n%s\n\n"+
			"If you weren't expecting this, you can ignore this email.",
		team.Name, inviteURL)
	go func(to string) {
		if err := apiMailer.Send(to, fmt.Sprintf("Join %s on headcount1", team.Name), body); err != nil {
			log.Printf("team: sending invite to %s failed: %v", to, err)
		}
	}(email)

	// The raw link is returned once so the owner can also share it directly
	// (e.g. when SMTP is not configured).
	api.respondJSON(w, http.StatusCreated, map[string]interface{}{
		"invite":     invite,
		"invite_url": inviteURL,
	})
}

// DeleteTeamInvite revokes a pending invite. Owner only.
func (api *API) DeleteTeamInvite(w http.ResponseWriter, r *http.Request) {
	membership, err := api.requireMembership(r)
	if err != nil || membership.Role != db.TeamRoleOwner {
		api.respondError(w, http.StatusForbidden, "only the team owner can revoke invites")
		return
	}
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := api.q.DeleteTeamInvite(r.Context(), membership.TeamID, int32(id)); err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// InviteInfo lets the register page show which team an invite token joins.
// Public route; the token itself is the credential.
func (api *API) InviteInfo(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		api.respondError(w, http.StatusBadRequest, "token is required")
		return
	}
	invite, err := api.q.GetTeamInviteByTokenHash(r.Context(), authctx.HashToken(token))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "invalid or expired invite")
		return
	}
	team, err := api.q.GetTeam(r.Context(), invite.TeamID)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]interface{}{
		"team_name": team.Name,
		"email":     invite.Email,
	})
}
