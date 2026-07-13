package service

import (
	"context"
	"fmt"
	"log"
	"time"

	"quorum/internal/model"
)

// invoiceAger is the subset of repo.DuesRepo that the scheduler needs.
type invoiceAger interface {
	MarkOverdue(ctx context.Context) (int64, error)
	OverdueInvoicesForEmail(ctx context.Context) ([]model.DuesInvoice, error)
}

type DuesService struct {
	repo  invoiceAger
	email *EmailService
}

func NewDuesService(r invoiceAger, e *EmailService) *DuesService {
	return &DuesService{repo: r, email: e}
}

// RunNightlyJob ages pending invoices to overdue and sends reminder emails.
func (s *DuesService) RunNightlyJob(ctx context.Context) {
	count, err := s.repo.MarkOverdue(ctx)
	if err != nil {
		log.Printf("dues aging error: %v", err)
	} else {
		log.Printf("dues aging: marked %d invoices overdue", count)
	}

	if s.email == nil || !s.email.configured() {
		return
	}

	overdueInvs, err := s.repo.OverdueInvoicesForEmail(ctx)
	if err != nil {
		log.Printf("overdue query error: %v", err)
		return
	}

	body := fmt.Sprintf("Quorum nightly summary — %s\n\n", time.Now().Format("2006-01-02"))
	body += fmt.Sprintf("Overdue invoices: %d\n\n", len(overdueInvs))
	for _, inv := range overdueInvs {
		body += fmt.Sprintf("  • %s — %s %s %.2f (due %s)\n",
			inv.MemberName, inv.PeriodLabel, inv.Currency, inv.Amount, inv.DueDate.Format("2006-01-02"))
	}

	if err := s.email.SendToAdmins(ctx, "Quorum: nightly dues summary", body); err != nil {
		log.Printf("admin digest email error: %v", err)
	}
}

// StartScheduler launches the nightly background job, aligned to 2 AM local time.
func (s *DuesService) StartScheduler(ctx context.Context) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("dues scheduler panic: %v", r)
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(nextScheduledRun()):
			}
			s.RunNightlyJob(ctx)
		}
	}()
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
