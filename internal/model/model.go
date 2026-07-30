// Package model defines the core data types shared across the application.
package model

import (
	"strings"
	"time"
)

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

// CategoryValue is a labeled magnitude for bar/donut charts.
type CategoryValue struct {
	Label string `json:"label"`
	Value int64  `json:"value"`
}

// SeriesPoint is one point in a time series (x is a period label, e.g. "2026-07").
type SeriesPoint struct {
	X string `json:"x"`
	Y int64  `json:"y"`
}

// StatusAmount pairs a status with its count and summed amount (minor units).
type StatusAmount struct {
	Status      string `json:"status"`
	Count       int    `json:"count"`
	AmountMinor int64  `json:"amount_minor"`
}

// MeetingAttendanceStat summarizes attendance at one meeting.
type MeetingAttendanceStat struct {
	Label     string `json:"label"`
	Present   int    `json:"present"`
	Attendees int    `json:"attendees"`
}

// AnalyticsOverview holds the headline KPIs for the analytics dashboard. Money
// figures are converted into Currency (the org reporting currency).
// UnconvertibleCurrencies lists any currencies present in the data that had no
// exchange rate to the reporting currency and were therefore left out of the
// totals — the UI warns when it is non-empty. MixedCurrencies is retained for
// backward compatibility (true when money spans more than one currency at all).
type AnalyticsOverview struct {
	ActiveMembers           int      `json:"active_members"`
	YTDPaymentsMinor        int64    `json:"ytd_payments_minor"`
	OutstandingMinor        int64    `json:"outstanding_minor"`
	OpenMotions             int      `json:"open_motions"`
	UpcomingMeetings        int      `json:"upcoming_meetings"`
	Currency                string   `json:"currency"`
	MixedCurrencies         bool     `json:"mixed_currencies"`
	UnconvertibleCurrencies []string `json:"unconvertible_currencies,omitempty"`
}

// MembershipAnalytics breaks the roster down by status and tier, with monthly joins.
type MembershipAnalytics struct {
	ByStatus    []CategoryValue `json:"by_status"`
	ByTier      []CategoryValue `json:"by_tier"`
	Growth      []SeriesPoint   `json:"growth"`
	ActiveTotal int             `json:"active_total"`
}

// AttendanceAnalytics holds per-meeting attendance and the average present count.
type AttendanceAnalytics struct {
	Meetings   []MeetingAttendanceStat `json:"meetings"`
	AvgPresent float64                 `json:"avg_present"`
}

// GovernanceAnalytics summarizes motion outcomes and the overall vote split.
type GovernanceAnalytics struct {
	ByOutcome    []CategoryValue `json:"by_outcome"`
	Votes        []CategoryValue `json:"votes"`
	TotalMotions int             `json:"total_motions"`
}

// PaymentsAnalytics holds monthly collections, dues status breakdown, and
// outstanding total, all converted into Currency (the reporting currency).
// UnconvertibleCurrencies lists currencies left out of the totals for lack of a
// rate.
type PaymentsAnalytics struct {
	Monthly                 []SeriesPoint  `json:"monthly"`
	DuesByStatus            []StatusAmount `json:"dues_by_status"`
	OutstandingMinor        int64          `json:"outstanding_minor"`
	Currency                string         `json:"currency"`
	UnconvertibleCurrencies []string       `json:"unconvertible_currencies,omitempty"`
}

