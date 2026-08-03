# Deploying on a Domain — Passkey (WebAuthn) Setup Guide

Authentication is **passwordless**: every account is a WebAuthn passkey, and the
same passkey also unlocks the user's encrypted secrets. WebAuthn is bound to the
site's origin, so a passkey created on one origin will not work on another. The
built-in defaults target **local development** (`localhost`); a real deployment
must point the relying-party (RP) config at its own domain, or account creation
fails with `attestation failed: Error validating origin`.

This document is about that host/domain setup. For the code, read
`server/controllers/auth_webauthn.go` (RP config, ceremonies) and
`server/controllers/auth.go` (`requestIsTLS`, cookies).

---

## TL;DR — the operator checklist

| # | Task | Why | Who does it |
| - | ---- | --- | ----------- |
| 1 | Serve the site over **HTTPS** (real cert) | WebAuthn refuses to run outside a secure context — `localhost` is the only HTTP exception | **you (TLS/proxy)** |
| 2 | Set **`WEBAUTHN_RP_ID`** to the domain (host only) | identifies the relying party; passkeys are scoped to it | **you (config)** |
| 3 | Set **`WEBAUTHN_RP_ORIGINS`** to the full origin(s) | validated exactly against the browser's origin | **you (config)** |
| 4 | Set **`APP_BASE_URL`** to the public URL | recovery / invite email links (Host header is refused when a real mailer is set) | **you (config)** |
| 5 | Forward **`X-Forwarded-Proto: https`** (and `Host`) from the proxy | so the server detects TLS and sets `Secure` cookies | **you (proxy)** |
| 6 | Build the frontend into the binary (`make build`) and run it | production serves the bundled UI on one origin — no Vite | **you (build)** |

---

## Environment variables

```bash
# Domain only — no scheme, no port. Passkeys are scoped to this value.
WEBAUTHN_RP_ID="headcount1.example.com"

# Full origin(s), comma-separated. Must EXACTLY match the browser's origin
# (scheme + host + port). List every hostname you serve on (e.g. apex + www).
WEBAUTHN_RP_ORIGINS="https://headcount1.example.com"

# Public base URL used to build password-reset / team-invite links in email.
APP_BASE_URL="https://headcount1.example.com"

# Optional: the label shown in the browser's passkey prompt.
WEBAUTHN_RP_DISPLAY_NAME="headcount1"
```

Defaults (dev only) are `RPID=localhost`,
`RPOrigins=http://localhost:8080,http://localhost:5174` — the Go server (8080)
and the Vite dev server (5174, `make run-dev`). These do **not** work on a real
domain; you must override all three of the above.

## Hard requirements

1. **HTTPS is mandatory.** WebAuthn only runs in a secure context. On a real
   domain over plain HTTP the browser will not create or use a passkey at all.

2. **`WEBAUTHN_RP_ORIGINS` must match the origin exactly** — scheme, host, and
   port. `https://headcount1.example.com` is a different origin from
   `https://www.headcount1.example.com`. If you serve both, list both.

3. **`WEBAUTHN_RP_ID` must be the origin host or a registrable parent of it.**
   For origin `https://app.example.com`:
   - `app.example.com` → passkey scoped to that subdomain
   - `example.com` → passkey works across all `*.example.com` subdomains
   - `example.org` → **invalid**; attestation fails.

4. **Behind a reverse proxy, forward the protocol.** The server decides "is this
   TLS?" from `r.TLS` or the `X-Forwarded-Proto` header, and only then marks
   session cookies `Secure`. Without it, logins over HTTPS get non-`Secure`
   cookies and break.

## Reverse proxy example (nginx)

```nginx
server {
    listen 443 ssl;
    server_name headcount1.example.com;

    ssl_certificate     /etc/letsencrypt/live/headcount1.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/headcount1.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host              $host;
        proxy_set_header X-Forwarded-Proto $scheme;   # required — see #4 above
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
    }
}
```

## Caveats

- **Passkeys are bound to the RPID.** Passkeys enrolled on `localhost` do not
  work on your domain (different RPID) — users simply enroll a fresh passkey on
  the real domain.
- **Don't change the domain after launch.** Changing `WEBAUTHN_RP_ID` (or the
  domain) invalidates every existing passkey; all users must re-enroll via the
  recovery-email flow. Pick the final domain (and decide apex vs subdomain RPID)
  before onboarding real users.
- **Recovery email needs SMTP + `APP_BASE_URL`.** With a real mailer configured,
  the server refuses to build links from the request `Host` header (anti-spoof),
  so `APP_BASE_URL` must be set or recovery/invite emails will error.
