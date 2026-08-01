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
	CreateUserFn                    func(ctx context.Context, email, hash, role string, memberID *string) (*model.User, error)
	SetUserMemberFn                 func(ctx context.Context, id string, memberID *string) error
	CreateFirstUserFn               func(ctx context.Context, email, hash, role string) (*model.User, error)
	UpdateLastLoginFn               func(ctx context.Context, id string) error
	StoreRefreshTokenFn             func(ctx context.Context, userID, hash string, expiresAt time.Time) error
	GetRefreshTokenFn               func(ctx context.Context, hash string) (string, bool, time.Time, time.Time, error)
	RevokeRefreshTokenFn            func(ctx context.Context, hash string) error
	CountUsersFn                    func(ctx context.Context) (int, error)
	ListUsersFn                     func(ctx context.Context) ([]model.User, error)
	UpdateUserRoleFn                func(ctx context.Context, id, role string) (*model.User, error)
	DeleteUserFn                    func(ctx context.Context, id string) error
	GetPasswordHashFn               func(ctx context.Context, id string) (string, error)
	UpdatePasswordHashFn            func(ctx context.Context, id, hash string) error
	RevokeAllRefreshTokensForUserFn func(ctx context.Context, userID string) error
	ActiveSessionCountFn            func(ctx context.Context, userID string) (int, error)
	RevokeOtherRefreshTokensFn      func(ctx context.Context, userID, keepHash string) (int64, error)
	CreatePasswordResetTokenFn      func(ctx context.Context, userID, hash string, expiresAt time.Time) error
	ConsumePasswordResetTokenFn     func(ctx context.Context, hash string) (string, error)
	GetTOTPFn                       func(ctx context.Context, userID string) (string, bool, error)
	SetTOTPSecretFn                 func(ctx context.Context, userID, secret string) error
	EnableTOTPFn                    func(ctx context.Context, userID string) error
	DisableTOTPFn                   func(ctx context.Context, userID string) error
	ReplaceRecoveryCodesFn          func(ctx context.Context, userID string, hashes []string) error
	ConsumeRecoveryCodeFn           func(ctx context.Context, userID, hash string) (bool, error)
}

func (m *mockAuthRepo) CreatePasswordResetToken(ctx context.Context, userID, hash string, e time.Time) error {
	if m.CreatePasswordResetTokenFn != nil {
		return m.CreatePasswordResetTokenFn(ctx, userID, hash, e)
	}
	return nil
}
func (m *mockAuthRepo) ConsumePasswordResetToken(ctx context.Context, hash string) (string, error) {
	return m.ConsumePasswordResetTokenFn(ctx, hash)
}
func (m *mockAuthRepo) GetTOTP(ctx context.Context, userID string) (string, bool, error) {
	if m.GetTOTPFn != nil {
		return m.GetTOTPFn(ctx, userID)
	}
	return "", false, nil
}
func (m *mockAuthRepo) SetTOTPSecret(ctx context.Context, userID, secret string) error {
	if m.SetTOTPSecretFn != nil {
		return m.SetTOTPSecretFn(ctx, userID, secret)
	}
	return nil
}
func (m *mockAuthRepo) EnableTOTP(ctx context.Context, userID string) error {
	if m.EnableTOTPFn != nil {
		return m.EnableTOTPFn(ctx, userID)
	}
	return nil
}
func (m *mockAuthRepo) DisableTOTP(ctx context.Context, userID string) error {
	if m.DisableTOTPFn != nil {
		return m.DisableTOTPFn(ctx, userID)
	}
	return nil
}
func (m *mockAuthRepo) ReplaceRecoveryCodes(ctx context.Context, userID string, hashes []string) error {
	if m.ReplaceRecoveryCodesFn != nil {
		return m.ReplaceRecoveryCodesFn(ctx, userID, hashes)
	}
	return nil
}
func (m *mockAuthRepo) ConsumeRecoveryCode(ctx context.Context, userID, hash string) (bool, error) {
	if m.ConsumeRecoveryCodeFn != nil {
		return m.ConsumeRecoveryCodeFn(ctx, userID, hash)
	}
	return false, nil
}