// BudgetScenario is a named draft budget for what-if planning. Lines and Totals
// are populated when a single scenario is fetched (and Totals also on list).
type BudgetScenario struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description *string      `json:"description,omitempty"`
	PeriodLabel *string      `json:"period_label,omitempty"`
	Status      string       `json:"status"`
	Currency    string       `json:"currency"`
	CreatedBy   *string      `json:"created_by,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
	Lines       []BudgetLine `json:"lines,omitempty"`
	Totals      BudgetTotals `json:"totals"`
}

// BudgetLine is one income or expense line. AmountMinor is the computed
// contribution (Quantity × UnitAmountMinor), filled by the repo.
type BudgetLine struct {
	ID              string    `json:"id"`
	ScenarioID      string    `json:"scenario_id"`
	Kind            string    `json:"kind"`
	Category        *string   `json:"category,omitempty"`
	Label           string    `json:"label"`
	Quantity        int64     `json:"quantity"`
	UnitAmountMinor int64     `json:"unit_amount_minor"`
	AmountMinor     int64     `json:"amount_minor"`
	Note            *string   `json:"note,omitempty"`
	SortOrder       int       `json:"sort_order"`
	CreatedAt       time.Time `json:"created_at"`
}

// BudgetTotals summarizes a scenario: total income, total expense, and the net
// (surplus if positive, deficit if negative), all in minor units.
type BudgetTotals struct {
	IncomeMinor  int64  `json:"income_minor"`
	ExpenseMinor int64  `json:"expense_minor"`
	NetMinor     int64  `json:"net_minor"`
	Currency     string `json:"currency"`
}

// ValidBudgetStatuses and ValidBudgetKinds are the authoritative allowed sets.
var (
	ValidBudgetStatuses = map[string]bool{"draft": true, "active": true, "archived": true}
	ValidBudgetKinds    = map[string]bool{"income": true, "expense": true}
)

// DuesSchedule defines recurring dues for a member tier: the amount and how
// often it is billed. Cadence is "annual", "quarterly", or "monthly"; DueDays
// is how many days after each period's start the invoice falls due.
type DuesSchedule struct {
	ID          string    `json:"id"`
	Tier        string    `json:"tier"`
	AmountMinor int64     `json:"amount_minor"`
	Currency    string    `json:"currency"`
	Cadence     string    `json:"cadence"`
	DueDays     int       `json:"due_days"`
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ValidCadences is the authoritative set of dues-schedule cadences.
var ValidCadences = map[string]bool{"annual": true, "quarterly": true, "monthly": true}

// GovernanceSettings holds the org-wide quorum and voting rules (a single row).
// QuorumMode: "majority" (floor(active/2)+1), "percent" (ceil(active*value/100)),
// or "fixed" (value as an absolute count). DefaultThreshold seeds new motions.
type GovernanceSettings struct {
	QuorumMode               string    `json:"quorum_mode"`
	QuorumValue              int       `json:"quorum_value"`
	ProxiesCountTowardQuorum bool      `json:"proxies_count_toward_quorum"`
	DefaultThreshold         string    `json:"default_threshold"`
	UpdatedAt                time.Time `json:"updated_at"`
}

// ValidQuorumModes and ValidThresholds are the authoritative allowed sets.
var (
	ValidQuorumModes = map[string]bool{"majority": true, "percent": true, "fixed": true}
	ValidThresholds  = map[string]bool{"majority": true, "two_thirds": true, "unanimous": true}
	ValidVoteChoices = map[string]bool{"for": true, "against": true, "abstain": true}
)

// QuorumStatus is the live quorum computation for a meeting.
// EffectivePresent is PresentCount plus proxy-represented members (when proxies
// count toward quorum); Met is EffectivePresent >= Required.
type QuorumStatus struct {
	Mode               string `json:"mode"`
	Required           int    `json:"required"`
	ActiveMembers      int    `json:"active_members"`
	PresentCount       int    `json:"present_count"`
	ProxiesRepresented int    `json:"proxies_represented"`
	EffectivePresent   int    `json:"effective_present"`
	Met                bool   `json:"met"`
}

// Motion is a formal proposal put to a vote within a meeting.
// Status lifecycle: "draft" → "seconded" → "open" → terminal
// ("carried", "failed", "tabled", "withdrawn"). Threshold is the passing bar:
// "majority", "two_thirds", or "unanimous". Tally is computed from ballots and
// populated when a motion is fetched or listed.
type Motion struct {
	ID           string       `json:"id"`
	MeetingID    string       `json:"meeting_id"`
	Title        string       `json:"title"`
	Detail       *string      `json:"detail,omitempty"`
	MoverID      *string      `json:"mover_id,omitempty"`
	MoverName    *string      `json:"mover_name,omitempty"`
	SeconderID   *string      `json:"seconder_id,omitempty"`
	SeconderName *string      `json:"seconder_name,omitempty"`
	Threshold    string       `json:"threshold"`
	Status       string       `json:"status"`
	CreatedBy    *string      `json:"created_by,omitempty"`
	OpenedAt     *time.Time   `json:"opened_at,omitempty"`
	ClosedAt     *time.Time   `json:"closed_at,omitempty"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
	Tally        MotionTally  `json:"tally"`
	Votes        []MotionVote `json:"votes,omitempty"`
}

