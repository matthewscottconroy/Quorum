package service

import (
	"context"
	"fmt"
	"log"
	"time"

	"quorum/internal/model"
)

// userDirectory is the subset of the auth repo the notifier needs.
type userDirectory interface {
	ListUsers(ctx context.Context) ([]model.User, error)
	GetUserByID(ctx context.Context, id string) (*model.User, error)
}

// Notifier sends out-of-band notifications about governance-significant events.
type Notifier struct {
	email *EmailService
	dir   userDirectory
}

func NewNotifier(email *EmailService, dir userDirectory) *Notifier {
	return &Notifier{email: email, dir: dir}
}

// NotifyDeletion informs the governance body (all admins and superadmins) and
// any directly-affected members that a record was permanently deleted. It is
// fire-and-forget: the SMTP round-trip must not block the HTTP response, and a
// notification failure never fails the delete itself.
func (n *Notifier) NotifyDeletion(_ context.Context, actorUserID, entityType, entityName string, affectedEmails []string) {
	go n.notifyDeletion(actorUserID, entityType, entityName, affectedEmails)
}

func (n *Notifier) notifyDeletion(actorUserID, entityType, entityName string, affectedEmails []string) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("notify: deletion notice panic: %v", rec)
		}
	}()
	if n.email == nil || !n.email.configured() {
		return
	}
	// Own background context: the request that triggered this has already returned.
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	actor := actorUserID
	if u, err := n.dir.GetUserByID(ctx, actorUserID); err == nil && u.Email != "" {
		actor = u.Email
	}

	recipients := map[string]struct{}{}
	users, err := n.dir.ListUsers(ctx)
	if err != nil {
		log.Printf("notify: list users: %v", err)
	}
	for _, u := range users {
		if (u.Role == "admin" || u.Role == "superadmin") && u.Email != "" {
			recipients[u.Email] = struct{}{}
		}
	}
	for _, e := range affectedEmails {
		if e != "" {
			recipients[e] = struct{}{}
		}
	}
	if len(recipients) == 0 {
		return
	}
	to := make([]string, 0, len(recipients))
	for e := range recipients {
		to = append(to, e)
	}

	subject := fmt.Sprintf("Quorum: %s deleted", entityType)
	body := fmt.Sprintf(
		"A %s was permanently deleted from Quorum.\n\n"+
			"  Record:     %s\n"+
			"  Deleted by: %s\n\n"+
			"This action cannot be undone. If it was unexpected, contact your Quorum administrator.\n",
		entityType, entityName, actor)
	if err := n.email.Send(to, subject, body); err != nil {
		log.Printf("notify: send deletion notice: %v", err)
	}
}
