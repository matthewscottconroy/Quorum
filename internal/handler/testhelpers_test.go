package handler

// Mock repo implementations for unit tests.
// Each field is a function that can be swapped per-test.
// Unset functions panic, making it obvious when an unexpected call occurs.

import (
	"context"
	"time"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// fakeNotifier captures the arguments of the most recent NotifyDeletion call.
type fakeNotifier struct {
	called     bool
	actor      string
	entityType string
	entityName string
	affected   []string
}

func (f *fakeNotifier) NotifyDeletion(_ context.Context, actorUserID, entityType, entityName string, affectedEmails []string) {
	f.called = true
	f.actor = actorUserID
	f.entityType = entityType
	f.entityName = entityName
	f.affected = affectedEmails
}

// ---- mockAuthRepo ----

type mockAuthRepo struct {
	GetUserByEmailFn                func(ctx context.Context, email string) (*model.User, string, error)
	GetUserByIDFn                   func(ctx context.Context, id string) (*model.User, error)
	CreateUserFn                    func(ctx context.Context, email, hash, role string) (*model.User, error)
	CreateFirstUserFn               func(ctx context.Context, email, hash, role string) (*model.User, error)
	UpdateLastLoginFn               func(ctx context.Context, id string) error
	StoreRefreshTokenFn             func(ctx context.Context, userID, hash string, expiresAt time.Time) error
	GetRefreshTokenFn               func(ctx context.Context, hash string) (string, bool, time.Time, error)
	RevokeRefreshTokenFn            func(ctx context.Context, hash string) error
	CountUsersFn                    func(ctx context.Context) (int, error)
	ListUsersFn                     func(ctx context.Context) ([]model.User, error)
	UpdateUserRoleFn                func(ctx context.Context, id, role string) (*model.User, error)
	DeleteUserFn                    func(ctx context.Context, id string) error
	GetPasswordHashFn               func(ctx context.Context, id string) (string, error)
	UpdatePasswordHashFn            func(ctx context.Context, id, hash string) error
	RevokeAllRefreshTokensForUserFn func(ctx context.Context, userID string) error
}

func (m *mockAuthRepo) GetUserByEmail(ctx context.Context, email string) (*model.User, string, error) {
	return m.GetUserByEmailFn(ctx, email)
}
func (m *mockAuthRepo) GetUserByID(ctx context.Context, id string) (*model.User, error) {
	return m.GetUserByIDFn(ctx, id)
}
func (m *mockAuthRepo) CreateUser(ctx context.Context, email, hash, role string) (*model.User, error) {
	return m.CreateUserFn(ctx, email, hash, role)
}
func (m *mockAuthRepo) CreateFirstUser(ctx context.Context, email, hash, role string) (*model.User, error) {
	return m.CreateFirstUserFn(ctx, email, hash, role)
}
func (m *mockAuthRepo) UpdateLastLogin(ctx context.Context, id string) error {
	return m.UpdateLastLoginFn(ctx, id)
}
func (m *mockAuthRepo) StoreRefreshToken(ctx context.Context, userID, hash string, expiresAt time.Time) error {
	return m.StoreRefreshTokenFn(ctx, userID, hash, expiresAt)
}
func (m *mockAuthRepo) GetRefreshToken(ctx context.Context, hash string) (string, bool, time.Time, error) {
	return m.GetRefreshTokenFn(ctx, hash)
}
func (m *mockAuthRepo) RevokeRefreshToken(ctx context.Context, hash string) error {
	return m.RevokeRefreshTokenFn(ctx, hash)
}
func (m *mockAuthRepo) CountUsers(ctx context.Context) (int, error) {
	if m.CountUsersFn != nil {
		return m.CountUsersFn(ctx)
	}
	return 0, nil
}
func (m *mockAuthRepo) ListUsers(ctx context.Context) ([]model.User, error) {
	return m.ListUsersFn(ctx)
}
func (m *mockAuthRepo) UpdateUserRole(ctx context.Context, id, role string) (*model.User, error) {
	return m.UpdateUserRoleFn(ctx, id, role)
}
func (m *mockAuthRepo) DeleteUser(ctx context.Context, id string) error {
	return m.DeleteUserFn(ctx, id)
}
func (m *mockAuthRepo) GetPasswordHash(ctx context.Context, id string) (string, error) {
	return m.GetPasswordHashFn(ctx, id)
}
func (m *mockAuthRepo) UpdatePasswordHash(ctx context.Context, id, hash string) error {
	return m.UpdatePasswordHashFn(ctx, id, hash)
}
func (m *mockAuthRepo) RevokeAllRefreshTokensForUser(ctx context.Context, userID string) error {
	if m.RevokeAllRefreshTokensForUserFn != nil {
		return m.RevokeAllRefreshTokensForUserFn(ctx, userID)
	}
	return nil
}

// ---- mockMembersRepo ----

type mockMembersRepo struct {
	ListFn   func(ctx context.Context, f repo.MemberFilter) ([]model.Member, int, error)
	GetFn    func(ctx context.Context, id string) (*model.Member, error)
	CreateFn func(ctx context.Context, m *model.Member) (*model.Member, error)
	UpdateFn func(ctx context.Context, id string, fields map[string]any) (*model.Member, error)
	DeleteFn func(ctx context.Context, id string) error
	CountFn  func(ctx context.Context) (int, error)
}

func (m *mockMembersRepo) List(ctx context.Context, f repo.MemberFilter) ([]model.Member, int, error) {
	return m.ListFn(ctx, f)
}
func (m *mockMembersRepo) Get(ctx context.Context, id string) (*model.Member, error) {
	return m.GetFn(ctx, id)
}
func (m *mockMembersRepo) Create(ctx context.Context, mem *model.Member) (*model.Member, error) {
	return m.CreateFn(ctx, mem)
}
func (m *mockMembersRepo) Update(ctx context.Context, id string, fields map[string]any) (*model.Member, error) {
	return m.UpdateFn(ctx, id, fields)
}
func (m *mockMembersRepo) Delete(ctx context.Context, id string) error {
	return m.DeleteFn(ctx, id)
}
func (m *mockMembersRepo) Count(ctx context.Context) (int, error) { return m.CountFn(ctx) }

// ---- mockDuesRepo ----

type mockDuesRepo struct {
	ListInvoicesFn             func(ctx context.Context, f repo.InvoiceFilter) ([]model.DuesInvoice, int, error)
	GetInvoiceFn               func(ctx context.Context, id string) (*model.DuesInvoice, error)
	CreateInvoiceBatchFn       func(ctx context.Context, invs []*model.DuesInvoice) ([]model.DuesInvoice, error)
	UpdateInvoiceStatusFn      func(ctx context.Context, id, status string, notes *string) error
	RecomputeInvoiceStatusFn   func(ctx context.Context, id string) error
	CountByStatusFn            func(ctx context.Context, status string) (int, error)
	ListTransactionsFn         func(ctx context.Context, f repo.TransactionFilter) ([]model.Transaction, int, error)
	CreateTransactionFn        func(ctx context.Context, t *model.Transaction) (*model.Transaction, error)
	FindInvoiceByProviderRefFn func(ctx context.Context, ref string) (string, error)
	MarkEventProcessedFn       func(ctx context.Context, eventID string) error
	RecordWebhookPaymentFn     func(ctx context.Context, eventID string, t *model.Transaction) (bool, error)
}

func (m *mockDuesRepo) ListInvoices(ctx context.Context, f repo.InvoiceFilter) ([]model.DuesInvoice, int, error) {
	return m.ListInvoicesFn(ctx, f)
}
func (m *mockDuesRepo) GetInvoice(ctx context.Context, id string) (*model.DuesInvoice, error) {
	return m.GetInvoiceFn(ctx, id)
}
func (m *mockDuesRepo) CreateInvoiceBatch(ctx context.Context, invs []*model.DuesInvoice) ([]model.DuesInvoice, error) {
	return m.CreateInvoiceBatchFn(ctx, invs)
}
func (m *mockDuesRepo) UpdateInvoiceStatus(ctx context.Context, id, status string, notes *string) error {
	return m.UpdateInvoiceStatusFn(ctx, id, status, notes)
}
func (m *mockDuesRepo) RecomputeInvoiceStatus(ctx context.Context, id string) error {
	return m.RecomputeInvoiceStatusFn(ctx, id)
}
func (m *mockDuesRepo) CountByStatus(ctx context.Context, status string) (int, error) {
	return m.CountByStatusFn(ctx, status)
}
func (m *mockDuesRepo) ListTransactions(ctx context.Context, f repo.TransactionFilter) ([]model.Transaction, int, error) {
	return m.ListTransactionsFn(ctx, f)
}
func (m *mockDuesRepo) CreateTransaction(ctx context.Context, t *model.Transaction) (*model.Transaction, error) {
	return m.CreateTransactionFn(ctx, t)
}
func (m *mockDuesRepo) FindInvoiceByProviderRef(ctx context.Context, ref string) (string, error) {
	return m.FindInvoiceByProviderRefFn(ctx, ref)
}
func (m *mockDuesRepo) MarkEventProcessed(ctx context.Context, eventID string) error {
	return m.MarkEventProcessedFn(ctx, eventID)
}
func (m *mockDuesRepo) RecordWebhookPayment(ctx context.Context, eventID string, t *model.Transaction) (bool, error) {
	return m.RecordWebhookPaymentFn(ctx, eventID, t)
}

// ---- mockMeetingsRepo ----

type mockMeetingsRepo struct {
	ListFn           func(ctx context.Context, f repo.MeetingFilter) ([]model.Meeting, int, error)
	GetFn            func(ctx context.Context, id string) (*model.Meeting, error)
	CreateFn         func(ctx context.Context, mt *model.Meeting, createdBy string) (*model.Meeting, error)
	UpdateFn         func(ctx context.Context, id string, title *string, scheduledAt *time.Time, location, agenda, notes, status *string) (*model.Meeting, error)
	DeleteFn         func(ctx context.Context, id string) error
	GetAttendeesFn   func(ctx context.Context, meetingID string) ([]model.MeetingAttendee, error)
	AttendeeEmailsFn func(ctx context.Context, meetingID string) ([]string, error)
	SetAttendeesFn   func(ctx context.Context, meetingID string, attendees []model.MeetingAttendee) error
	CreateDecisionFn func(ctx context.Context, d *model.MeetingDecision) (*model.MeetingDecision, error)
	UpdateDecisionFn func(ctx context.Context, id string, summary, detail, outcome *string, voteFor, voteAgainst, voteAbstain *int) (*model.MeetingDecision, error)
	DeleteDecisionFn func(ctx context.Context, id string) error
	UpcomingFn       func(ctx context.Context, n int) ([]model.Meeting, error)
}

func (m *mockMeetingsRepo) List(ctx context.Context, f repo.MeetingFilter) ([]model.Meeting, int, error) {
	return m.ListFn(ctx, f)
}
func (m *mockMeetingsRepo) Get(ctx context.Context, id string) (*model.Meeting, error) {
	return m.GetFn(ctx, id)
}
func (m *mockMeetingsRepo) Create(ctx context.Context, mt *model.Meeting, createdBy string) (*model.Meeting, error) {
	return m.CreateFn(ctx, mt, createdBy)
}
func (m *mockMeetingsRepo) Update(ctx context.Context, id string, title *string, scheduledAt *time.Time, location, agenda, notes, status *string) (*model.Meeting, error) {
	return m.UpdateFn(ctx, id, title, scheduledAt, location, agenda, notes, status)
}
func (m *mockMeetingsRepo) Delete(ctx context.Context, id string) error {
	return m.DeleteFn(ctx, id)
}
func (m *mockMeetingsRepo) GetAttendees(ctx context.Context, meetingID string) ([]model.MeetingAttendee, error) {
	return m.GetAttendeesFn(ctx, meetingID)
}
func (m *mockMeetingsRepo) AttendeeEmails(ctx context.Context, meetingID string) ([]string, error) {
	if m.AttendeeEmailsFn != nil {
		return m.AttendeeEmailsFn(ctx, meetingID)
	}
	return nil, nil
}
func (m *mockMeetingsRepo) SetAttendees(ctx context.Context, meetingID string, attendees []model.MeetingAttendee) error {
	return m.SetAttendeesFn(ctx, meetingID, attendees)
}
func (m *mockMeetingsRepo) CreateDecision(ctx context.Context, d *model.MeetingDecision) (*model.MeetingDecision, error) {
	return m.CreateDecisionFn(ctx, d)
}
func (m *mockMeetingsRepo) UpdateDecision(ctx context.Context, id string, summary, detail, outcome *string, voteFor, voteAgainst, voteAbstain *int) (*model.MeetingDecision, error) {
	return m.UpdateDecisionFn(ctx, id, summary, detail, outcome, voteFor, voteAgainst, voteAbstain)
}
func (m *mockMeetingsRepo) DeleteDecision(ctx context.Context, id string) error {
	return m.DeleteDecisionFn(ctx, id)
}
func (m *mockMeetingsRepo) Upcoming(ctx context.Context, n int) ([]model.Meeting, error) {
	return m.UpcomingFn(ctx, n)
}

// ---- mockActionItemsRepo ----

type mockActionItemsRepo struct {
	ListFn          func(ctx context.Context, f repo.ActionItemFilter) ([]model.ActionItem, int, error)
	GetFn           func(ctx context.Context, id string) (*model.ActionItem, error)
	CreateFn        func(ctx context.Context, item *model.ActionItem, createdBy string) (*model.ActionItem, error)
	UpdateFn        func(ctx context.Context, id string, fields map[string]any) (*model.ActionItem, error)
	DeleteFn        func(ctx context.Context, id string) error
	AssigneeEmailFn func(ctx context.Context, id string) (string, error)
}

func (m *mockActionItemsRepo) List(ctx context.Context, f repo.ActionItemFilter) ([]model.ActionItem, int, error) {
	return m.ListFn(ctx, f)
}
func (m *mockActionItemsRepo) Get(ctx context.Context, id string) (*model.ActionItem, error) {
	return m.GetFn(ctx, id)
}
func (m *mockActionItemsRepo) Create(ctx context.Context, item *model.ActionItem, createdBy string) (*model.ActionItem, error) {
	return m.CreateFn(ctx, item, createdBy)
}
func (m *mockActionItemsRepo) Update(ctx context.Context, id string, fields map[string]any) (*model.ActionItem, error) {
	return m.UpdateFn(ctx, id, fields)
}
func (m *mockActionItemsRepo) Delete(ctx context.Context, id string) error {
	return m.DeleteFn(ctx, id)
}
func (m *mockActionItemsRepo) AssigneeEmail(ctx context.Context, id string) (string, error) {
	if m.AssigneeEmailFn != nil {
		return m.AssigneeEmailFn(ctx, id)
	}
	return "", nil
}

// ---- mockContactsRepo ----

type mockContactsRepo struct {
	ListFn   func(ctx context.Context, f repo.ContactFilter) ([]model.Contact, int, error)
	GetFn    func(ctx context.Context, id string) (*model.Contact, error)
	CreateFn func(ctx context.Context, c *model.Contact, createdBy string) (*model.Contact, error)
	UpdateFn func(ctx context.Context, id string, fields map[string]any) (*model.Contact, error)
	DeleteFn func(ctx context.Context, id string) error
}

func (m *mockContactsRepo) List(ctx context.Context, f repo.ContactFilter) ([]model.Contact, int, error) {
	return m.ListFn(ctx, f)
}
func (m *mockContactsRepo) Get(ctx context.Context, id string) (*model.Contact, error) {
	return m.GetFn(ctx, id)
}
func (m *mockContactsRepo) Create(ctx context.Context, c *model.Contact, createdBy string) (*model.Contact, error) {
	return m.CreateFn(ctx, c, createdBy)
}
func (m *mockContactsRepo) Update(ctx context.Context, id string, fields map[string]any) (*model.Contact, error) {
	return m.UpdateFn(ctx, id, fields)
}
func (m *mockContactsRepo) Delete(ctx context.Context, id string) error {
	return m.DeleteFn(ctx, id)
}

// ---- mockResourcesRepo ----

type mockResourcesRepo struct {
	ListFn   func(ctx context.Context, f repo.ResourceFilter) ([]model.Resource, int, error)
	GetFn    func(ctx context.Context, id string) (*model.Resource, error)
	CreateFn func(ctx context.Context, res *model.Resource, addedBy string) (*model.Resource, error)
	UpdateFn func(ctx context.Context, id string, fields map[string]any) (*model.Resource, error)
	DeleteFn func(ctx context.Context, id string) error
}

func (m *mockResourcesRepo) List(ctx context.Context, f repo.ResourceFilter) ([]model.Resource, int, error) {
	return m.ListFn(ctx, f)
}
func (m *mockResourcesRepo) Get(ctx context.Context, id string) (*model.Resource, error) {
	return m.GetFn(ctx, id)
}
func (m *mockResourcesRepo) Create(ctx context.Context, res *model.Resource, addedBy string) (*model.Resource, error) {
	return m.CreateFn(ctx, res, addedBy)
}
func (m *mockResourcesRepo) Update(ctx context.Context, id string, fields map[string]any) (*model.Resource, error) {
	return m.UpdateFn(ctx, id, fields)
}
func (m *mockResourcesRepo) Delete(ctx context.Context, id string) error {
	return m.DeleteFn(ctx, id)
}

// ---- mockAuditRepo ----

type mockAuditRepo struct {
	LogFn func(ctx context.Context, userID, action, entityID string) error
}

func (m *mockAuditRepo) Log(ctx context.Context, userID, action, entityID string) error {
	if m.LogFn != nil {
		return m.LogFn(ctx, userID, action, entityID)
	}
	return nil
}

// ---- mockPlansRepo ----

type mockPlansRepo struct {
	ListFn           func(ctx context.Context, f repo.PlanFilter) ([]model.Plan, int, error)
	GetFn            func(ctx context.Context, id string) (*model.Plan, error)
	CreateFn         func(ctx context.Context, p *model.Plan, createdBy string) (*model.Plan, error)
	UpdateFn         func(ctx context.Context, id string, fields map[string]any) (*model.Plan, error)
	DeleteFn         func(ctx context.Context, id string) error
	OwnerEmailFn     func(ctx context.Context, planID string) (string, error)
	CreateDecisionFn func(ctx context.Context, d *model.PlanDecision, decidedBy string) (*model.PlanDecision, error)
	UpdateDecisionFn func(ctx context.Context, id string, summary, rationale *string) (*model.PlanDecision, error)
	DeleteDecisionFn func(ctx context.Context, id string) error
}

func (m *mockPlansRepo) List(ctx context.Context, f repo.PlanFilter) ([]model.Plan, int, error) {
	return m.ListFn(ctx, f)
}
func (m *mockPlansRepo) Get(ctx context.Context, id string) (*model.Plan, error) {
	return m.GetFn(ctx, id)
}
func (m *mockPlansRepo) Create(ctx context.Context, p *model.Plan, createdBy string) (*model.Plan, error) {
	return m.CreateFn(ctx, p, createdBy)
}
func (m *mockPlansRepo) Update(ctx context.Context, id string, fields map[string]any) (*model.Plan, error) {
	return m.UpdateFn(ctx, id, fields)
}
func (m *mockPlansRepo) Delete(ctx context.Context, id string) error {
	return m.DeleteFn(ctx, id)
}
func (m *mockPlansRepo) OwnerEmail(ctx context.Context, planID string) (string, error) {
	if m.OwnerEmailFn != nil {
		return m.OwnerEmailFn(ctx, planID)
	}
	return "", nil
}
func (m *mockPlansRepo) CreateDecision(ctx context.Context, d *model.PlanDecision, decidedBy string) (*model.PlanDecision, error) {
	return m.CreateDecisionFn(ctx, d, decidedBy)
}
func (m *mockPlansRepo) UpdateDecision(ctx context.Context, id string, summary, rationale *string) (*model.PlanDecision, error) {
	return m.UpdateDecisionFn(ctx, id, summary, rationale)
}
func (m *mockPlansRepo) DeleteDecision(ctx context.Context, id string) error {
	return m.DeleteDecisionFn(ctx, id)
}
