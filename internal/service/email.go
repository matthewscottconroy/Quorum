package service

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"

	"quorum/internal/config"
	"quorum/internal/model"
)

type userLister interface {
	ListUsers(ctx context.Context) ([]model.User, error)
}

type EmailService struct {
	cfg      *config.Config
	authRepo userLister
}

func NewEmailService(cfg *config.Config, authRepo userLister) *EmailService {
	return &EmailService{cfg: cfg, authRepo: authRepo}
}

func (s *EmailService) configured() bool {
	return s.cfg.SMTPHost != "" && s.cfg.EmailFrom != ""
}

func (s *EmailService) Send(to []string, subject, body string) error {
	if !s.configured() {
		return nil
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

	return smtp.SendMail(addr, auth, s.cfg.EmailFrom, to, msg)
}

func (s *EmailService) SendToAdmins(ctx context.Context, subject, body string) error {
	users, err := s.authRepo.ListUsers(ctx)
	if err != nil {
		return err
	}
	var adminEmails []string
	for _, u := range users {
		if u.Role == "admin" {
			adminEmails = append(adminEmails, u.Email)
		}
	}
	if len(adminEmails) == 0 {
		return nil
	}
	return s.Send(adminEmails, subject, body)
}
