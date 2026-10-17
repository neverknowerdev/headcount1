package endpoints

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/secrets"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/stretchr/testify/require"
)

// softPasskey is a minimal in-process ES256 authenticator holding one resident
// credential, enough to drive a real get() assertion through the server.
type softPasskey struct {
	key    *ecdsa.PrivateKey
	credID []byte
	handle []byte // userHandle the authenticator stored at registration
	count  uint32
}

func newSoftPasskey(t *testing.T, userID int32) *softPasskey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &softPasskey{key: k, credID: id, handle: []byte(strconv.Itoa(int(userID)))}
}

func (p *softPasskey) coseKey(t *testing.T) []byte {
	t.Helper()
	b, err := webauthncbor.Marshal(webauthncose.EC2PublicKeyData{
		PublicKeyData: webauthncose.PublicKeyData{KeyType: int64(webauthncose.EllipticKey), Algorithm: int64(webauthncose.AlgES256)},
		Curve:         1, // P-256
		XCoord:        p.key.PublicKey.X.FillBytes(make([]byte, 32)),
		YCoord:        p.key.PublicKey.Y.FillBytes(make([]byte, 32)),
	})
	require.NoError(t, err)
	return b
}

// assert answers the challenge from a begin response with a UV assertion.
func (p *softPasskey) assert(t *testing.T, challenge string) map[string]any {
	t.Helper()
	b64 := base64.RawURLEncoding.EncodeToString
	clientData, _ := json.Marshal(map[string]any{
		"type": "webauthn.get", "challenge": challenge, "origin": "http://localhost:8080",
	})
	rpHash := sha256.Sum256([]byte("localhost"))
	p.count++
	authData := append(rpHash[:], 0x05) // UP | UV
	authData = binary.BigEndian.AppendUint32(authData, p.count)
	cdHash := sha256.Sum256(clientData)
	digest := sha256.Sum256(append(append([]byte{}, authData...), cdHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, p.key, digest[:])
	require.NoError(t, err)
	return map[string]any{
		"id": b64(p.credID), "rawId": b64(p.credID), "type": "public-key",
		"response": map[string]any{
			"authenticatorData": b64(authData), "clientDataJSON": b64(clientData),
			"signature": b64(sig), "userHandle": b64(p.handle),
		},
		"clientExtensionResults": map[string]any{},
	}
}

func postJSON(t *testing.T, h http.HandlerFunc, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b)))
	return w
}

// loginBegin starts a login and returns the session id, the challenge, and the
// raw publicKey options.
func loginBegin(t *testing.T, api *API) (int32, string, map[string]any) {
	t.Helper()
	w := postJSON(t, api.LoginBegin, map[string]any{})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		SessionID int32          `json:"session_id"`
		PublicKey map[string]any `json:"publicKey"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp.SessionID, resp.PublicKey["challenge"].(string), resp.PublicKey
}

// TestLoginIsUsernameless covers the email-less login: begin takes no email and
// names no credentials, and finish resolves the account from the passkey's
// userHandle, signs the user in, and unlocks the keyring with the PRF output.
func TestLoginIsUsernameless(t *testing.T) {
	database, api := setupAuthTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	user, err := q.CreateUser(ctx, "passkey@corp.io")
	require.NoError(t, err)
	pk := newSoftPasskey(t, user.ID)
	prf := bytes.Repeat([]byte{7}, 32)
	dek, err := secrets.NewUserDEK()
	require.NoError(t, err)
	wrapped, err := secrets.WrapDEKForPRF(dek, prf)
	require.NoError(t, err)
	_, err = q.CreateWebAuthnCredential(ctx, db.WebAuthnCredential{
		UserID: user.ID, CredentialID: pk.credID, PublicKey: pk.coseKey(t),
		WrappedDEK: wrapped, PRFSalt: prfSalt, Nickname: "test", LastUsedAt: time.Now(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { secrets.LockUser(user.ID) })

	sid, challenge, opts := loginBegin(t, api)
	require.Empty(t, opts["allowCredentials"], "a discoverable login must not list (and so leak) credentials")
	require.Equal(t, "required", opts["userVerification"])

	w := postJSON(t, api.LoginFinish, map[string]any{
		"session_id": sid, "credential": pk.assert(t, challenge),
		"prf": base64.RawURLEncoding.EncodeToString(prf),
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var out struct {
		User     userResponse `json:"user"`
		Unlocked bool         `json:"unlocked"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	require.Equal(t, user.ID, out.User.ID)
	require.Equal(t, "passkey@corp.io", out.User.Email)
	require.True(t, out.Unlocked)
	require.NotEmpty(t, w.Result().Cookies(), "login must start a session")
}

// TestLoginRejectsForeignUserHandle: a passkey can't sign in as another account
// by presenting that account's user handle — the credential must belong to the
// user the handle names.
func TestLoginRejectsForeignUserHandle(t *testing.T) {
	database, api := setupAuthTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	victim, err := q.CreateUser(ctx, "victim@corp.io")
	require.NoError(t, err)
	attacker, err := q.CreateUser(ctx, "attacker@corp.io")
	require.NoError(t, err)
	pk := newSoftPasskey(t, attacker.ID)
	_, err = q.CreateWebAuthnCredential(ctx, db.WebAuthnCredential{
		UserID: attacker.ID, CredentialID: pk.credID, PublicKey: pk.coseKey(t),
		PRFSalt: prfSalt, Nickname: "test", LastUsedAt: time.Now(),
	})
	require.NoError(t, err)

	pk.handle = []byte(strconv.Itoa(int(victim.ID)))
	sid, challenge, _ := loginBegin(t, api)
	w := postJSON(t, api.LoginFinish, map[string]any{"session_id": sid, "credential": pk.assert(t, challenge)})
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	require.Empty(t, w.Result().Cookies(), "a failed login must not start a session")
}
