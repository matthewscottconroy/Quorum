// Package service holds background jobs (nightly dues aging, reminders, and
// pruning) and the email/notification senders.
package service

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"quorum/internal/config"
	"quorum/internal/model"
)

// smtpTimeout bounds the entire SMTP conversation so a hung relay cannot
// pile up scheduler goroutines indefinitely.
const smtpTimeout = 30 * time.Second

type userLister interface {
	ListUsers(ctx context.Context) ([]model.User, error)
}

// emailSender is the subset of *EmailService that background jobs depend on,
// extracted so the reminder/digest logic can be unit-tested with a fake.
type emailSender interface {
	Send(to []string, subject, body string) error
	SendToAdmins(ctx context.Context, subject, body string) error
	configured() bool
	baseURL() string
}

// EmailService sends plain-text email over SMTP (STARTTLS when the relay offers it).
type EmailService struct {
	cfg      *config.Config
	authRepo userLister
}

// NewEmailService constructs an EmailService from config and a user lister.
func NewEmailService(cfg *config.Config, authRepo userLister) *EmailService {
	return &EmailService{cfg: cfg, authRepo: authRepo}
}

func (s *EmailService) configured() bool {
	return s.cfg.SMTPHost != "" && s.cfg.EmailFrom != ""
}

func (s *EmailService) baseURL() string {
	return s.cfg.BaseURL
}

func containsCRLF(s string) bool {
	return strings.ContainsAny(s, "\r\n")
}

// Send emails a plain-text message. It no-ops when SMTP is unconfigured and rejects
// CR/LF in the subject or recipients (header-injection guard).
func (s *EmailService) Send(to []string, subject, body string) error {
	if !s.configured() {
		return nil
	}

	// Guard against header injection: recipients and subject become header
	// values, so a CR/LF in them could forge additional headers. Bodies are
	// safe (they follow the header/body separator).
	if containsCRLF(subject) {
		return fmt.Errorf("email subject contains a newline")
	}
	for _, addr := range to {
		if containsCRLF(addr) {
			return fmt.Errorf("email recipient contains a newline")
		}
	}

	addr := fmt.Sprintf("%s:%d", s.cfg.SMTPHost, s.cfg.SMTPPort)
	msg := []byte(
		"From: " + s.cfg.EmailFrom + "\r\n" +
			"To: " + strings.Join(to, ", ") + "\r\n" +
			"Subject: " + subject + "\r\n" +
			"Content-Type: text/plain; charset=UTF-8\r\n" +
			"\r\n" +
			body + "\r\n",
	)

	var auth smtp.Auth
	if s.cfg.SMTPUser != "" {
		auth = smtp.PlainAuth("", s.cfg.SMTPUser, s.cfg.SMTPPass, s.cfg.SMTPHost)
	}

	return sendMailWithTimeout(addr, s.cfg.SMTPHost, auth, s.cfg.EmailFrom, to, msg, s.cfg.SMTPRequireTLS)
}

// sendMailWithTimeout mirrors smtp.SendMail (STARTTLS when offered, then
// optional auth) but bounds the whole exchange with connect and I/O deadlines,
// which net/smtp itself does not support. When requireTLS is set and the relay
// does not advertise STARTTLS, it aborts rather than sending in cleartext.
func sendMailWithTimeout(addr, host string, auth smtp.Auth, from string, to []string, msg []byte, requireTLS bool) error {
	conn, err := net.DialTimeout("tcp", addr, smtpTimeout)
	if err != nil {
		return err
	}
	if err := conn.SetDeadline(time.Now().Add(smtpTimeout)); err != nil {
		conn.Close()
		return err
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return err
		}
	} else if requireTLS {
		return fmt.Errorf("smtp: server %q does not offer STARTTLS but QUORUM_SMTP_REQUIRE_TLS is set", host)
	}
	if auth != nil {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(auth); err != nil {
				return err
			}
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	wc, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := wc.Write(msg); err != nil {
		wc.Close()
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// SendToAdmins emails every admin and superadmin who has an address on file.
func (s *EmailService) SendToAdmins(ctx context.Context, subject, body string) error {
	users, err := s.authRepo.ListUsers(ctx)
	if err != nil {
		return err
	}
	var adminEmails []string
	for _, u := range users {
		if (u.Role == "admin" || u.Role == "superadmin") && u.Email != "" {
			adminEmails = append(adminEmails, u.Email)
		}
	}
	if len(adminEmails) == 0 {
		return nil
	}
	return s.Send(adminEmails, subject, body)
}
