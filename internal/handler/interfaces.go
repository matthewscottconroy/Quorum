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
	CreateUser(ctx context.Context, email, hash, role string) (*model.User, error)
	CreateFirstUser(ctx context.Context, email, hash, role string) (*model.User, error)
	UpdateLastLogin(ctx context.Context, id string) error
	StoreRefreshToken(ctx context.Context, userID, hash string, expiresAt time.Time) error
	GetRefreshToken(ctx context.Context, hash string) (userID string, revoked bool, expiresAt time.Time, err error)
	RevokeRefreshToken(ctx context.Context, hash string) error
	ListUsers(ctx context.Context) ([]model.User, error)
	UpdateUserRole(ctx context.Context, id, role string) (*model.User, error)
	DeleteUser(ctx context.Context, id string) error
	GetPasswordHash(ctx context.Context, id string) (string, error)
	UpdatePasswordHash(ctx context.Context, id, hash string) error
	RevokeAllRefreshTokensForUser(ctx context.Context, userID string) error
}

// membersRepo is satisfied by *repo.MembersRepo.
type membersRepo interface {
	List(ctx context.Context, f repo.MemberFilter) ([]model.Member, int, error)
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
	CreateInvoice(ctx context.Context, inv *model.DuesInvoice) (*model.DuesInvoice, error)
	CreateInvoiceBatch(ctx context.Context, invs []*model.DuesInvoice) ([]model.DuesInvoice, error)
	UpdateInvoiceStatus(ctx context.Context, id, status string, notes *string) error
	RecomputeInvoiceStatus(ctx context.Context, id string) error
	CountByStatus(ctx context.Context, status string) (int, error)
	ListTransactions(ctx context.Context, f repo.TransactionFilter) ([]model.Transaction, int, error)
	CreateTransaction(ctx context.Context, t *model.Transaction) (*model.Transaction, error)
	FindInvoiceByProviderRef(ctx context.Context, providerRef string) (string, error)
	EventProcessed(ctx context.Context, eventID string) (bool, error)
	MarkEventProcessed(ctx context.Context, eventID string) error
}

// meetingsRepo is satisfied by *repo.MeetingsRepo.
type meetingsRepo interface {
	List(ctx context.Context, f repo.MeetingFilter) ([]model.Meeting, int, error)
	Get(ctx context.Context, id string) (*model.Meeting, error)
	Create(ctx context.Context, mt *model.Meeting, createdBy string) (*model.Meeting, error)
	Update(ctx context.Context, id string, title *string, scheduledAt *time.Time, location, agenda, notes, status *string) (*model.Meeting, error)
	Delete(ctx context.Context, id string) error
	GetAttendees(ctx context.Context, meetingID string) ([]model.MeetingAttendee, error)
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
	CreateDecision(ctx context.Context, d *model.PlanDecision, decidedBy string) (*model.PlanDecision, error)
	UpdateDecision(ctx context.Context, id string, summary, rationale *string) (*model.PlanDecision, error)
	DeleteDecision(ctx context.Context, id string) error
}

// contactsRepo is satisfied by *repo.ContactsRepo.
type contactsRepo interface {
	List(ctx context.Context, f repo.ContactFilter) ([]model.Contact, int, error)
	Get(ctx context.Context, id string) (*model.Contact, error)
	Create(ctx context.Context, c *model.Contact, createdBy string) (*model.Contact, error)
	Update(ctx context.Context, id string, c *model.Contact) (*model.Contact, error)
	Delete(ctx context.Context, id string) error
}

// resourcesRepo is satisfied by *repo.ResourcesRepo.
type resourcesRepo interface {
	List(ctx context.Context, f repo.ResourceFilter) ([]model.Resource, int, error)
	Get(ctx context.Context, id string) (*model.Resource, error)
	Create(ctx context.Context, res *model.Resource, addedBy string) (*model.Resource, error)
	Update(ctx context.Context, id string, res *model.Resource) (*model.Resource, error)
	Delete(ctx context.Context, id string) error
}

// actionItemsRepo is satisfied by *repo.ActionItemsRepo.
type actionItemsRepo interface {
	List(ctx context.Context, f repo.ActionItemFilter) ([]model.ActionItem, int, error)
	Create(ctx context.Context, item *model.ActionItem, createdBy string) (*model.ActionItem, error)
	Update(ctx context.Context, id string, fields map[string]any) (*model.ActionItem, error)
	Delete(ctx context.Context, id string) error
}

// auditRepo is satisfied by *repo.AuditRepo.
type auditRepo interface {
	Log(ctx context.Context, userID, action, entityID string) error
}
