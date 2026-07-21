// Package mailer sends transactional email (password resets) over SMTP.
// stdlib-only: net/smtp covers STARTTLS (587) and crypto/tls covers implicit
// TLS (465). When SMTP is unconfigured, NopMailer logs the message instead —
// the reset URL then appears in the server log, which keeps self-hosted
// installs usable without an email provider.
package mailer

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/smtp"
	"os"
	"strings"
	"time"
)

type Mailer interface {
	Send(to, subject, textBody string) error
}

// FromEnv builds the mailer from SMTP_HOST/SMTP_PORT/SMTP_USERNAME/
// SMTP_PASSWORD/SMTP_FROM. Without SMTP_HOST it returns a NopMailer.
func FromEnv() Mailer {
	host := os.Getenv("SMTP_HOST")
	if host == "" {
		return NopMailer{}
	}
	port := os.Getenv("SMTP_PORT")
	if port == "" {
		port = "587"
	}
	from := os.Getenv("SMTP_FROM")
	if from == "" {
		from = os.Getenv("SMTP_USERNAME")
	}
	return &SMTPMailer{
		Host:     host,
		Port:     port,
		Username: os.Getenv("SMTP_USERNAME"),
		Password: os.Getenv("SMTP_PASSWORD"),
		From:     from,
		// Token-bearing mail must not travel in cleartext. Require a TLS-protected
		// session by default (implicit TLS on 465, verified STARTTLS elsewhere);
		// an active MITM can strip STARTTLS from EHLO otherwise. Operators with a
		// trusted local relay can opt out with SMTP_REQUIRE_TLS=0.
		RequireTLS: !isFalsey(os.Getenv("SMTP_REQUIRE_TLS")),
	}
}

func isFalsey(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return true
	}
	return false
}

// NopMailer logs instead of sending — the fallback when SMTP is unconfigured.
type NopMailer struct{}

func (NopMailer) Send(to, subject, textBody string) error {
	// Never log the message body: it carries a live recovery/invite token, and
	// RecoverRequest is unauthenticated, so a shared log sink would leak
	// account-takeover tokens. Log only recipient + subject. For LOCAL dev
	// without SMTP, set MAILER_DEV_LOG_LINKS=1 to also print the body.
	if isTruthy(os.Getenv("MAILER_DEV_LOG_LINKS")) {
		log.Printf("mailer: SMTP not configured (dev) — message:\nTo: %s\nSubject: %s\n%s", to, subject, textBody)
		return nil
	}
	log.Printf("mailer: SMTP not configured — email not sent (To: %s, Subject: %q). Set SMTP_* to deliver, or MAILER_DEV_LOG_LINKS=1 to log the token link in local dev.", to, subject)
	return nil
}

func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// SMTPMailer sends via an external SMTP relay.
type SMTPMailer struct {
	Host     string
	Port     string
	Username string
	Password string
	From     string
	// RequireTLS aborts rather than sending token mail over an unencrypted
	// session (see FromEnv).
	RequireTLS bool
}

// validateHeaderField rejects CR/LF (and NUL) in a value destined for an email
// header line. net/smtp validates only the envelope, not DATA headers, so an
// unescaped newline in to/subject/from would inject arbitrary headers/body
// (e.g. a team name containing "\r\nBcc: attacker@evil"). Fail closed.
func validateHeaderField(name, v string) error {
	if strings.ContainsAny(v, "\r\n\x00") {
		return fmt.Errorf("mailer: illegal control character in %s header", name)
	}
	return nil
}

func (m *SMTPMailer) Send(to, subject, textBody string) error {
	for _, f := range []struct{ name, v string }{{"To", to}, {"Subject", subject}, {"From", m.From}} {
		if err := validateHeaderField(f.name, f.v); err != nil {
			return err
		}
	}
	msg := strings.Join([]string{
		"From: " + m.From,
		"To: " + to,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		`Content-Type: text/plain; charset="utf-8"`,
		"Date: " + time.Now().Format(time.RFC1123Z),
		"",
		textBody,
	}, "\r\n")

	addr := net.JoinHostPort(m.Host, m.Port)
	var auth smtp.Auth
	if m.Username != "" {
		auth = smtp.PlainAuth("", m.Username, m.Password, m.Host)
	}

	// Port 465 is implicit TLS (cert-verified in sendImplicitTLS). Everything
	// else opens a plaintext connection and upgrades with STARTTLS; when
	// RequireTLS is set we abort if the server doesn't offer STARTTLS instead of
	// falling back to cleartext.
	if m.Port == "465" {
		return m.sendImplicitTLS(addr, auth, to, msg)
	}
	return m.sendSTARTTLS(addr, auth, to, msg)
}

// sendSTARTTLS opens a plaintext SMTP session, upgrades to TLS via STARTTLS
// (verifying the server cert), and only proceeds in cleartext when RequireTLS
// is off and the server advertises no STARTTLS.
func (m *SMTPMailer) sendSTARTTLS(addr string, auth smtp.Auth, to, msg string) error {
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: m.Host}); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	} else if m.RequireTLS {
		return fmt.Errorf("smtp: server at %s does not offer STARTTLS and SMTP_REQUIRE_TLS is set — refusing to send token mail in cleartext", addr)
	}
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(m.From); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func (m *SMTPMailer) sendImplicitTLS(addr string, auth smtp.Auth, to, msg string) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: m.Host})
	if err != nil {
		return fmt.Errorf("smtp tls dial: %w", err)
	}
	c, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(m.From); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