func (m *mockAuthRepo) GetUserByEmail(ctx context.Context, email string) (*model.User, string, error) {
	return m.GetUserByEmailFn(ctx, email)
}
func (m *mockAuthRepo) GetUserByID(ctx context.Context, id string) (*model.User, error) {
	return m.GetUserByIDFn(ctx, id)
}
func (m *mockAuthRepo) CreateUser(ctx context.Context, email, hash, role string, memberID *string) (*model.User, error) {
	return m.CreateUserFn(ctx, email, hash, role, memberID)
}
func (m *mockAuthRepo) SetUserMember(ctx context.Context, id string, memberID *string) error {
	if m.SetUserMemberFn != nil {
		return m.SetUserMemberFn(ctx, id, memberID)
	}
	return nil
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
func (m *mockAuthRepo) GetRefreshToken(ctx context.Context, hash string) (string, bool, time.Time, time.Time, error) {
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
func (m *mockAuthRepo) ActiveSessionCount(ctx context.Context, userID string) (int, error) {
	if m.ActiveSessionCountFn != nil {
		return m.ActiveSessionCountFn(ctx, userID)
	}
	return 0, nil
}
func (m *mockAuthRepo) RevokeOtherRefreshTokensForUser(ctx context.Context, userID, keepHash string) (int64, error) {
	if m.RevokeOtherRefreshTokensFn != nil {
		return m.RevokeOtherRefreshTokensFn(ctx, userID, keepHash)
	}
	return 0, nil
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
	EraseFn  func(ctx context.Context, id string) error
}

func (m *mockMembersRepo) Erase(ctx context.Context, id string) error {
	if m.EraseFn != nil {
		return m.EraseFn(ctx, id)
	}
	return nil
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
	ListInvoicesFn                func(ctx context.Context, f repo.InvoiceFilter) ([]model.DuesInvoice, int, error)
	GetInvoiceFn                  func(ctx context.Context, id string) (*model.DuesInvoice, error)
	CreateInvoiceBatchFn          func(ctx context.Context, invs []*model.DuesInvoice) ([]model.DuesInvoice, error)
	UpdateInvoiceStatusFn         func(ctx context.Context, id, status string, notes *string) error
	RecomputeInvoiceStatusFn      func(ctx context.Context, id string) error
	CountByStatusFn               func(ctx context.Context, status string) (int, error)
	ListTransactionsFn            func(ctx context.Context, f repo.TransactionFilter) ([]model.Transaction, int, error)
	CreateTransactionFn           func(ctx context.Context, t *model.Transaction) (*model.Transaction, error)
	FindInvoiceByProviderRefFn    func(ctx context.Context, ref string) (string, error)
	MarkEventProcessedFn          func(ctx context.Context, eventID string) error
	RecordWebhookPaymentFn        func(ctx context.Context, eventID string, t *model.Transaction) (bool, error)
	ListSchedulesFn               func(ctx context.Context) ([]model.DuesSchedule, error)
	GetScheduleFn                 func(ctx context.Context, id string) (*model.DuesSchedule, error)
	CreateScheduleFn              func(ctx context.Context, s *model.DuesSchedule) (*model.DuesSchedule, error)
	UpdateScheduleFn              func(ctx context.Context, id string, tier *string, amountMinor *int64, currency, cadence *string, dueDays *int, active *bool) (*model.DuesSchedule, error)
	DeleteScheduleFn              func(ctx context.Context, id string) error
	GenerateInvoicesForScheduleFn func(ctx context.Context, s model.DuesSchedule, label string, due time.Time) (int, error)
}

func (m *mockDuesRepo) ListSchedules(ctx context.Context) ([]model.DuesSchedule, error) {
	return m.ListSchedulesFn(ctx)
}
func (m *mockDuesRepo) GetSchedule(ctx context.Context, id string) (*model.DuesSchedule, error) {
	return m.GetScheduleFn(ctx, id)
}
func (m *mockDuesRepo) CreateSchedule(ctx context.Context, s *model.DuesSchedule) (*model.DuesSchedule, error) {
	return m.CreateScheduleFn(ctx, s)
}
func (m *mockDuesRepo) UpdateSchedule(ctx context.Context, id string, tier *string, amountMinor *int64, currency, cadence *string, dueDays *int, active *bool) (*model.DuesSchedule, error) {
	return m.UpdateScheduleFn(ctx, id, tier, amountMinor, currency, cadence, dueDays, active)
}
func (m *mockDuesRepo) DeleteSchedule(ctx context.Context, id string) error {
	return m.DeleteScheduleFn(ctx, id)
}
func (m *mockDuesRepo) GenerateInvoicesForSchedule(ctx context.Context, s model.DuesSchedule, label string, due time.Time) (int, error) {
	return m.GenerateInvoicesForScheduleFn(ctx, s, label, due)
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
	ListFn                 func(ctx context.Context, f repo.MeetingFilter) ([]model.Meeting, int, error)
	GetFn                  func(ctx context.Context, id string) (*model.Meeting, error)
	CreateFn               func(ctx context.Context, mt *model.Meeting, createdBy string) (*model.Meeting, error)
	UpdateFn               func(ctx context.Context, id string, title *string, scheduledAt, endsAt *time.Time, clearEndsAt bool, location, agenda, notes, status *string) (*model.Meeting, error)
	DeleteFn               func(ctx context.Context, id string) error
	GetAttendeesFn         func(ctx context.Context, meetingID string) ([]model.MeetingAttendee, error)
	AttendeeEmailsFn       func(ctx context.Context, meetingID string) ([]string, error)
	HasGovernanceHistoryFn func(ctx context.Context, meetingID string) (bool, error)
	ListMinutesFn          func(ctx context.Context, meetingID string) ([]model.MinutesEntry, error)
	AddMinutesEntryFn      func(ctx context.Context, meetingID, kind, body string, motionID *string, recordedBy string) (*model.MinutesEntry, error)
	UpdateMinutesEntryFn   func(ctx context.Context, meetingID, entryID, kind, body string, motionID *string) (*model.MinutesEntry, error)
	DeleteMinutesEntryFn   func(ctx context.Context, meetingID, entryID string) error
	FinalizeMinutesFn      func(ctx context.Context, meetingID, userID string) error
	SetAttendeesFn         func(ctx context.Context, meetingID string, attendees []model.MeetingAttendee) error
	CreateDecisionFn       func(ctx context.Context, d *model.MeetingDecision) (*model.MeetingDecision, error)
	UpdateDecisionFn       func(ctx context.Context, id string, summary, detail, outcome *string, voteFor, voteAgainst, voteAbstain *int) (*model.MeetingDecision, error)
	DeleteDecisionFn       func(ctx context.Context, id string) error
	UpcomingFn             func(ctx context.Context, n int) ([]model.Meeting, error)
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
func (m *mockMeetingsRepo) Update(ctx context.Context, id string, title *string, scheduledAt, endsAt *time.Time, clearEndsAt bool, location, agenda, notes, status *string) (*model.Meeting, error) {
	return m.UpdateFn(ctx, id, title, scheduledAt, endsAt, clearEndsAt, location, agenda, notes, status)
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
func (m *mockMeetingsRepo) HasGovernanceHistory(ctx context.Context, meetingID string) (bool, error) {
	if m.HasGovernanceHistoryFn != nil {
		return m.HasGovernanceHistoryFn(ctx, meetingID)
	}
	return false, nil // default: no history, so existing delete tests still pass
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
func (m *mockMeetingsRepo) ListMinutes(ctx context.Context, meetingID string) ([]model.MinutesEntry, error) {
	if m.ListMinutesFn != nil {
		return m.ListMinutesFn(ctx, meetingID)
	}
	return nil, nil
}
func (m *mockMeetingsRepo) AddMinutesEntry(ctx context.Context, meetingID, kind, body string, motionID *string, recordedBy string) (*model.MinutesEntry, error) {
	if m.AddMinutesEntryFn != nil {
		return m.AddMinutesEntryFn(ctx, meetingID, kind, body, motionID, recordedBy)
	}
	return &model.MinutesEntry{ID: "e1", MeetingID: meetingID, Kind: kind, Body: body}, nil
}
func (m *mockMeetingsRepo) UpdateMinutesEntry(ctx context.Context, meetingID, entryID, kind, body string, motionID *string) (*model.MinutesEntry, error) {
	if m.UpdateMinutesEntryFn != nil {
		return m.UpdateMinutesEntryFn(ctx, meetingID, entryID, kind, body, motionID)
	}
	return &model.MinutesEntry{ID: entryID, MeetingID: meetingID, Kind: kind, Body: body}, nil
}
func (m *mockMeetingsRepo) DeleteMinutesEntry(ctx context.Context, meetingID, entryID string) error {
	if m.DeleteMinutesEntryFn != nil {
		return m.DeleteMinutesEntryFn(ctx, meetingID, entryID)
	}
	return nil
}
func (m *mockMeetingsRepo) FinalizeMinutes(ctx context.Context, meetingID, userID string) error {
	if m.FinalizeMinutesFn != nil {
		return m.FinalizeMinutesFn(ctx, meetingID, userID)
	}
	return nil
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
	GetVisibleFn func(ctx context.Context, id string, seesAll bool, memberID string) (*model.Resource, error)
	ListFn       func(ctx context.Context, f repo.ResourceFilter) ([]model.Resource, int, error)
	GetFn        func(ctx context.Context, id string) (*model.Resource, error)
	CreateFn     func(ctx context.Context, res *model.Resource, addedBy string) (*model.Resource, error)
	UpdateFn     func(ctx context.Context, id string, fields map[string]any) (*model.Resource, error)
	DeleteFn     func(ctx context.Context, id string) error
}

func (m *mockResourcesRepo) List(ctx context.Context, f repo.ResourceFilter) ([]model.Resource, int, error) {
	return m.ListFn(ctx, f)
}
func (m *mockResourcesRepo) GetVisible(ctx context.Context, id string, seesAll bool, memberID string) (*model.Resource, error) {
	if m.GetVisibleFn != nil {
		return m.GetVisibleFn(ctx, id, seesAll, memberID)
	}
	return m.GetFn(ctx, id)
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
	LogFn func(ctx context.Context, userID, action, entityType, entityID string, detail map[string]any) error
}

func (m *mockAuditRepo) Log(ctx context.Context, userID, action, entityType, entityID string, detail map[string]any) error {
	if m.LogFn != nil {
		return m.LogFn(ctx, userID, action, entityType, entityID, detail)
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

// ---- mockGovernanceRepo ----

type mockGovernanceRepo struct {
	GetSettingsFn           func(ctx context.Context) (*model.GovernanceSettings, error)
	UpdateSettingsFn        func(ctx context.Context, s *model.GovernanceSettings) (*model.GovernanceSettings, error)
	ComputeQuorumFn         func(ctx context.Context, meetingID string) (*model.QuorumStatus, error)
	ListMotionsFn           func(ctx context.Context, meetingID string) ([]model.Motion, error)
	GetMotionFn             func(ctx context.Context, id string) (*model.Motion, error)
	CreateMotionFn          func(ctx context.Context, m *model.Motion, createdBy string) (*model.Motion, error)
	UpdateMotionFn          func(ctx context.Context, id string, title, detail *string, moverID, seconderID *string, threshold, business *string) (*model.Motion, error)
	SetMotionStatusFn       func(ctx context.Context, id, status string, seconderID *string) (*model.Motion, error)
	DeleteMotionFn          func(ctx context.Context, id string) error
	MotionStatusFn          func(ctx context.Context, id string) (string, string, error)
	CastVoteFn              func(ctx context.Context, motionID, memberID, choice string, isProxy bool, castBy string) error
	MemberIsActiveFn        func(ctx context.Context, memberID string) (bool, error)
	GetVotesFn              func(ctx context.Context, motionID string) ([]model.MotionVote, error)
	ListProxiesFn           func(ctx context.Context, meetingID string) ([]model.MeetingProxy, error)
	CreateProxyFn           func(ctx context.Context, meetingID, grantorID, holderID string) (*model.MeetingProxy, error)
	DeleteProxyFn           func(ctx context.Context, id string) error
	EligibleBallotMembersFn func(ctx context.Context, motionID string) ([]repo.BallotRecipient, error)
	UpsertBallotTokenFn     func(ctx context.Context, motionID, memberID, hash string, expiresAt time.Time) error
	GetBallotContextFn      func(ctx context.Context, hash string) (*model.BallotContext, error)
	ConsumeBallotAndVoteFn  func(ctx context.Context, hash, choice string) (string, error)
}

func (m *mockGovernanceRepo) EligibleBallotMembers(ctx context.Context, motionID string) ([]repo.BallotRecipient, error) {
	return m.EligibleBallotMembersFn(ctx, motionID)
}
func (m *mockGovernanceRepo) UpsertBallotToken(ctx context.Context, motionID, memberID, hash string, e time.Time) error {
	return m.UpsertBallotTokenFn(ctx, motionID, memberID, hash, e)
}
func (m *mockGovernanceRepo) GetBallotContext(ctx context.Context, hash string) (*model.BallotContext, error) {
	return m.GetBallotContextFn(ctx, hash)
}
func (m *mockGovernanceRepo) ConsumeBallotAndVote(ctx context.Context, hash, choice string) (string, error) {
	return m.ConsumeBallotAndVoteFn(ctx, hash, choice)
}

func (m *mockGovernanceRepo) GetSettings(ctx context.Context) (*model.GovernanceSettings, error) {
	if m.GetSettingsFn != nil {
		return m.GetSettingsFn(ctx)
	}
	return &model.GovernanceSettings{QuorumMode: "majority", DefaultThreshold: "majority", ProxiesCountTowardQuorum: true}, nil
}
func (m *mockGovernanceRepo) UpdateSettings(ctx context.Context, s *model.GovernanceSettings) (*model.GovernanceSettings, error) {
	if m.UpdateSettingsFn != nil {
		return m.UpdateSettingsFn(ctx, s)
	}
	return s, nil
}
func (m *mockGovernanceRepo) ComputeQuorum(ctx context.Context, meetingID string) (*model.QuorumStatus, error) {
	return m.ComputeQuorumFn(ctx, meetingID)
}
func (m *mockGovernanceRepo) ListMotions(ctx context.Context, meetingID string) ([]model.Motion, error) {
	return m.ListMotionsFn(ctx, meetingID)
}
func (m *mockGovernanceRepo) GetMotion(ctx context.Context, id string) (*model.Motion, error) {
	return m.GetMotionFn(ctx, id)
}
func (m *mockGovernanceRepo) CreateMotion(ctx context.Context, mo *model.Motion, createdBy string) (*model.Motion, error) {
	return m.CreateMotionFn(ctx, mo, createdBy)
}
func (m *mockGovernanceRepo) UpdateMotion(ctx context.Context, id string, title, detail *string, moverID, seconderID *string, threshold, business *string) (*model.Motion, error) {
	return m.UpdateMotionFn(ctx, id, title, detail, moverID, seconderID, threshold, business)
}
func (m *mockGovernanceRepo) SetMotionStatus(ctx context.Context, id, status string, seconderID *string) (*model.Motion, error) {
	return m.SetMotionStatusFn(ctx, id, status, seconderID)
}
func (m *mockGovernanceRepo) DeleteMotion(ctx context.Context, id string) error {
	return m.DeleteMotionFn(ctx, id)
}
func (m *mockGovernanceRepo) MotionStatus(ctx context.Context, id string) (string, string, error) {
	return m.MotionStatusFn(ctx, id)
}
func (m *mockGovernanceRepo) CastVote(ctx context.Context, motionID, memberID, choice string, isProxy bool, castBy string) error {
	return m.CastVoteFn(ctx, motionID, memberID, choice, isProxy, castBy)
}
func (m *mockGovernanceRepo) MemberIsActive(ctx context.Context, memberID string) (bool, error) {
	if m.MemberIsActiveFn != nil {
		return m.MemberIsActiveFn(ctx, memberID)
	}
	return true, nil // default: eligible, so existing tests are unaffected
}
func (m *mockGovernanceRepo) GetVotes(ctx context.Context, motionID string) ([]model.MotionVote, error) {
	return m.GetVotesFn(ctx, motionID)
}
func (m *mockGovernanceRepo) ListProxies(ctx context.Context, meetingID string) ([]model.MeetingProxy, error) {
	return m.ListProxiesFn(ctx, meetingID)
}
func (m *mockGovernanceRepo) CreateProxy(ctx context.Context, meetingID, grantorID, holderID string) (*model.MeetingProxy, error) {
	return m.CreateProxyFn(ctx, meetingID, grantorID, holderID)
}
func (m *mockGovernanceRepo) DeleteProxy(ctx context.Context, id string) error {
	return m.DeleteProxyFn(ctx, id)
}

// ---- mockBudgetRepo ----

type mockBudgetRepo struct {
	ListScenariosFn    func(ctx context.Context) ([]model.BudgetScenario, error)
	CompareScenariosFn func(ctx context.Context, ids []string) ([]model.BudgetScenario, error)
	GetScenarioFn      func(ctx context.Context, id string) (*model.BudgetScenario, error)
	CreateScenarioFn   func(ctx context.Context, s *model.BudgetScenario, createdBy string) (*model.BudgetScenario, error)
	UpdateScenarioFn   func(ctx context.Context, id string, name, description, periodLabel, status, currency *string) (*model.BudgetScenario, error)
	DeleteScenarioFn   func(ctx context.Context, id string) error
	CloneScenarioFn    func(ctx context.Context, id, newName, createdBy string) (*model.BudgetScenario, error)
	SeedDuesIncomeFn   func(ctx context.Context, scenarioID string) (int, error)
	AddLineFn          func(ctx context.Context, l *model.BudgetLine) (*model.BudgetLine, error)
	UpdateLineFn       func(ctx context.Context, id string, kind, category, label *string, quantity, unitAmountMinor *int64, note *string, sortOrder *int) (*model.BudgetLine, error)
	DeleteLineFn       func(ctx context.Context, id string) error
}

func (m *mockBudgetRepo) ListScenarios(ctx context.Context) ([]model.BudgetScenario, error) {
	return m.ListScenariosFn(ctx)
}
func (m *mockBudgetRepo) CompareScenarios(ctx context.Context, ids []string) ([]model.BudgetScenario, error) {
	return m.CompareScenariosFn(ctx, ids)
}
func (m *mockBudgetRepo) GetScenario(ctx context.Context, id string) (*model.BudgetScenario, error) {
	return m.GetScenarioFn(ctx, id)
}
func (m *mockBudgetRepo) CreateScenario(ctx context.Context, s *model.BudgetScenario, createdBy string) (*model.BudgetScenario, error) {
	return m.CreateScenarioFn(ctx, s, createdBy)
}
func (m *mockBudgetRepo) UpdateScenario(ctx context.Context, id string, name, description, periodLabel, status, currency *string) (*model.BudgetScenario, error) {
	return m.UpdateScenarioFn(ctx, id, name, description, periodLabel, status, currency)
}
func (m *mockBudgetRepo) DeleteScenario(ctx context.Context, id string) error {
	return m.DeleteScenarioFn(ctx, id)
}
func (m *mockBudgetRepo) CloneScenario(ctx context.Context, id, newName, createdBy string) (*model.BudgetScenario, error) {
	return m.CloneScenarioFn(ctx, id, newName, createdBy)
}
func (m *mockBudgetRepo) SeedDuesIncome(ctx context.Context, scenarioID string) (int, error) {
	return m.SeedDuesIncomeFn(ctx, scenarioID)
}
func (m *mockBudgetRepo) AddLine(ctx context.Context, l *model.BudgetLine) (*model.BudgetLine, error) {
	return m.AddLineFn(ctx, l)
}
func (m *mockBudgetRepo) UpdateLine(ctx context.Context, id string, kind, category, label *string, quantity, unitAmountMinor *int64, note *string, sortOrder *int) (*model.BudgetLine, error) {
	return m.UpdateLineFn(ctx, id, kind, category, label, quantity, unitAmountMinor, note, sortOrder)
}
func (m *mockBudgetRepo) DeleteLine(ctx context.Context, id string) error {
	return m.DeleteLineFn(ctx, id)
}

// ---- mockAnalyticsRepo ----

type mockAnalyticsRepo struct {
	OverviewFn   func(ctx context.Context) (*model.AnalyticsOverview, error)
	MembershipFn func(ctx context.Context) (*model.MembershipAnalytics, error)
	AttendanceFn func(ctx context.Context) (*model.AttendanceAnalytics, error)
	GovernanceFn func(ctx context.Context) (*model.GovernanceAnalytics, error)
	PaymentsFn   func(ctx context.Context) (*model.PaymentsAnalytics, error)
}

func (m *mockAnalyticsRepo) Overview(ctx context.Context) (*model.AnalyticsOverview, error) {
	return m.OverviewFn(ctx)
}
func (m *mockAnalyticsRepo) Membership(ctx context.Context) (*model.MembershipAnalytics, error) {
	return m.MembershipFn(ctx)
}
func (m *mockAnalyticsRepo) Attendance(ctx context.Context) (*model.AttendanceAnalytics, error) {
	return m.AttendanceFn(ctx)
}
func (m *mockAnalyticsRepo) Governance(ctx context.Context) (*model.GovernanceAnalytics, error) {
	return m.GovernanceFn(ctx)
}
func (m *mockAnalyticsRepo) Payments(ctx context.Context) (*model.PaymentsAnalytics, error) {
	return m.PaymentsFn(ctx)
}
