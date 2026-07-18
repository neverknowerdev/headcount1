package endpoints

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/authctx"
	"agent-orchestrator/pkg/secrets"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// Passkey (WebAuthn) authentication with a PRF-derived per-user encryption
// key. There are no passwords: a passkey both authenticates the user (the
// assertion) and, via its WebAuthn PRF output, unlocks the user's data key
// into the in-memory keyring (see pkg/secrets). The DEK is wrapped once per
// enrolled credential.

// prfSalt is the constant PRF evaluation input. It is not secret — the PRF
// output still differs per credential because each authenticator holds a
// distinct secret, so a shared salt yields distinct per-credential wraps.
var prfSalt = []byte("headcount1-prf-salt-v1-do-not-change-me!")

// keyringTTL is how long an unlocked DEK stays warm in memory. Bounded by the
// absolute session cap — past that ceiling the refresh family can no longer be
// rotated (refresh tokens carry no PRF, so the session can't be re-warmed), so
// keeping the DEK resident any longer would leave a "logged-out" user's secrets
// decryptable in RAM, breaking the zero-knowledge invariant.
func keyringTTL() time.Duration { return db.SessionAbsoluteCap() }

var (
	webauthnOnce sync.Once
	webauthnInst *webauthn.WebAuthn
	webauthnErr  error
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// webauthnRP builds the relying-party config from the environment. Defaults
// suit local/dev + E2E (localhost:8080); production sets WEBAUTHN_RP_ID and
// WEBAUTHN_RP_ORIGINS to the real domain.
func webauthnRP() (*webauthn.WebAuthn, error) {
	webauthnOnce.Do(func() {
		webauthnInst, webauthnErr = webauthn.New(&webauthn.Config{
			RPID:          envOr("WEBAUTHN_RP_ID", "localhost"),
			RPDisplayName: envOr("WEBAUTHN_RP_DISPLAY_NAME", "headcount1"),
			RPOrigins:     strings.Split(envOr("WEBAUTHN_RP_ORIGINS", "http://localhost:8080"), ","),
		})
	})
	return webauthnInst, webauthnErr
}

// webauthnUser adapts a db.User + its credentials to the go-webauthn User
// interface. WebAuthnID is the stable user id (never the email).
type webauthnUser struct {
	user  db.User
	creds []db.WebAuthnCredential
}

func (u webauthnUser) WebAuthnID() []byte          { return []byte(strconv.Itoa(int(u.user.ID))) }
func (u webauthnUser) WebAuthnName() string        { return u.user.Email }
func (u webauthnUser) WebAuthnDisplayName() string { return u.user.Email }
func (u webauthnUser) WebAuthnCredentials() []webauthn.Credential {
	out := make([]webauthn.Credential, 0, len(u.creds))
	for _, c := range u.creds {
		out = append(out, webauthn.Credential{
			ID:            c.CredentialID,
			PublicKey:     c.PublicKey,
			Authenticator: webauthn.Authenticator{SignCount: c.SignCount, AAGUID: c.AAGUID},
		})
	}
	return out
}

func prfExtension() protocol.AuthenticationExtensions {
	return protocol.AuthenticationExtensions{
		"prf": map[string]interface{}{
			"eval": map[string]interface{}{
				"first": base64.RawURLEncoding.EncodeToString(prfSalt),
			},
		},
	}
}

// finishPayload is the body of every *finish request: the WebAuthn credential
// JSON plus the PRF output the browser extracted from
// getClientExtensionResults().prf.results.first (base64url). Sending the PRF
// output explicitly decouples us from library-specific extension parsing.
type finishPayload struct {
	SessionID  int32           `json:"session_id"`
	Credential json.RawMessage `json:"credential"`
	PRF        string          `json:"prf"` // base64url of the PRF first output; empty if the device has no PRF
}

func (p finishPayload) prfBytes() ([]byte, error) {
	if p.PRF == "" {
		return nil, nil
	}
	// Accept both raw and padded base64url.
	if b, err := base64.RawURLEncoding.DecodeString(p.PRF); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(p.PRF)
}

func (u webauthnUser) loadCredsFor(ctx context.Context, q *db.Queries) webauthnUser {
	u.creds, _ = q.ListCredentialsForUser(ctx, u.user.ID)
	return u
}

// acceptTeamInviteFor joins the user to an invite's team, but ONLY when the
// invite's email matches the account's email. Invites are email-bound: the
// address is fixed when the invite is issued, so a leaked/forwarded link must
// not let someone join under an arbitrary email. Returns true iff the user
// joined a team via the invite.
func (api *API) acceptTeamInviteFor(ctx context.Context, inviteToken string, user db.User) bool {
	if inviteToken == "" {
		return false
	}
	inv, err := api.q.GetTeamInviteByTokenHash(ctx, authctx.HashToken(inviteToken))
	if err != nil || db.NormalizeEmail(inv.Email) != user.Email {
		return false
	}
	return api.q.AcceptTeamInvite(ctx, inv, user.ID) == nil
}

// ── registration (new account) ───────────────────────────────────────────────

// RegisterBegin creates the account (passwordless) and returns credential
// creation options requesting the PRF extension.
func (api *API) RegisterBegin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email       string `json:"email"`
		InviteToken string `json:"invite_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	email := db.NormalizeEmail(req.Email)
	if !looksLikeEmail(email) {
		api.respondError(w, http.StatusBadRequest, "a valid email is required")
		return
	}
	wa, err := webauthnRP()
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "webauthn not configured: "+err.Error())
		return
	}
	// If the account already exists, only allow (re-)enrollment when it has ZERO
	// credentials — a crypto-shredded account after recovery, or an abandoned
	// registration where the browser cancelled the passkey dialog. Any account
	// that still holds a passkey is a genuine duplicate → 409. Guarding on the
	// credential count keeps this from ever hijacking an active account.
	var user db.User
	if existing, err := api.q.GetUserByEmail(r.Context(), email); err == nil {
		creds, _ := api.q.ListCredentialsForUser(r.Context(), existing.ID)
		if len(creds) > 0 {
			api.respondError(w, http.StatusConflict, "an account with this email already exists")
			return
		}
		user = existing // re-enroll onto the existing (credential-less) account
	} else {
		// Validate an invite up front (before creating anything) so a bad token
		// doesn't leave an orphan account.
		if req.InviteToken != "" {
			if _, err := api.q.GetTeamInviteByTokenHash(r.Context(), authctx.HashToken(req.InviteToken)); err != nil {
				api.respondError(w, http.StatusBadRequest, "invalid or expired invite")
				return
			}
		}
		created, err := api.q.CreateUser(r.Context(), email)
		if err != nil {
			api.respondError(w, http.StatusInternalServerError, "failed to create account")
			return
		}
		user = created
	}
	wu := webauthnUser{user: user}
	options, sessionData, err := wa.BeginRegistration(wu, webauthn.WithExtensions(prfExtension()))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "failed to start registration: "+err.Error())
		return
	}
	uid := user.ID
	api.stashChallenge(w, r, &uid, "register", sessionData, map[string]any{
		"options":      options,
		"invite_token": req.InviteToken,
	})
}

// RegisterFinish verifies the attestation, wraps a fresh DEK under the
// credential's PRF output, seeds the account, unlocks the keyring, and starts
// a session.
func (api *API) RegisterFinish(w http.ResponseWriter, r *http.Request) {
	body, err := readFinish(r)
	if err != nil {
		api.respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	sess, err := api.q.ConsumeWebAuthnSession(r.Context(), body.SessionID, "register")
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "registration session expired — start over")
		return
	}
	prf, err := body.prfBytes()
	if err != nil || len(prf) < 16 {
		api.respondError(w, http.StatusBadRequest, "this device can't create an encryption passkey (no PRF support)")
		return
	}
	wa, _ := webauthnRP()
	stash, sessionData, inviteToken := decodeStash(sess.Data)
	if sess.UserID == nil {
		api.respondError(w, http.StatusBadRequest, "invalid registration session")
		return
	}
	user, err := api.q.GetUser(r.Context(), *sess.UserID)
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "account not found")
		return
	}
	parsed, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(body.Credential))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid attestation: "+err.Error())
		return
	}
	cred, err := wa.CreateCredential(webauthnUser{user: user}, *sessionData, parsed)
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "attestation failed: "+err.Error())
		return
	}
	_ = stash

	dek, err := secrets.NewUserDEK()
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "failed to create key")
		return
	}
	wrapped, err := secrets.WrapDEKForPRF(dek, prf)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "failed to wrap key")
		return
	}
	if _, err := api.q.CreateWebAuthnCredential(r.Context(), db.WebAuthnCredential{
		UserID: user.ID, CredentialID: cred.ID, PublicKey: cred.PublicKey,
		SignCount: cred.Authenticator.SignCount, AAGUID: cred.Authenticator.AAGUID,
		WrappedDEK: wrapped, PRFSalt: prfSalt, Nickname: "This device", LastUsedAt: time.Now(),
	}); err != nil {
		api.respondError(w, http.StatusInternalServerError, "failed to save passkey")
		return
	}

	// Join via the invite (email-bound) or fall back to a fresh personal team.
	if !api.acceptTeamInviteFor(r.Context(), inviteToken, user) {
		_ = api.q.EnsureTeamForUser(r.Context(), user)
	}
	secrets.UnlockUser(user.ID, dek, keyringTTL())
	api.seedNewUser(r.Context(), user.ID)

	api.issueTokenPair(w, r, user)
	api.respondJSON(w, http.StatusCreated, map[string]any{"user": userResponse{ID: user.ID, Email: user.Email}, "unlocked": true})
}

// ── login (existing account) ─────────────────────────────────────────────────

func (api *API) LoginBegin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	wa, err := webauthnRP()
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "webauthn not configured")
		return
	}
	user, err := api.q.GetUserByEmail(r.Context(), db.NormalizeEmail(req.Email))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "no account for that email")
		return
	}
	wu := webauthnUser{user: user}.loadCredsFor(r.Context(), api.q)
	if len(wu.creds) == 0 {
		api.respondError(w, http.StatusBadRequest, "this account has no passkey — use account recovery")
		return
	}
	options, sessionData, err := wa.BeginLogin(wu, webauthn.WithAssertionExtensions(prfExtension()))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "failed to start login: "+err.Error())
		return
	}
	uid := user.ID
	api.stashChallenge(w, r, &uid, "login", sessionData, map[string]any{"options": options})
}

func (api *API) LoginFinish(w http.ResponseWriter, r *http.Request) {
	api.finishAssertion(w, r, "login", true)
}

// UnlockFinish re-warms the keyring for an already-authenticated user after a
// crash (no new session issued). UnlockBegin reuses LoginBegin semantics but
// is mounted behind RequireAuth.
func (api *API) UnlockBegin(w http.ResponseWriter, r *http.Request) {
	user, ok := authctx.UserFrom(r.Context())
	if !ok {
		api.respondError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	wa, _ := webauthnRP()
	wu := webauthnUser{user: user}.loadCredsFor(r.Context(), api.q)
	options, sessionData, err := wa.BeginLogin(wu, webauthn.WithAssertionExtensions(prfExtension()))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "failed to start unlock")
		return
	}
	uid := user.ID
	api.stashChallenge(w, r, &uid, "unlock", sessionData, map[string]any{"options": options})
}

func (api *API) UnlockFinish(w http.ResponseWriter, r *http.Request) {
	api.finishAssertion(w, r, "unlock", false)
}

// finishAssertion validates a login/unlock assertion, updates the sign
// counter, unwraps the DEK with the PRF output, and warms the keyring. When
// issueSession is true a session cookie is also set (login); otherwise only
// the keyring is re-warmed (unlock).
func (api *API) finishAssertion(w http.ResponseWriter, r *http.Request, purpose string, issueSession bool) {
	body, err := readFinish(r)
	if err != nil {
		api.respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	sess, err := api.q.ConsumeWebAuthnSession(r.Context(), body.SessionID, purpose)
	if err != nil || sess.UserID == nil {
		api.respondError(w, http.StatusBadRequest, "session expired — start over")
		return
	}
	user, err := api.q.GetUser(r.Context(), *sess.UserID)
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "account not found")
		return
	}
	wa, _ := webauthnRP()
	_, sessionData, _ := decodeStash(sess.Data)
	wu := webauthnUser{user: user}.loadCredsFor(r.Context(), api.q)
	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(body.Credential))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid assertion: "+err.Error())
		return
	}
	validated, err := wa.ValidateLogin(wu, *sessionData, parsed)
	if err != nil {
		api.respondError(w, http.StatusUnauthorized, "assertion failed")
		return
	}
	// Find our DB credential row and update its sign counter.
	dbCred, err := api.q.GetCredentialByCredentialID(r.Context(), validated.ID)
	if err != nil {
		api.respondError(w, http.StatusUnauthorized, "unknown credential")
		return
	}
	_ = api.q.UpdateCredentialUsage(r.Context(), dbCred.ID, validated.Authenticator.SignCount)

	if issueSession {
		api.issueTokenPair(w, r, user)
	}

	// Unlock: unwrap this credential's DEK with the PRF output. Absent PRF
	// (older device / hybrid) → authenticated but not unlocked.
	prf, _ := body.prfBytes()
	unlocked := false
	if len(prf) >= 16 {
		if dek, err := secrets.UnwrapDEKWithPRF(dbCred.WrappedDEK, prf); err == nil {
			secrets.UnlockUser(user.ID, dek, keyringTTL())
			unlocked = true
		}
	}
	api.respondJSON(w, http.StatusOK, map[string]any{"user": userResponse{ID: user.ID, Email: user.Email}, "unlocked": unlocked})
}

// ── add another device (behind RequireAuth, must be unlocked) ─────────────────

func (api *API) AddCredentialBegin(w http.ResponseWriter, r *http.Request) {
	user, ok := authctx.UserFrom(r.Context())
	if !ok {
		api.respondError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if !secrets.IsUnlocked(user.ID) {
		api.respondError(w, http.StatusConflict, "unlock this device first to enroll another")
		return
	}
	wa, _ := webauthnRP()
	wu := webauthnUser{user: user}.loadCredsFor(r.Context(), api.q)
	options, sessionData, err := wa.BeginRegistration(wu, webauthn.WithExtensions(prfExtension()))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "failed to start enrollment")
		return
	}
	uid := user.ID
	api.stashChallenge(w, r, &uid, "add-credential", sessionData, map[string]any{"options": options})
}

func (api *API) AddCredentialFinish(w http.ResponseWriter, r *http.Request) {
	user, ok := authctx.UserFrom(r.Context())
	if !ok {
		api.respondError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	dek, ok := secrets.Default().Keyring().Get(user.ID)
	if !ok {
		api.respondError(w, http.StatusConflict, "unlock this device first")
		return
	}
	body, err := readFinish(r)
	if err != nil {
		api.respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	sess, err := api.q.ConsumeWebAuthnSession(r.Context(), body.SessionID, "add-credential")
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "session expired — start over")
		return
	}
	prf, err := body.prfBytes()
	if err != nil || len(prf) < 16 {
		api.respondError(w, http.StatusBadRequest, "this device can't create an encryption passkey (no PRF support)")
		return
	}
	wa, _ := webauthnRP()
	_, sessionData, _ := decodeStash(sess.Data)
	parsed, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(body.Credential))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid attestation")
		return
	}
	cred, err := wa.CreateCredential(webauthnUser{user: user}.loadCredsFor(r.Context(), api.q), *sessionData, parsed)
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "attestation failed")
		return
	}
	// Re-wrap the SAME DEK under the new credential's PRF output.
	wrapped, err := secrets.WrapDEKForPRF(dek, prf)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "failed to wrap key")
		return
	}
	if _, err := api.q.CreateWebAuthnCredential(r.Context(), db.WebAuthnCredential{
		UserID: user.ID, CredentialID: cred.ID, PublicKey: cred.PublicKey,
		SignCount: cred.Authenticator.SignCount, AAGUID: cred.Authenticator.AAGUID,
		WrappedDEK: wrapped, PRFSalt: prfSalt, Nickname: "New device", LastUsedAt: time.Now(),
	}); err != nil {
		api.respondError(w, http.StatusInternalServerError, "failed to save passkey")
		return
	}
	api.respondJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

// ── credential management ─────────────────────────────────────────────────────

func (api *API) ListCredentials(w http.ResponseWriter, r *http.Request) {
	user, ok := authctx.UserFrom(r.Context())
	if !ok {
		api.respondError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	creds, _ := api.q.ListCredentialsForUser(r.Context(), user.ID)
	api.respondJSON(w, http.StatusOK, creds)
}

// ── recovery (email → wipe secrets → re-enroll) ──────────────────────────────

// RecoverRequest emails a passkey-reset link. Always 200 (no enumeration).
func (api *API) RecoverRequest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	email := db.NormalizeEmail(req.Email)
	if user, err := api.q.GetUserByEmail(r.Context(), email); err == nil {
		if token, err := authctx.NewToken(); err == nil {
			if _, err := api.q.CreatePasswordResetToken(r.Context(), user.ID, authctx.HashToken(token)); err == nil {
				link := appBaseURL(r) + "/recover?token=" + token
				go apiMailer.Send(email, "Reset your passkey", "Use this link to reset your passkey and regain access to your account. Your stored secrets will be cleared and must be re-entered.\n\n"+link)
			}
		}
	}
	api.respondJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// RecoverConfirm verifies the emailed token, crypto-shreds the user's secrets
// (deletes credentials + nulls secret columns — the DEK is unrecoverable), and
// hands back the email so the client can immediately re-register a passkey.
// The account, teams, companies, and tasks are all preserved.
func (api *API) RecoverConfirm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" {
		api.respondError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	user, err := api.q.ConsumePasswordResetToken(r.Context(), authctx.HashToken(req.Token))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid or expired reset link")
		return
	}
	if err := api.q.CryptoShredUser(r.Context(), user.ID); err != nil {
		api.respondError(w, http.StatusInternalServerError, "failed to reset account")
		return
	}
	secrets.LockUser(user.ID)
	_ = api.q.DeleteSessionsForUser(r.Context(), user.ID)
	// Also revoke refresh tokens: recovery is a full reset, and a stolen refresh
	// token would otherwise survive it and mint fresh access sessions.
	_ = api.q.RevokeRefreshTokensForUser(r.Context(), user.ID)
	api.respondJSON(w, http.StatusOK, map[string]any{"email": user.Email})
}

// ── helpers ──────────────────────────────────────────────────────────────────

// seedNewUser seeds the per-user builtin providers and default model settings
// (the encryption-independent half of the old onUserCreated).
func (api *API) seedNewUser(ctx context.Context, userID int32) {
	_ = api.q.EnsureBuiltinLLMProvidersForUser(ctx, userID)
	_ = api.q.EnsureDefaultModelSettingsForUser(ctx, userID)
}

// stashChallenge stores the ceremony session data + options and returns the
// options (with a session_id the client echoes at finish) to the browser.
func (api *API) stashChallenge(w http.ResponseWriter, r *http.Request, userID *int32, purpose string, sessionData *webauthn.SessionData, extra map[string]any) {
	blob, _ := json.Marshal(map[string]any{"session": sessionData, "invite_token": extra["invite_token"]})
	sess, err := api.q.CreateWebAuthnSession(r.Context(), userID, purpose, string(blob))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "failed to start ceremony")
		return
	}
	resp := map[string]any{"session_id": sess.ID}
	if opts, ok := extra["options"]; ok {
		resp["publicKey"] = publicKeyOf(opts)
	}
	api.respondJSON(w, http.StatusOK, resp)
}

// publicKeyOf pulls the PublicKey member out of a CredentialCreation /
// CredentialAssertion so the client gets the shape navigator.credentials
// expects.
func publicKeyOf(opts any) any {
	switch o := opts.(type) {
	case *protocol.CredentialCreation:
		return o.Response
	case *protocol.CredentialAssertion:
		return o.Response
	}
	return opts
}

func decodeStash(data string) (map[string]any, *webauthn.SessionData, string) {
	var wrapper struct {
		Session     *webauthn.SessionData `json:"session"`
		InviteToken string                `json:"invite_token"`
	}
	_ = json.Unmarshal([]byte(data), &wrapper)
	return nil, wrapper.Session, wrapper.InviteToken
}

func readFinish(r *http.Request) (finishPayload, error) {
	var body finishPayload
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return body, errors.New("invalid finish payload")
	}
	if len(body.Credential) == 0 {
		return body, errors.New("missing credential")
	}
	return body, nil
}
