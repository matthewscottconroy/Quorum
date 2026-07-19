package service

import (
	"context"
	"fmt"
	"log"
	"time"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// invoiceAger is the subset of repo.DuesRepo that the scheduler needs.
type invoiceAger interface {
	MarkOverdue(ctx context.Context) (int64, error)
	OverdueInvoicesForEmail(ctx context.Context) ([]model.DuesInvoice, error)
	OverdueForReminders(ctx context.Context) ([]repo.OverdueReminder, error)
	AdvanceReminderStage(ctx context.Context, invoiceID string, stage int) error
}

// Reminder escalation stages, keyed by how many days an invoice is overdue.
// Stage 0 = none sent, 1 = first notice, 2 = 7-day follow-up, 3 = 30-day final.
const (
	reminderFirstDays  = 0
	reminderFollowDays = 7
	reminderFinalDays  = 30
)

// reminderStageForDaysOverdue returns the highest reminder stage an invoice
// that many days overdue should have received.
func reminderStageForDaysOverdue(days int) int {
	switch {
	case days >= reminderFinalDays:
		return 3
	case days >= reminderFollowDays:
		return 2
	case days >= reminderFirstDays:
		return 1
	default:
		return 0
	}
}

// janitor prunes bookkeeping tables that would otherwise grow without bound.
type janitor interface {
	PruneRefreshTokens(ctx context.Context) (int64, error)
	PruneProcessedEvents(ctx context.Context, retain time.Duration) (int64, error)
	PruneAuditLog(ctx context.Context, retain time.Duration) (int64, error)
}

// Retention windows for the nightly prune. Providers retry webhooks for a few
// days at most, so 90 days keeps dedup safe; audit entries are kept a year.
const (
	processedEventRetention = 90 * 24 * time.Hour
	auditLogRetention       = 365 * 24 * time.Hour
)

type DuesService struct {
	repo    invoiceAger
	email   *EmailService
	janitor janitor
}

// NewDuesService constructs the service. janitor may be nil, in which case the
// nightly prune step is skipped.
func NewDuesService(r invoiceAger, e *EmailService, j janitor) *DuesService {
	return &DuesService{repo: r, email: e, janitor: j}
}

// RunNightlyJob ages pending invoices to overdue, prunes bookkeeping tables,
// sends escalating member reminders, and emails the admin digest.
func (s *DuesService) RunNightlyJob(ctx context.Context) {
	count, err := s.repo.MarkOverdue(ctx)
	if err != nil {
		log.Printf("dues aging error: %v", err)
	} else {
		log.Printf("dues aging: marked %d invoices overdue", count)
	}

	s.pruneBookkeeping(ctx)

	if s.email == nil || !s.email.configured() {
		return
	}

	s.sendMemberReminders(ctx)
	s.sendAdminDigest(ctx)
}

// sendMemberReminders emails each member whose dues are overdue, escalating
// through first / 7-day / 30-day notices. It advances an invoice's stage only
// after a successful send, so a transient SMTP failure is retried next night
// rather than silently skipped.
func (s *DuesService) sendMemberReminders(ctx context.Context) {
	reminders, err := s.repo.OverdueForReminders(ctx)
	if err != nil {
		log.Printf("overdue-reminders query error: %v", err)
		return
	}
	today := time.Now()
	sent := 0
	for _, rem := range reminders {
		daysOverdue := int(today.Sub(rem.DueDate).Hours() / 24)
		target := reminderStageForDaysOverdue(daysOverdue)
		if target <= rem.ReminderStage {
			continue // already at or past the appropriate stage
		}
		subject, body := reminderMessage(rem, target, s.email.baseURL())
		if err := s.email.Send([]string{rem.MemberEmail}, subject, body); err != nil {
			log.Printf("member reminder email error (invoice %s): %v", rem.InvoiceID, err)
			continue
		}
		if err := s.repo.AdvanceReminderStage(ctx, rem.InvoiceID, target); err != nil {
			log.Printf("advance reminder stage error (invoice %s): %v", rem.InvoiceID, err)
		}
		sent++
	}
	if sent > 0 {
		log.Printf("sent %d member dues reminder(s)", sent)
	}
}

// reminderMessage builds the subject and body for a member dues reminder at the
// given escalation stage.
func reminderMessage(rem repo.OverdueReminder, stage int, baseURL string) (subject, body string) {
	switch stage {
	case 3:
		subject = "Final notice: your dues are significantly overdue"
	case 2:
		subject = "Reminder: your dues remain unpaid"
	default:
		subject = "Notice: your dues are overdue"
	}
	body = fmt.Sprintf(
		"Hello %s,\n\n"+
			"Our records show the following dues invoice is overdue:\n\n"+
			"  Period:   %s\n"+
			"  Amount:   %s %s\n"+
			"  Due date: %s\n\n"+
			"Please arrange payment at your earliest convenience. If you have already\n"+
			"paid, thank you — you can disregard this message.\n",
		rem.MemberName, rem.PeriodLabel, rem.Currency, model.FormatMoney(rem.AmountMinor, rem.Currency), rem.DueDate.Format("2006-01-02"))
	if baseURL != "" {
		body += fmt.Sprintf("\nAccount: %s\n", baseURL)
	}
	return subject, body
}

// sendAdminDigest emails admins/superadmins a summary of all overdue invoices.
func (s *DuesService) sendAdminDigest(ctx context.Context) {
	overdueInvs, err := s.repo.OverdueInvoicesForEmail(ctx)
	if err != nil {
		log.Printf("overdue query error: %v", err)
		return
	}
	body := fmt.Sprintf("Quorum nightly summary — %s\n\n", time.Now().Format("2006-01-02"))
	body += fmt.Sprintf("Overdue invoices: %d\n\n", len(overdueInvs))
	for _, inv := range overdueInvs {
		body += fmt.Sprintf("  • %s — %s %s %s (due %s)\n",
			inv.MemberName, inv.PeriodLabel, inv.Currency, model.FormatMoney(inv.AmountMinor, inv.Currency), inv.DueDate.Format("2006-01-02"))
	}
	if err := s.email.SendToAdmins(ctx, "Quorum: nightly dues summary", body); err != nil {
		log.Printf("admin digest email error: %v", err)
	}
}

// pruneBookkeeping deletes rows that are no longer useful from the append-only
// and token tables. Each prune is independent; a failure is logged and skipped.
func (s *DuesService) pruneBookkeeping(ctx context.Context) {
	if s.janitor == nil {
		return
	}
	if n, err := s.janitor.PruneRefreshTokens(ctx); err != nil {
		log.Printf("prune refresh_tokens error: %v", err)
	} else if n > 0 {
		log.Printf("pruned %d expired/revoked refresh tokens", n)
	}
	if n, err := s.janitor.PruneProcessedEvents(ctx, processedEventRetention); err != nil {
		log.Printf("prune processed_events error: %v", err)
	} else if n > 0 {
		log.Printf("pruned %d old processed events", n)
	}
	if n, err := s.janitor.PruneAuditLog(ctx, auditLogRetention); err != nil {
		log.Printf("prune audit_log error: %v", err)
	} else if n > 0 {
		log.Printf("pruned %d old audit entries", n)
	}
}

// StartScheduler launches the nightly background job, aligned to 2 AM local
// time. The returned channel closes when the goroutine has fully stopped, so
// shutdown can wait for an in-flight job before closing the database pool.
func (s *DuesService) StartScheduler(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(nextScheduledRun()):
			}
			s.runNightlyJobSafely(ctx)
		}
	}()
	return done
}

// runNightlyJobSafely recovers from panics per run, so one bad night cannot
// kill the scheduler for the remaining lifetime of the process.
func (s *DuesService) runNightlyJobSafely(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("dues scheduler panic: %v", r)
		}
	}()
	s.RunNightlyJob(ctx)
}

// nextScheduledRun returns the duration until the next 2 AM local time.
func nextScheduledRun() time.Duration {
	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day(), 2, 0, 0, 0, now.Location())
	if !now.Before(next) {
		next = next.Add(24 * time.Hour)
	}
	return time.Until(next)
}
