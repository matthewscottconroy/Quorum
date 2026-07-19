package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"quorum/internal/config"
	"quorum/internal/model"
	"quorum/internal/repo"
)

type mockJanitor struct {
	refreshCalls int
	eventCalls   int
	auditCalls   int
	eventRetain  time.Duration
	auditRetain  time.Duration
	refreshErr   error
}

func (m *mockJanitor) PruneRefreshTokens(_ context.Context) (int64, error) {
	m.refreshCalls++
	return 3, m.refreshErr
}
func (m *mockJanitor) PruneProcessedEvents(_ context.Context, retain time.Duration) (int64, error) {
	m.eventCalls++
	m.eventRetain = retain
	return 2, nil
}
func (m *mockJanitor) PruneAuditLog(_ context.Context, retain time.Duration) (int64, error) {
	m.auditCalls++
	m.auditRetain = retain
	return 1, nil
}

func TestPruneBookkeeping_CallsAllWithRetention(t *testing.T) {
	j := &mockJanitor{}
	svc := NewDuesService(nil, nil, j)
	svc.pruneBookkeeping(context.Background())

	if j.refreshCalls != 1 || j.eventCalls != 1 || j.auditCalls != 1 {
		t.Fatalf("each prune should run once, got refresh=%d event=%d audit=%d",
			j.refreshCalls, j.eventCalls, j.auditCalls)
	}
	if j.eventRetain != processedEventRetention {
		t.Errorf("processed-events retention: got %v, want %v", j.eventRetain, processedEventRetention)
	}
	if j.auditRetain != auditLogRetention {
		t.Errorf("audit-log retention: got %v, want %v", j.auditRetain, auditLogRetention)
	}
}

func TestPruneBookkeeping_ContinuesAfterError(t *testing.T) {
	// A failure in one prune must not skip the others.
	j := &mockJanitor{refreshErr: errors.New("db down")}
	svc := NewDuesService(nil, nil, j)
	svc.pruneBookkeeping(context.Background())

	if j.eventCalls != 1 || j.auditCalls != 1 {
		t.Errorf("later prunes should still run after an earlier error: event=%d audit=%d",
			j.eventCalls, j.auditCalls)
	}
}

func TestPruneBookkeeping_NilJanitorNoPanic(t *testing.T) {
	svc := NewDuesService(nil, nil, nil)
	svc.pruneBookkeeping(context.Background()) // must not panic
}

// ---- reminderStageForDaysOverdue ----

func TestReminderStageForDaysOverdue(t *testing.T) {
	cases := []struct {
		days int
		want int
	}{
		{-1, 0},  // not yet due
		{0, 1},   // due today → first notice
		{6, 1},   // still first-notice window
		{7, 2},   // 7-day follow-up
		{29, 2},  // still follow-up window
		{30, 3},  // 30-day final notice
		{100, 3}, // clamped at final
	}
	for _, tc := range cases {
		if got := reminderStageForDaysOverdue(tc.days); got != tc.want {
			t.Errorf("reminderStageForDaysOverdue(%d) = %d, want %d", tc.days, got, tc.want)
		}
	}
}

// ---- reminderMessage ----

func TestReminderMessage_SubjectVariesByStage(t *testing.T) {
	rem := repo.OverdueReminder{
		MemberName:  "Alice",
		PeriodLabel: "2026-Q1",
		AmountMinor: 4250,
		Currency:    "USD",
		DueDate:     time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
	}
	s1, _ := reminderMessage(rem, 1, "")
	s2, _ := reminderMessage(rem, 2, "")
	s3, _ := reminderMessage(rem, 3, "")
	if s1 == s2 || s2 == s3 || s1 == s3 {
		t.Errorf("subjects must differ by stage: s1=%q s2=%q s3=%q", s1, s2, s3)
	}
	if !strings.Contains(strings.ToLower(s3), "final") {
		t.Errorf("stage-3 subject should read as a final notice, got %q", s3)
	}
}

func TestReminderMessage_BodyContainsDetails(t *testing.T) {
	rem := repo.OverdueReminder{
		MemberName:  "Bob",
		PeriodLabel: "2026-Q2",
		AmountMinor: 9999,
		Currency:    "USD",
		DueDate:     time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
	}
	_, body := reminderMessage(rem, 2, "https://quorum.example")
	for _, want := range []string{"Bob", "2026-Q2", "99.99", "USD", "2026-04-01", "https://quorum.example"} {
		if !strings.Contains(body, want) {
			t.Errorf("reminder body missing %q; body:\n%s", want, body)
		}
	}
}

// ---- mockInvoiceAger ----
// A full invoiceAger implementation so the service can be constructed for tests
// that exercise the escalation flow. Each field defaults to a benign no-op.

type mockInvoiceAger struct {
	MarkOverdueFn             func(ctx context.Context) (int64, error)
	OverdueInvoicesForEmailFn func(ctx context.Context) ([]model.DuesInvoice, error)
	OverdueForRemindersFn     func(ctx context.Context) ([]repo.OverdueReminder, error)
	AdvanceReminderStageFn    func(ctx context.Context, invoiceID string, stage int) error
}

func (m *mockInvoiceAger) MarkOverdue(ctx context.Context) (int64, error) {
	if m.MarkOverdueFn != nil {
		return m.MarkOverdueFn(ctx)
	}
	return 0, nil
}
func (m *mockInvoiceAger) OverdueInvoicesForEmail(ctx context.Context) ([]model.DuesInvoice, error) {
	if m.OverdueInvoicesForEmailFn != nil {
		return m.OverdueInvoicesForEmailFn(ctx)
	}
	return nil, nil
}
func (m *mockInvoiceAger) OverdueForReminders(ctx context.Context) ([]repo.OverdueReminder, error) {
	if m.OverdueForRemindersFn != nil {
		return m.OverdueForRemindersFn(ctx)
	}
	return nil, nil
}
func (m *mockInvoiceAger) AdvanceReminderStage(ctx context.Context, invoiceID string, stage int) error {
	if m.AdvanceReminderStageFn != nil {
		return m.AdvanceReminderStageFn(ctx, invoiceID, stage)
	}
	return nil
}

// TestSendMemberReminders_UnconfiguredEmailSkips verifies the nightly job's
// early-return: with an unconfigured EmailService, RunNightlyJob must not query
// reminders (no SMTP is attempted), so OverdueForReminders is never called.
func TestSendMemberReminders_UnconfiguredEmailSkips(t *testing.T) {
	queried := false
	ager := &mockInvoiceAger{
		OverdueForRemindersFn: func(_ context.Context) ([]repo.OverdueReminder, error) {
			queried = true
			return nil, nil
		},
	}
	email := NewEmailService(&config.Config{}, nil) // SMTPHost="" => not configured
	svc := NewDuesService(ager, email, nil)
	svc.RunNightlyJob(context.Background())
	if queried {
		t.Error("reminders must not be queried when email is unconfigured")
	}
}
