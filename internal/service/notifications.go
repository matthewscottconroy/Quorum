package service

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// notifyStore is the subset of *repo.NotifyRepo the delivery service needs.
type notifyStore interface {
	Create(ctx context.Context, userIDs []string, notifType, title string, body, link *string) error
	MemberRecipients(ctx context.Context) ([]repo.Recipient, error)
	RecipientForMember(ctx context.Context, memberID string) (*repo.Recipient, error)
	EmailOptedIn(ctx context.Context, userIDs []string, category string) ([]string, error)
}

const (
	notifEventWorkers = 4
	notifEventQueue   = 512
)

// audience selects who an event reaches.
type audience int

const (
	audienceMembers audience = iota // every user at member level or above
	audienceMember                  // the user account linked to one member
)

type notifyEvent struct {
	audience  audience
	memberID  string // for audienceMember
	notifType string
	title     string
	body      *string
	link      *string
}

// NotificationService fans domain events out to users as in-app notifications
// (always recorded) and email (gated per-user by category preference). Delivery
// runs on a bounded worker pool so emitting a notice never blocks the request
// path, and a burst can neither spawn unbounded goroutines nor fail the action
// that triggered it. Recipient resolution happens on the worker, not the caller.
type NotificationService struct {
	store  notifyStore
	email  emailSender
	jobs   chan notifyEvent
	wg     sync.WaitGroup
	mu     sync.Mutex
	closed bool
	onDrop func() // optional metric hook, fired when an event is dropped
}

// SetDropHook attaches a callback fired whenever an event is dropped (queue
// full or service closed), so drops can be counted and alerted on.
func (s *NotificationService) SetDropHook(fn func()) { s.onDrop = fn }

// NewNotificationService starts the delivery worker pool.
func NewNotificationService(store notifyStore, email emailSender) *NotificationService {
	s := &NotificationService{
		store: store,
		email: email,
		jobs:  make(chan notifyEvent, notifEventQueue),
	}
	for i := 0; i < notifEventWorkers; i++ {
		s.wg.Add(1)
		go s.worker()
	}
	return s
}

// Close stops accepting events and waits for in-flight deliveries to finish.
// Safe against late producers: enqueue() checks the closed flag under the same
// lock, so a straggler is dropped rather than panicking on a closed channel.
func (s *NotificationService) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.jobs)
	s.mu.Unlock()
	s.wg.Wait()
}

// NotifyMembers notifies every member-level (and above) user — for org-wide
// events like a motion opening or a meeting being scheduled. Non-blocking.
func (s *NotificationService) NotifyMembers(notifType, title string, body, link *string) {
	s.enqueue(notifyEvent{audience: audienceMembers, notifType: notifType, title: title, body: body, link: link})
}

// TryNotifyMembers is NotifyMembers reporting whether the event was accepted
// into the queue. Callers that record a once-only "sent" marker must check:
// marking after a silent drop loses the notice forever.
func (s *NotificationService) TryNotifyMembers(notifType, title string, body, link *string) bool {
	return s.enqueue(notifyEvent{audience: audienceMembers, notifType: notifType, title: title, body: body, link: link})
}

// NotifyMember notifies the user account linked to one member — for personal
// events like being assigned an action item. Non-blocking; a no-op if the member
// has no linked account.
func (s *NotificationService) NotifyMember(memberID, notifType, title string, body, link *string) {
	if memberID == "" {
		return
	}
	s.enqueue(notifyEvent{audience: audienceMember, memberID: memberID, notifType: notifType, title: title, body: body, link: link})
}

// TryNotifyMember is NotifyMember reporting queue acceptance (see TryNotifyMembers).
func (s *NotificationService) TryNotifyMember(memberID, notifType, title string, body, link *string) bool {
	if memberID == "" {
		return false
	}
	return s.enqueue(notifyEvent{audience: audienceMember, memberID: memberID, notifType: notifType, title: title, body: body, link: link})
}

func (s *NotificationService) enqueue(e notifyEvent) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		slog.Warn("notifications: service closed, dropping event", "type", e.notifType)
		if s.onDrop != nil {
			s.onDrop()
		}
		return false
	}
	select {
	case s.jobs <- e:
		return true
	default:
		slog.Warn("notifications: queue full, dropping event", "type", e.notifType)
		if s.onDrop != nil {
			s.onDrop()
		}
		return false
	}
}

func (s *NotificationService) worker() {
	defer s.wg.Done()
	for e := range s.jobs {
		s.deliver(e)
	}
}

func (s *NotificationService) deliver(e notifyEvent) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("notifications: delivery panic", "panic", rec, "type", e.notifType)
		}
	}()
	// The request that emitted this has already returned; use a fresh context.
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	recipients, err := s.resolve(ctx, e)
	if err != nil {
		slog.Error("notifications: resolve recipients", "err", err, "type", e.notifType)
		return
	}
	if len(recipients) == 0 {
		return
	}

	userIDs := make([]string, 0, len(recipients))
	for _, r := range recipients {
		userIDs = append(userIDs, r.UserID)
	}
	// In-app notices are recorded for everyone, regardless of email preference.
	if err := s.store.Create(ctx, userIDs, e.notifType, e.title, e.body, e.link); err != nil {
		slog.Error("notifications: record in-app", "err", err, "type", e.notifType)
	}

	// Email only those who haven't opted out of this category.
	if s.email == nil || !s.email.configured() {
		return
	}
	category := model.NotificationCategory(e.notifType)
	opted, err := s.store.EmailOptedIn(ctx, userIDs, category)
	if err != nil {
		slog.Error("notifications: email opt-in lookup", "err", err, "type", e.notifType)
		return
	}
	optedSet := make(map[string]bool, len(opted))
	for _, id := range opted {
		optedSet[id] = true
	}
	var emails []string
	for _, r := range recipients {
		if r.Email != "" && optedSet[r.UserID] {
			emails = append(emails, r.Email)
		}
	}
	if len(emails) == 0 {
		return
	}
	// Titles are free text; a newline in an email subject is a header-injection
	// vector and EmailService.Send would (rightly) refuse the whole message.
	subject := "Quorum: " + strings.NewReplacer("\r", " ", "\n", " ").Replace(e.title)
	body := e.title
	if e.body != nil && *e.body != "" {
		body += "\n\n" + *e.body
	}
	if e.link != nil && *e.link != "" {
		body += "\n\n" + s.email.baseURL() + "/" + *e.link
	}
	if err := s.email.Send(emails, subject, body); err != nil {
		slog.Error("notifications: send email", "err", err, "type", e.notifType)
	}
}

func (s *NotificationService) resolve(ctx context.Context, e notifyEvent) ([]repo.Recipient, error) {
	switch e.audience {
	case audienceMember:
		rec, err := s.store.RecipientForMember(ctx, e.memberID)
		if err != nil || rec == nil {
			return nil, err
		}
		return []repo.Recipient{*rec}, nil
	default:
		return s.store.MemberRecipients(ctx)
	}
}
