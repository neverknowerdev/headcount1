// Package redact keeps secret values out of LLM message history and run logs.
//
// Two complementary detectors feed one Scrub entry point:
//
//   - A process-wide registry of known plaintext secrets. Every value the
//     server decrypts (provider API keys, MCP tokens, SSH keys) is registered
//     at its decrypt chokepoint, and Scrub replaces the exact value plus its
//     common transport encodings (base64, URL-escaped, JSON-escaped).
//   - Pattern scrubbers for secrets the server never decrypted but an agent
//     may encounter in a workspace (.env files, PEM keys, well-known token
//     shapes, Authorization headers, passwords embedded in URLs).
//
// Scrub is called on both sides of the history boundary: on tool outputs
// before they enter the conversation sent to the LLM, and on every log entry
// before it is written to disk, the database, or the WebSocket stream.
package redact

import (
	"encoding/base64"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

// minSecretLen guards against registering short values whose redaction would
// mangle ordinary text (e.g. a 4-char token would hit substrings everywhere).
const minSecretLen = 8

// Marker is the stable replacement format; label is a short kind hint.
func marker(label string) string { return "[REDACTED:" + label + "]" }

var (
	mu       sync.Mutex
	needles  = map[string]string{} // needle → replacement marker
	replacer atomicReplacer
)

type atomicReplacer struct {
	mu sync.RWMutex
	r  *strings.Replacer
}

func (a *atomicReplacer) get() *strings.Replacer {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.r
}

func (a *atomicReplacer) set(r *strings.Replacer) {
	a.mu.Lock()
	a.r = r
	a.mu.Unlock()
}

// Register adds a known secret plaintext to the redaction registry. The exact
// value and its common transport encodings (std/url base64 with and without
// padding, URL-percent-escaped, JSON-string-escaped) are all redacted by
// Scrub from then on. Values shorter than 8 characters are ignored — too
// likely to collide with ordinary text. Registration is idempotent and safe
// for concurrent use. label appears in the replacement marker, e.g.
// "[REDACTED:provider-api-key]"; empty defaults to "secret".
func Register(label, value string) {
	if len(value) < minSecretLen {
		return
	}
	if label == "" {
		label = "secret"
	}
	m := marker(label)

	forms := []string{value}
	// Transport encodings a secret commonly travels in. Only keep forms that
	// actually differ from the raw value and stay above the length floor.
	if b := base64.StdEncoding.EncodeToString([]byte(value)); b != value {
		forms = append(forms, b, strings.TrimRight(b, "="))
	}
	if b := base64.URLEncoding.EncodeToString([]byte(value)); b != value {
		forms = append(forms, b, strings.TrimRight(b, "="))
	}
	if u := url.QueryEscape(value); u != value {
		forms = append(forms, u)
	}
	if j := jsonEscape(value); j != value {
		forms = append(forms, j)
	}

	mu.Lock()
	changed := false
	for _, f := range forms {
		if len(f) < minSecretLen {
			continue
		}
		if needles[f] != m {
			needles[f] = m
			changed = true
		}
	}
	if changed {
		pairs := make([]string, 0, len(needles)*2)
		for n, rep := range needles {
			pairs = append(pairs, n, rep)
		}
		replacer.set(strings.NewReplacer(pairs...))
	}
	mu.Unlock()
}

// jsonEscape returns the value as it would appear inside a JSON string
// literal (without the surrounding quotes). Only differs from the raw value
// when it contains characters JSON escapes (quotes, backslashes, control
// chars) — e.g. a PEM key's newlines become \n in a logged JSON body.
func jsonEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// patternRule pairs a compiled detector with its replacement behavior. When
// group is 0 the whole match is replaced; otherwise only that capture group
// is, preserving the surrounding context (e.g. keep "Authorization: Bearer ",
// redact the token).
type patternRule struct {
	re    *regexp.Regexp
	group int
	label string
}

// Patterns are deliberately high-precision: each anchors on a well-known
// secret shape so ordinary code, UUIDs, and git SHAs pass through untouched.
var patternRules = []patternRule{
	// PEM private key blocks (RSA/EC/OPENSSH/PKCS8, incl. encrypted).
	{re: regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`), label: "private-key"},
	// OpenAI / Anthropic style keys.
	{re: regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`), label: "api-key"},
	// GitHub tokens: classic PATs, app/oauth/refresh tokens, fine-grained PATs.
	{re: regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,255}\b`), label: "github-token"},
	{re: regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9]{22}_[A-Za-z0-9]{59}\b`), label: "github-token"},
	// AWS access key id + the secret that usually rides next to it.
	{re: regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), label: "aws-key"},
	{re: regexp.MustCompile(`(?i)\baws_?secret_?access_?key\b['"]?\s*[:=]\s*['"]?([A-Za-z0-9/+=]{40})`), group: 1, label: "aws-secret"},
	// Slack tokens.
	{re: regexp.MustCompile(`\bxox[bpoasr]-[A-Za-z0-9-]{10,}\b`), label: "slack-token"},
	// Google API keys.
	{re: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`), label: "google-api-key"},
	// JWTs: three base64url segments where the header decodes to {"alg"... .
	// eyJ is base64url for {" — anchoring on it keeps precision high.
	{re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`), label: "jwt"},
	// Authorization headers: keep the scheme, redact the credential.
	{re: regexp.MustCompile(`(?i)\b(authorization["']?\s*[:=]\s*["']?(?:bearer|basic|token)\s+)([A-Za-z0-9._~+/=-]{8,})`), group: 2, label: "auth-header"},
	// Passwords embedded in connection URLs: scheme://user:PASSWORD@host.
	{re: regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.-]*://[^/\s:@]+:([^/\s@]{4,})@`), group: 1, label: "url-password"},
	// .env-style assignments of secret-named variables. Requires the value on
	// the same line; quotes optional. The name list mirrors the server's env
	// scrubber (engine/aicli/tools/sandbox.go isServerSecretEnv).
	{re: regexp.MustCompile(`(?im)^(\s*(?:export\s+)?\w*(?:TOKEN|SECRET|PASSWORD|PASSWD|PASSPHRASE|CREDENTIAL|APIKEY|API_KEY|PRIVATE_KEY|ACCESS_KEY)\w*\s*=\s*)(['"]?)([^\s'"#]{8,})`), group: 3, label: "env-secret"},
}

