package service

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"quorum/internal/repo"
)

// fakeNotifyStore records Create calls and serves canned recipients.
type fakeNotifyStore struct {
	mu           sync.Mutex
	created      [][]string // each Create's userIDs
	members      []repo.Recipient
	memberByID   map[string]repo.Recipient
	optedInByCat map[string][]string // category -> userIDs opted in
}

func (f *fakeNotifyStore) Create(_ context.Context, userIDs []string, _, _ string, _, _ *string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := append([]string(nil), userIDs...)
	f.created = append(f.created, ids)
	return nil
}
func (f *fakeNotifyStore) MemberRecipients(_ context.Context) ([]repo.Recipient, error) {
	return f.members, nil
}
func (f *fakeNotifyStore) RecipientForMember(_ context.Context, memberID string) (*repo.Recipient, error) {
	if r, ok := f.memberByID[memberID]; ok {
		return &r, nil
	}
	return nil, nil
}
func (f *fakeNotifyStore) EmailOptedIn(_ context.Context, userIDs []string, category string) ([]string, error) {
	set := map[string]bool{}
	for _, id := range f.optedInByCat[category] {
		set[id] = true
	}
	var out []string
	for _, id := range userIDs {
		if set[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func (f *fakeNotifyStore) createdUsers() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var all []string
	for _, c := range f.created {
		all = append(all, c...)
	}
	sort.Strings(all)
	return all
}

// captureEmail records recipients of Send.
type captureEmail struct {
	mu   sync.Mutex
	sent [][]string
	conf bool
}

func (c *captureEmail) Send(to []string, _, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, append([]string(nil), to...))
	return nil
}
func (c *captureEmail) SendToAdmins(_ context.Context, _, _ string) error { return nil }
func (c *captureEmail) configured() bool                                  { return c.conf }
func (c *captureEmail) baseURL() string                                   { return "http://localhost:8080" }

func (c *captureEmail) recipients() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var all []string
	for _, s := range c.sent {
		all = append(all, s...)
	}
	sort.Strings(all)
	return all
}

// waitFor polls until cond() or a timeout — the delivery is async on a worker.
func waitFor(cond func() bool) bool {
	for i := 0; i < 200; i++ {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

func TestNotificationService_InAppAllEmailOptedIn(t *testing.T) {
	store := &fakeNotifyStore{
		members: []repo.Recipient{
			{UserID: "u1", Email: "a@x.com"},
			{UserID: "u2", Email: "b@x.com"},
			{UserID: "u3", Email: ""}, // no email — in-app only
		},
		// u2 has opted OUT of governance email (not in the opted-in set).
		optedInByCat: map[string][]string{"governance": {"u1", "u3"}},
	}
	email := &captureEmail{conf: true}
	s := NewNotificationService(store, email)
	defer s.Close()

	s.NotifyMembers("motion.opened", "Motion X open", nil, nil)

	if !waitFor(func() bool { return len(store.createdUsers()) == 3 && len(email.recipients()) >= 1 }) {
		t.Fatalf("delivery did not complete: created=%v emailed=%v", store.createdUsers(), email.recipients())
	}
	// In-app recorded for ALL three members regardless of email preference.
	got := store.createdUsers()
	if len(got) != 3 {
		t.Errorf("in-app should reach all 3 members, got %v", got)
	}
	// Email only u1 (u2 opted out, u3 has no address).
	if em := email.recipients(); len(em) != 1 || em[0] != "a@x.com" {
		t.Errorf("email should reach only opted-in u1, got %v", em)
	}
}

func TestNotificationService_NoEmailWhenUnconfigured(t *testing.T) {
	store := &fakeNotifyStore{
		members:      []repo.Recipient{{UserID: "u1", Email: "a@x.com"}},
		optedInByCat: map[string][]string{"governance": {"u1"}},
	}
	email := &captureEmail{conf: false} // SMTP not configured
	s := NewNotificationService(store, email)
	defer s.Close()

	s.NotifyMembers("motion.opened", "X", nil, nil)
	if !waitFor(func() bool { return len(store.createdUsers()) == 1 }) {
		t.Fatal("in-app not recorded")
	}
	time.Sleep(30 * time.Millisecond)
	if len(email.recipients()) != 0 {
		t.Errorf("no email should be sent when unconfigured, got %v", email.recipients())
	}
}

func TestNotificationService_NotifyMemberResolvesAccount(t *testing.T) {
	store := &fakeNotifyStore{
		memberByID:   map[string]repo.Recipient{"m-9": {UserID: "u-9", Email: "nine@x.com"}},
		optedInByCat: map[string][]string{"assignments": {"u-9"}},
	}
	email := &captureEmail{conf: true}
	s := NewNotificationService(store, email)
	defer s.Close()

	s.NotifyMember("m-9", "action_item.assigned", "Assigned: task", nil, nil)
	if !waitFor(func() bool { return len(store.createdUsers()) == 1 && len(email.recipients()) == 1 }) {
		t.Fatalf("member notify did not complete: created=%v emailed=%v", store.createdUsers(), email.recipients())
	}
	if store.createdUsers()[0] != "u-9" || email.recipients()[0] != "nine@x.com" {
		t.Errorf("wrong recipient: created=%v emailed=%v", store.createdUsers(), email.recipients())
	}
}

func TestNotificationService_NotifyMemberNoAccountIsNoop(t *testing.T) {
	store := &fakeNotifyStore{memberByID: map[string]repo.Recipient{}} // member has no linked user
	email := &captureEmail{conf: true}
	s := NewNotificationService(store, email)
	defer s.Close()

	s.NotifyMember("m-unknown", "action_item.assigned", "x", nil, nil)
	time.Sleep(30 * time.Millisecond)
	if len(store.createdUsers()) != 0 || len(email.recipients()) != 0 {
		t.Errorf("unlinked member should produce no notifications")
	}
}
