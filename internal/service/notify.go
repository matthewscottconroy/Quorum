package service

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"quorum/internal/model"
)

// userDirectory is the subset of the auth repo the notifier needs.
type userDirectory interface {
	ListUsers(ctx context.Context) ([]model.User, error)
	GetUserByID(ctx context.Context, id string) (*model.User, error)
}

const (
	notifyWorkers   = 4   // bounds concurrent SMTP conversations
	notifyQueueSize = 256 // bounds memory; excess notices are dropped, not queued forever
)

type deletionJob struct {
	actorUserID, entityType, entityName string
	affectedEmails                      []string
}

// Notifier sends out-of-band notifications about governance-significant events
// via a bounded worker pool, so a burst of deletes can neither spawn unbounded
// goroutines nor block the request path.
type Notifier struct {
	email  *EmailService
	dir    userDirectory
	jobs   chan deletionJob
	wg     sync.WaitGroup
	mu     sync.Mutex
	closed bool
}

// NewNotifier constructs a Notifier with a bounded worker pool for deletion notices.
func NewNotifier(email *EmailService, dir userDirectory) *Notifier {
	n := &Notifier{
		email: email,
		dir:   dir,
		jobs:  make(chan deletionJob, notifyQueueSize),
	}
	for i := 0; i < notifyWorkers; i++ {
		n.wg.Add(1)
		go n.worker()
	}
	return n
}

func (n *Notifier) worker() {
	defer n.wg.Done()
	for j := range n.jobs {
		n.notifyDeletion(j.actorUserID, j.entityType, j.entityName, j.affectedEmails)
	}
}

// Close stops accepting notices and waits for in-flight ones to finish. Call
// during graceful shutdown, before closing the database pool.
func (n *Notifier) Close() {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return
	}
	n.closed = true
	close(n.jobs)
	n.mu.Unlock()
	n.wg.Wait()
}

// NotifyDeletion informs the governance body (all admins and superadmins) and
// any directly-affected members that a record was permanently deleted. It is
// non-blocking: the notice is enqueued for a worker, so the SMTP round-trip
// never blocks the HTTP response and a notification failure never fails the
// delete. If the queue is saturated the notice is dropped (and logged) rather
// than growing memory without bound.
func (n *Notifier) NotifyDeletion(_ context.Context, actorUserID, entityType, entityName string, affectedEmails []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		log.Printf("notify: closed, dropping deletion notice for %s %q", entityType, entityName)
		return
	}
	select {
	case n.jobs <- deletionJob{actorUserID, entityType, entityName, affectedEmails}:
	default:
		log.Printf("notify: queue full, dropping deletion notice for %s %q", entityType, entityName)
	}
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