// MotionTally aggregates the ballots on a motion. Carried is set relative to the
// motion's threshold, counting only for/against (abstentions don't count toward
// the bar but are reported).
type MotionTally struct {
	For     int  `json:"for"`
	Against int  `json:"against"`
	Abstain int  `json:"abstain"`
	Total   int  `json:"total"`
	Carried bool `json:"carried"`
}

// MotionVote is a single member's ballot on a motion.
type MotionVote struct {
	MemberID   string    `json:"member_id"`
	MemberName string    `json:"member_name"`
	Choice     string    `json:"choice"`
	IsProxy    bool      `json:"is_proxy"`
	CastAt     time.Time `json:"cast_at"`
}

// BallotContext is what a public ballot page shows for a tokenized async vote:
// the motion being voted on and the member the token belongs to.
type BallotContext struct {
	MotionID     string  `json:"motion_id"`
	MemberID     string  `json:"-"`
	MemberName   string  `json:"member_name"`
	MeetingTitle string  `json:"meeting_title"`
	Title        string  `json:"title"`
	Detail       *string `json:"detail,omitempty"`
	Threshold    string  `json:"threshold"`
	Status       string  `json:"status"`
}

// MeetingProxy records that HolderID may cast GrantorID's ballot at a meeting.
type MeetingProxy struct {
	ID          string    `json:"id"`
	MeetingID   string    `json:"meeting_id"`
	GrantorID   string    `json:"grantor_id"`
	GrantorName string    `json:"grantor_name"`
	HolderID    string    `json:"holder_id"`
	HolderName  string    `json:"holder_name"`
	CreatedAt   time.Time `json:"created_at"`
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

// Notification is a single in-app notice for a user. Type is a dotted event key
// (e.g. "motion.opened"); Link is an in-app hash route to open on click.
type Notification struct {
	ID        string     `json:"id"`
	Type      string     `json:"type"`
	Title     string     `json:"title"`
	Body      *string    `json:"body,omitempty"`
	Link      *string    `json:"link,omitempty"`
	ReadAt    *time.Time `json:"read_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// NotificationPreferences captures a user's per-category email opt-outs. In-app
// notices are always recorded regardless of these flags.
type NotificationPreferences struct {
	GovernanceEmail  bool `json:"governance_email"`
	MeetingsEmail    bool `json:"meetings_email"`
	DuesEmail        bool `json:"dues_email"`
	AssignmentsEmail bool `json:"assignments_email"`
}

// NotificationCategory maps a dotted notification type to the preference
// category that gates its email delivery. Unknown types default to governance.
func NotificationCategory(notifType string) string {
	switch {
	case strings.HasPrefix(notifType, "meeting."):
		return "meetings"
	case strings.HasPrefix(notifType, "dues."):
		return "dues"
	case strings.HasPrefix(notifType, "action_item."):
		return "assignments"
	default:
		return "governance" // motion.*, ballot.*, and anything else
	}
}

// AuditEntry is one recorded mutating action, with the actor's email resolved.
// UserEmail/EntityType/EntityID are nullable: the actor may have been deleted
// (the FK is ON DELETE SET NULL) and some actions target no specific row.
type AuditEntry struct {
	ID         string    `json:"id"`
	Seq        int64     `json:"seq,omitempty"`
	UserID     *string   `json:"user_id,omitempty"`
	UserEmail  *string   `json:"user_email,omitempty"`
	Action     string    `json:"action"`
	EntityType *string   `json:"entity_type,omitempty"`
	EntityID   *string   `json:"entity_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	PrevHash   string    `json:"prev_hash,omitempty"`
	EntryHash  string    `json:"entry_hash,omitempty"`
}
