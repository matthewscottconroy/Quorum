// Package model defines the core data types shared across the application.
package model

import "time"

// User represents an application login account.
// A User may optionally be linked to a Member record via MemberID.
// Roles, in ascending privilege: "restricted", "member", "officer", "admin",
// "superadmin". A "restricted" user sees only its own linked member record; a
// "superadmin" can perform destructive deletes. MemberID links the account to a
// Member (required for a "restricted" user to see their own data).
type User struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	Role        string     `json:"role"`
	MemberID    *string    `json:"member_id,omitempty"`
	TOTPEnabled bool       `json:"totp_enabled"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

// Member represents an organization member.
// DuesStatus is a computed field derived from the most recent invoice status.
// Tier values: "standard", "premium", "honorary" (or any custom string).
// Status values: "active", "inactive", "suspended".
type Member struct {
	ID          string         `json:"id"`
	DisplayName string         `json:"display_name"`
	Email       *string        `json:"email,omitempty"`
	Phone       *string        `json:"phone,omitempty"`
	Address     *string        `json:"address,omitempty"`
	Tier        string         `json:"tier"`
	Status      string         `json:"status"`
	JoinedAt    time.Time      `json:"joined_at"`
	Notes       *string        `json:"notes,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DuesStatus  string         `json:"dues_status,omitempty"`
}

// ValidInvoiceStatuses is the authoritative set of allowed dues invoice status values.
var ValidInvoiceStatuses = map[string]bool{
	"pending": true, "paid": true, "partial": true, "overdue": true, "waived": true,
}

// DuesInvoice is a dues bill issued to a Member for a given period.
// Status values: "pending", "paid", "partial", "overdue", "waived".
// Transactions is populated only when fetching a single invoice via GetInvoice.
// AmountMinor is the amount in the currency's minor units (e.g. cents); see
// money.go. Divide by 10^CurrencyExponent(Currency) for the major-unit value.
type DuesInvoice struct {
	ID           string        `json:"id"`
	MemberID     string        `json:"member_id"`
	AmountMinor  int64         `json:"amount_minor"`
	Currency     string        `json:"currency"`
	PeriodLabel  string        `json:"period_label"`
	DueDate      time.Time     `json:"due_date"`
	Status       string        `json:"status"`
	Notes        *string       `json:"notes,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
	MemberName   string        `json:"member_name,omitempty"`
	Transactions []Transaction `json:"transactions,omitempty"`
}

// Transaction records a single payment event against a DuesInvoice.
// Transactions may originate from Stripe/PayPal webhooks or be entered manually.
// AmountMinor is the amount in the currency's minor units (e.g. cents).
type Transaction struct {
	ID                  string    `json:"id"`
	InvoiceID           *string   `json:"invoice_id,omitempty"`
	MemberID            *string   `json:"member_id,omitempty"`
	AmountMinor         int64     `json:"amount_minor"`
	Currency            string    `json:"currency"`
	Provider            string    `json:"provider"`
	ProviderReferenceID *string   `json:"provider_reference_id,omitempty"`
	ProviderStatus      *string   `json:"provider_status,omitempty"`
	PaymentMethodType   *string   `json:"payment_method_type,omitempty"`
	RecordedBy          *string   `json:"recorded_by,omitempty"`
	OccurredAt          time.Time `json:"occurred_at"`
	Notes               *string   `json:"notes,omitempty"`
	MemberName          string    `json:"member_name,omitempty"`
}

// Meeting represents a scheduled or completed organizational meeting.
// Status values: "scheduled", "completed", "cancelled".
// Attendees and Decisions are populated only when fetching a single meeting.
type Meeting struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	ScheduledAt time.Time         `json:"scheduled_at"`
	Location    *string           `json:"location,omitempty"`
	Agenda      *string           `json:"agenda,omitempty"`
	Notes       *string           `json:"notes,omitempty"`
	Status      string            `json:"status"`
	CreatedBy   string            `json:"created_by"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	Attendees   []MeetingAttendee `json:"attendees,omitempty"`
	Decisions   []MeetingDecision `json:"decisions,omitempty"`
	ActionItems []ActionItem      `json:"action_items,omitempty"`
}

