package handler

import (
	"context"
	"time"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// authRepo is satisfied by *repo.AuthRepo.
type authRepo interface {
	GetUserByEmail(ctx context.Context, email string) (*model.User, string, error)
	GetUserByID(ctx context.Context, id string) (*model.User, error)
	CreateUser(ctx context.Context, email, hash, role string, memberID *string) (*model.User, error)
	SetUserMember(ctx context.Context, id string, memberID *string) error
	CreateFirstUser(ctx context.Context, email, hash, role string) (*model.User, error)
	UpdateLastLogin(ctx context.Context, id string) error
	StoreRefreshToken(ctx context.Context, userID, hash string, expiresAt time.Time) error
	GetRefreshToken(ctx context.Context, hash string) (userID string, revoked bool, expiresAt time.Time, err error)
	RevokeRefreshToken(ctx context.Context, hash string) error
	CountUsers(ctx context.Context) (int, error)
	ListUsers(ctx context.Context) ([]model.User, error)
	UpdateUserRole(ctx context.Context, id, role string) (*model.User, error)
	DeleteUser(ctx context.Context, id string) error
	GetPasswordHash(ctx context.Context, id string) (string, error)
	UpdatePasswordHash(ctx context.Context, id, hash string) error
	RevokeAllRefreshTokensForUser(ctx context.Context, userID string) error
	ActiveSessionCount(ctx context.Context, userID string) (int, error)
	RevokeOtherRefreshTokensForUser(ctx context.Context, userID, keepHash string) (int64, error)
	CreatePasswordResetToken(ctx context.Context, userID, hash string, expiresAt time.Time) error
	ConsumePasswordResetToken(ctx context.Context, hash string) (string, error)
	GetTOTP(ctx context.Context, userID string) (secret string, enabled bool, err error)
	SetTOTPSecret(ctx context.Context, userID, secret string) error
	EnableTOTP(ctx context.Context, userID string) error
	DisableTOTP(ctx context.Context, userID string) error
	ReplaceRecoveryCodes(ctx context.Context, userID string, hashes []string) error
	ConsumeRecoveryCode(ctx context.Context, userID, hash string) (bool, error)
}

// membersRepo is satisfied by *repo.MembersRepo.
type membersRepo interface {
	List(ctx context.Context, f repo.MemberFilter) ([]model.Member, int, error)
	Erase(ctx context.Context, id string) error
	Get(ctx context.Context, id string) (*model.Member, error)
	Create(ctx context.Context, m *model.Member) (*model.Member, error)
	Update(ctx context.Context, id string, fields map[string]any) (*model.Member, error)
	Delete(ctx context.Context, id string) error
	Count(ctx context.Context) (int, error)
}

// duesRepo is satisfied by *repo.DuesRepo.
type duesRepo interface {
	ListInvoices(ctx context.Context, f repo.InvoiceFilter) ([]model.DuesInvoice, int, error)
	GetInvoice(ctx context.Context, id string) (*model.DuesInvoice, error)
	CreateInvoiceBatch(ctx context.Context, invs []*model.DuesInvoice) ([]model.DuesInvoice, error)
	UpdateInvoiceStatus(ctx context.Context, id, status string, notes *string) error
	RecomputeInvoiceStatus(ctx context.Context, id string) error
	CountByStatus(ctx context.Context, status string) (int, error)
	ListTransactions(ctx context.Context, f repo.TransactionFilter) ([]model.Transaction, int, error)
	CreateTransaction(ctx context.Context, t *model.Transaction) (*model.Transaction, error)
	FindInvoiceByProviderRef(ctx context.Context, providerRef string) (string, error)
	MarkEventProcessed(ctx context.Context, eventID string) error
	RecordWebhookPayment(ctx context.Context, eventID string, t *model.Transaction) (already bool, err error)
	ListSchedules(ctx context.Context) ([]model.DuesSchedule, error)
	GetSchedule(ctx context.Context, id string) (*model.DuesSchedule, error)
	CreateSchedule(ctx context.Context, s *model.DuesSchedule) (*model.DuesSchedule, error)
	UpdateSchedule(ctx context.Context, id string, tier *string, amountMinor *int64, currency, cadence *string, dueDays *int, active *bool) (*model.DuesSchedule, error)
	DeleteSchedule(ctx context.Context, id string) error
	GenerateInvoicesForSchedule(ctx context.Context, s model.DuesSchedule, label string, due time.Time) (int, error)
}

// meetingsRepo is satisfied by *repo.MeetingsRepo.
type meetingsRepo interface {
	List(ctx context.Context, f repo.MeetingFilter) ([]model.Meeting, int, error)
	Get(ctx context.Context, id string) (*model.Meeting, error)
	Create(ctx context.Context, mt *model.Meeting, createdBy string) (*model.Meeting, error)
	Update(ctx context.Context, id string, title *string, scheduledAt *time.Time, location, agenda, notes, status *string) (*model.Meeting, error)
	Delete(ctx context.Context, id string) error
	GetAttendees(ctx context.Context, meetingID string) ([]model.MeetingAttendee, error)
	AttendeeEmails(ctx context.Context, meetingID string) ([]string, error)
	HasGovernanceHistory(ctx context.Context, meetingID string) (bool, error)
	SetAttendees(ctx context.Context, meetingID string, attendees []model.MeetingAttendee) error
	CreateDecision(ctx context.Context, d *model.MeetingDecision) (*model.MeetingDecision, error)
	UpdateDecision(ctx context.Context, id string, summary, detail, outcome *string, voteFor, voteAgainst, voteAbstain *int) (*model.MeetingDecision, error)
	DeleteDecision(ctx context.Context, id string) error
	Upcoming(ctx context.Context, n int) ([]model.Meeting, error)
}

// plansRepo is satisfied by *repo.PlansRepo.
type plansRepo interface {
	List(ctx context.Context, f repo.PlanFilter) ([]model.Plan, int, error)
	Get(ctx context.Context, id string) (*model.Plan, error)
	Create(ctx context.Context, p *model.Plan, createdBy string) (*model.Plan, error)
	Update(ctx context.Context, id string, fields map[string]any) (*model.Plan, error)
	Delete(ctx context.Context, id string) error
	OwnerEmail(ctx context.Context, planID string) (string, error)
	CreateDecision(ctx context.Context, d *model.PlanDecision, decidedBy string) (*model.PlanDecision, error)
	UpdateDecision(ctx context.Context, id string, summary, rationale *string) (*model.PlanDecision, error)
	DeleteDecision(ctx context.Context, id string) error
}

// contactsRepo is satisfied by *repo.ContactsRepo.
type contactsRepo interface {
	List(ctx context.Context, f repo.ContactFilter) ([]model.Contact, int, error)
	Get(ctx context.Context, id string) (*model.Contact, error)
	Create(ctx context.Context, c *model.Contact, createdBy string) (*model.Contact, error)
	Update(ctx context.Context, id string, fields map[string]any) (*model.Contact, error)
	Delete(ctx context.Context, id string) error
}

// resourcesRepo is satisfied by *repo.ResourcesRepo.
type resourcesRepo interface {
	List(ctx context.Context, f repo.ResourceFilter) ([]model.Resource, int, error)
	Get(ctx context.Context, id string) (*model.Resource, error)
	Create(ctx context.Context, res *model.Resource, addedBy string) (*model.Resource, error)
	Update(ctx context.Context, id string, fields map[string]any) (*model.Resource, error)
	Delete(ctx context.Context, id string) error
}

// actionItemsRepo is satisfied by *repo.ActionItemsRepo.
type actionItemsRepo interface {
	List(ctx context.Context, f repo.ActionItemFilter) ([]model.ActionItem, int, error)
	Get(ctx context.Context, id string) (*model.ActionItem, error)
	Create(ctx context.Context, item *model.ActionItem, createdBy string) (*model.ActionItem, error)
	Update(ctx context.Context, id string, fields map[string]any) (*model.ActionItem, error)
	Delete(ctx context.Context, id string) error
	AssigneeEmail(ctx context.Context, id string) (string, error)
}

// auditRepo is satisfied by *repo.AuditRepo.
type auditRepo interface {
	Log(ctx context.Context, userID, action, entityType, entityID string) error
}

// fxRepo is satisfied by *repo.FXRepo.
type fxRepo interface {
	ReportingCurrency(ctx context.Context) (string, error)
	SetReportingCurrency(ctx context.Context, code string) error
	ListRates(ctx context.Context) ([]model.FXRate, error)
	CreateRate(ctx context.Context, from, to, rate, effectiveAt, createdBy string) (*model.FXRate, error)
	DeleteRate(ctx context.Context, id string) error
}

// analyticsRepo is satisfied by *repo.AnalyticsRepo.
type analyticsRepo interface {
	Overview(ctx context.Context) (*model.AnalyticsOverview, error)
	Membership(ctx context.Context) (*model.MembershipAnalytics, error)
	Attendance(ctx context.Context) (*model.AttendanceAnalytics, error)
	Governance(ctx context.Context) (*model.GovernanceAnalytics, error)
	Payments(ctx context.Context) (*model.PaymentsAnalytics, error)
}

// budgetRepo is satisfied by *repo.BudgetRepo.
type budgetRepo interface {
	ListScenarios(ctx context.Context) ([]model.BudgetScenario, error)
	CompareScenarios(ctx context.Context, ids []string) ([]model.BudgetScenario, error)
	GetScenario(ctx context.Context, id string) (*model.BudgetScenario, error)
	CreateScenario(ctx context.Context, s *model.BudgetScenario, createdBy string) (*model.BudgetScenario, error)
	UpdateScenario(ctx context.Context, id string, name, description, periodLabel, status, currency *string) (*model.BudgetScenario, error)
	DeleteScenario(ctx context.Context, id string) error
	CloneScenario(ctx context.Context, id, newName, createdBy string) (*model.BudgetScenario, error)
	SeedDuesIncome(ctx context.Context, scenarioID string) (int, error)
	AddLine(ctx context.Context, l *model.BudgetLine) (*model.BudgetLine, error)
	UpdateLine(ctx context.Context, id string, kind, category, label *string, quantity, unitAmountMinor *int64, note *string, sortOrder *int) (*model.BudgetLine, error)
	DeleteLine(ctx context.Context, id string) error
}

// governanceRepo is satisfied by *repo.GovernanceRepo.
type governanceRepo interface {
	GetSettings(ctx context.Context) (*model.GovernanceSettings, error)
	UpdateSettings(ctx context.Context, s *model.GovernanceSettings) (*model.GovernanceSettings, error)
	ComputeQuorum(ctx context.Context, meetingID string) (*model.QuorumStatus, error)
	ListMotions(ctx context.Context, meetingID string) ([]model.Motion, error)
	GetMotion(ctx context.Context, id string) (*model.Motion, error)
	CreateMotion(ctx context.Context, m *model.Motion, createdBy string) (*model.Motion, error)
	UpdateMotion(ctx context.Context, id string, title, detail *string, moverID, seconderID *string, threshold *string) (*model.Motion, error)
	SetMotionStatus(ctx context.Context, id, status string, seconderID *string) (*model.Motion, error)
	DeleteMotion(ctx context.Context, id string) error
	MotionStatus(ctx context.Context, id string) (status, meetingID string, err error)
	CastVote(ctx context.Context, motionID, memberID, choice string, isProxy bool, castBy string) error
	MemberIsActive(ctx context.Context, memberID string) (bool, error)
	GetVotes(ctx context.Context, motionID string) ([]model.MotionVote, error)
	ListProxies(ctx context.Context, meetingID string) ([]model.MeetingProxy, error)
	CreateProxy(ctx context.Context, meetingID, grantorID, holderID string) (*model.MeetingProxy, error)
	DeleteProxy(ctx context.Context, id string) error
	EligibleBallotMembers(ctx context.Context, motionID string) ([]repo.BallotRecipient, error)
	UpsertBallotToken(ctx context.Context, motionID, memberID, hash string, expiresAt time.Time) error
	GetBallotContext(ctx context.Context, hash string) (*model.BallotContext, error)
	ConsumeBallotAndVote(ctx context.Context, hash, choice string) (motionID string, err error)
}
