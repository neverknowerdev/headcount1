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
	}
}

// NopMailer logs instead of sending — the fallback when SMTP is unconfigured.
type NopMailer struct{}

func (NopMailer) Send(to, subject, textBody string) error {
	log.Printf("mailer: SMTP not configured — logging message instead\nTo: %s\nSubject: %s\n%s", to, subject, textBody)
	return nil
}

// SMTPMailer sends via an external SMTP relay.
type SMTPMailer struct {
	Host     string
	Port     string
	Username string
	Password string
	From     string
}

func (m *SMTPMailer) Send(to, subject, textBody string) error {
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

	// Port 465 is implicit TLS; everything else goes through net/smtp, which
	// upgrades with STARTTLS when the server offers it.
	if m.Port == "465" {
		return m.sendImplicitTLS(addr, auth, to, msg)
	}
	return smtp.SendMail(addr, auth, m.From, []string{to}, []byte(msg))
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