// MeetingAttendee records one member's attendance at a meeting.
type MeetingAttendee struct {
	MemberID   string `json:"member_id"`
	MemberName string `json:"member_name"`
	Present    bool   `json:"present"`
}

// MeetingDecision records a formal decision made during a meeting, including vote counts.
// Outcome values: "passed", "failed", "tabled", "noted".
type MeetingDecision struct {
	ID          string    `json:"id"`
	MeetingID   string    `json:"meeting_id"`
	Summary     string    `json:"summary"`
	Detail      *string   `json:"detail,omitempty"`
	VoteFor     *int      `json:"vote_for,omitempty"`
	VoteAgainst *int      `json:"vote_against,omitempty"`
	VoteAbstain *int      `json:"vote_abstain,omitempty"`
	Outcome     string    `json:"outcome"`
	RecordedAt  time.Time `json:"recorded_at"`
}

// ActionItem is a task assigned to a member, optionally linked to a meeting or plan.
// Status values: "open", "in_progress", "done", "cancelled".
// Priority values: "high", "normal", "low".
type ActionItem struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Description  *string    `json:"description,omitempty"`
	AssigneeID   *string    `json:"assignee_id,omitempty"`
	MeetingID    *string    `json:"meeting_id,omitempty"`
	PlanID       *string    `json:"plan_id,omitempty"`
	DueDate      *time.Time `json:"due_date,omitempty"`
	Status       string     `json:"status"`
	Priority     string     `json:"priority"`
	CreatedBy    string     `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	AssigneeName *string    `json:"assignee_name,omitempty"`
}

// Plan represents a strategic initiative tracked over time.
// Status values: "draft", "active", "completed", "archived".
// Decisions is populated only when fetching a single plan.
type Plan struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Description *string        `json:"description,omitempty"`
	Status      string         `json:"status"`
	OwnerID     *string        `json:"owner_id,omitempty"`
	TargetDate  *time.Time     `json:"target_date,omitempty"`
	CreatedBy   string         `json:"created_by"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	OwnerName   *string        `json:"owner_name,omitempty"`
	Decisions   []PlanDecision `json:"decisions,omitempty"`
	ActionItems []ActionItem   `json:"action_items,omitempty"`
}

// PlanDecision logs a key decision made in the context of a Plan.
type PlanDecision struct {
	ID        string    `json:"id"`
	PlanID    string    `json:"plan_id"`
	Summary   string    `json:"summary"`
	Rationale *string   `json:"rationale,omitempty"`
	DecidedBy *string   `json:"decided_by,omitempty"`
	DecidedAt time.Time `json:"decided_at"`
}

// Contact is an entry in the organizational contact directory.
type Contact struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Organization *string   `json:"organization,omitempty"`
	Email        *string   `json:"email,omitempty"`
	Phone        *string   `json:"phone,omitempty"`
	Address      *string   `json:"address,omitempty"`
	Category     *string   `json:"category,omitempty"`
	Tags         []string  `json:"tags"`
	Notes        *string   `json:"notes,omitempty"`
	CreatedBy    string    `json:"created_by"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Resource is a document, link, or reference stored in the resource library.
type Resource struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description *string   `json:"description,omitempty"`
	URL         *string   `json:"url,omitempty"`
	Category    *string   `json:"category,omitempty"`
	Tags        []string  `json:"tags"`
	AddedBy     string    `json:"added_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Page is a paginated response envelope.
type Page[T any] struct {
	Data   []T `json:"data"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// DashboardSummary aggregates key metrics for the home screen.
type DashboardSummary struct {
	OverdueDuesCount  int          `json:"overdue_dues_count"`
	PendingDuesCount  int          `json:"pending_dues_count"`
	UpcomingMeetings  []Meeting    `json:"upcoming_meetings"`
	OpenActionItems   []ActionItem `json:"open_action_items"`
	ActiveMemberCount int          `json:"active_member_count"`
}
