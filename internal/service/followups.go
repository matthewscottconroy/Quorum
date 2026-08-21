package service

// Follow-ups: the nightly automations that close loops with the calendar and
// the task board — meeting reminders about two days out (with an RSVP nudge)
// and action-item due notices. Both ride the dues nightly job's leader lock
// via AddNightlyTask, and both are idempotent through DB markers.

import (
	"context"
	"fmt"
	"log"
	"time"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// followupMeetings is the slice of *repo.MeetingsRepo this service needs.
type followupMeetings interface {
	DueForReminder(ctx context.Context, from, to time.Time) ([]repo.MeetingReminderRow, error)
	MarkMeetingReminded(ctx context.Context, id string) error
}

// followupItems is the slice of *repo.ActionItemsRepo this service needs.
type followupItems interface {
	DueForReminder(ctx context.Context, today time.Time) ([]repo.ItemReminderRow, error)
	MarkDueReminded(ctx context.Context, id string) error
}

// memberNotifier fans notifications out honoring email preferences (the same
// path every other notice uses). The Try variants report queue acceptance:
// these reminders record once-only "sent" markers, and marking after a
// silent queue drop would lose the notice forever.
type memberNotifier interface {
	TryNotifyMembers(kind, title string, body, link *string) bool
	TryNotifyMember(memberID, kind, title string, body, link *string) bool
}

// FollowupsService sends both nightly follow-up kinds. Both ride the standard
// notification service (in-app + pref-honoring email); it needs no direct
// email sender.
type FollowupsService struct {
	meetings followupMeetings
	items    followupItems
	notify   memberNotifier
	loc      func(ctx context.Context) *time.Location
}

// NewFollowupsService constructs the service; loc may be nil (server zone).
func NewFollowupsService(m followupMeetings, i followupItems, n memberNotifier, loc func(ctx context.Context) *time.Location) *FollowupsService {
	return &FollowupsService{meetings: m, items: i, notify: n, loc: loc}
}

func (s *FollowupsService) location(ctx context.Context) *time.Location {
	if s.loc != nil {
		if l := s.loc(ctx); l != nil {
			return l
		}
	}
	return time.Local
}

// MeetingReminders notifies members about meetings roughly two days out —
// the window is 24-72h so a nightly cadence can't skip past it — and nudges
// anyone who hasn't RSVP'd. Marked per meeting, sent once.
func (s *FollowupsService) MeetingReminders(ctx context.Context) {
	if s.meetings == nil || s.notify == nil {
		return
	}
	now := time.Now()
	due, err := s.meetings.DueForReminder(ctx, now.Add(24*time.Hour), now.Add(72*time.Hour))
	if err != nil {
		log.Printf("meeting reminders: %v", err)
		return
	}
	for _, m := range due {
		when := m.ScheduledAt.In(s.location(ctx)).Format("Mon 2 Jan 2006, 15:04 MST")
		body := "Reminder: " + m.Title + " is coming up on " + when + "."
		if m.Location != "" {
			body += " Location: " + m.Location + "."
		}
		body += " If you haven't RSVP'd yet, a quick yes/no helps the chair plan quorum."
		link := "#/meetings?open=" + m.ID
		if !s.notify.TryNotifyMembers("meeting.reminder", "Upcoming: "+m.Title, &body, &link) {
			// Queue refused (full/closed): don't mark — tomorrow retries.
			log.Printf("meeting reminders: queue refused %s; will retry", m.ID)
			continue
		}
		if err := s.meetings.MarkMeetingReminded(ctx, m.ID); err != nil {
			log.Printf("meeting reminders: mark %s: %v", m.ID, err)
		}
	}
	if len(due) > 0 {
		log.Printf("meeting reminders: sent for %d meeting(s)", len(due))
	}
}

// ActionItemReminders notifies each assignee whose active card just came due —
// once per card, through the standard notification path, so the member's
// "assignments" email preference is honored and an in-app notice is recorded
// (the old direct-email path did neither). "Today" is computed in the org's
// timezone: the DB session runs UTC, which put the due day off by one for
// orgs far from Greenwich.
func (s *FollowupsService) ActionItemReminders(ctx context.Context) {
	if s.items == nil || s.notify == nil {
		return
	}
	today := time.Now().In(s.location(ctx))
	due, err := s.items.DueForReminder(ctx, today)
	if err != nil {
		log.Printf("action-item reminders: %v", err)
		return
	}
	sent := 0
	for _, it := range due {
		body := fmt.Sprintf("Your action item %q is due (%s). You can update it on the Board.",
			it.Title, it.DueDate.Format("2006-01-02"))
		link := "#/board"
		if !s.notify.TryNotifyMember(it.AssigneeID, "action_item.due", "Due today: "+it.Title, &body, &link) {
			log.Printf("action-item reminder: queue refused %s; will retry", it.ID)
			continue // not marked: retried tomorrow
		}
		if err := s.items.MarkDueReminded(ctx, it.ID); err != nil {
			log.Printf("action-item reminder mark (%s): %v", it.ID, err)
		}
		sent++
	}
	if sent > 0 {
		log.Printf("action-item reminders: sent %d", sent)
	}
}

// receiptDues is the slice of *repo.DuesRepo the receipt sender needs.
type receiptDues interface {
	ReceiptInfoFor(ctx context.Context, invoiceID string) (repo.ReceiptInfo, bool, error)
}

// NewReceiptSender returns the fire-and-forget hook handlers call after a
// successful payment recording: "received $X for {period} — remaining $Y."
// The email system dunned but never thanked; this is the thank-you, and it
// answers "did you get my check?" before it gets asked.
func NewReceiptSender(dues receiptDues, email emailSender) func(invoiceID string, amountMinor int64) {
	return func(invoiceID string, amountMinor int64) {
		if email == nil || !email.configured() || amountMinor <= 0 {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			info, ok, err := dues.ReceiptInfoFor(ctx, invoiceID)
			if err != nil || !ok {
				return // no reachable member: nothing to thank
			}
			remaining := "Your balance for this period is settled — thank you!"
			if info.RemainingMinor > 0 {
				remaining = fmt.Sprintf("Remaining balance: %s %s.", info.Currency, model.FormatMoney(info.RemainingMinor, info.Currency))
			}
			body := fmt.Sprintf(
				"Hello %s,\n\nWe received your payment of %s %s for %s.\n%s\n\nThank you!\n",
				info.MemberName, info.Currency, model.FormatMoney(amountMinor, info.Currency), info.PeriodLabel, remaining)
			if err := email.Send([]string{info.MemberEmail}, "Payment received — "+info.PeriodLabel, body); err != nil {
				log.Printf("payment receipt (%s): %v", invoiceID, err)
			}
		}()
	}
}