// Scrub returns s with every known registered secret (in any registered
// encoding) and every pattern-detected secret replaced by a [REDACTED:kind]
// marker. Safe for concurrent use; idempotent.
func Scrub(s string) string {
	if s == "" {
		return s
	}
	if r := replacer.get(); r != nil {
		s = r.Replace(s)
	}
	for _, p := range patternRules {
		if p.group == 0 {
			s = p.re.ReplaceAllString(s, marker(p.label))
			continue
		}
		m := marker(p.label)
		s = p.re.ReplaceAllStringFunc(s, func(match string) string {
			idx := p.re.FindStringSubmatchIndex(match)
			if idx == nil || len(idx) <= p.group*2+1 || idx[p.group*2] < 0 {
				return match
			}
			return match[:idx[p.group*2]] + m + match[idx[p.group*2+1]:]
		})
	}
	return s
}

// ScrubBytes is the []byte convenience wrapper around Scrub.
func ScrubBytes(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	return []byte(Scrub(string(b)))
}

// ScrubEntry redacts every string value (including nested maps and slices) of
// a structured log entry in place and returns it. Used by the run-log
// persistence path where entries are map[string]interface{}.
func ScrubEntry(entry map[string]interface{}) map[string]interface{} {
	for k, v := range entry {
		entry[k] = scrubValue(v)
	}
	return entry
}

func scrubValue(v interface{}) interface{} {
	switch x := v.(type) {
	case string:
		return Scrub(x)
	case map[string]interface{}:
		for k, e := range x {
			x[k] = scrubValue(e)
		}
		return x
	case []interface{}:
		for i, e := range x {
			x[i] = scrubValue(e)
		}
		return x
	default:
		return v
	}
}

// resetForTest clears the registry; test helper only.
func resetForTest() {
	mu.Lock()
	needles = map[string]string{}
	replacer.set(nil)
	mu.Unlock()
}
