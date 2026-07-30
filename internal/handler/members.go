package handler

import (
	"net/http"
	"strconv"
	"time"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// MembersHandler handles member CRUD and related sub-resource endpoints.
type MembersHandler struct {
	repo        membersRepo
	actionItems actionItemsRepo
	dues        duesRepo
}

// NewMembersHandler constructs a MembersHandler.
func NewMembersHandler(r membersRepo, ai actionItemsRepo, d duesRepo) *MembersHandler {
	return &MembersHandler{repo: r, actionItems: ai, dues: d}
}

// List handles GET requests for a paginated, filterable list of members.
func (h *MembersHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := repo.MemberFilter{
		Search: q.Get("search"),
		Status: q.Get("status"),
		Tier:   q.Get("tier"),
		Limit:  50,
	}
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= maxPageSize {
		f.Limit = v
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil && v >= 0 {
		f.Offset = v
	}
	members, total, err := h.repo.List(r.Context(), f)
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	writePage(w, members, total, f.Limit, f.Offset)
}

// Create handles creating a member.
func (h *MembersHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DisplayName string         `json:"display_name"`
		Email       *string        `json:"email"`
		Phone       *string        `json:"phone"`
		Address     *string        `json:"address"`
		Tier        string         `json:"tier"`
		Status      string         `json:"status"`
		JoinedAt    *string        `json:"joined_at"`
		Notes       *string        `json:"notes"`
		Metadata    map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return
	}
	if body.DisplayName == "" {
		writeError(w, 400, "display_name is required", "bad_request")
		return
	}

	tier := body.Tier
	if tier == "" {
		tier = "standard"
	}
	status := body.Status
	if status == "" {
		status = "active"
	}
	if status != "active" && status != "inactive" && status != "suspended" {
		writeError(w, 400, "invalid status: must be active, inactive, or suspended", "bad_request")
		return
	}
	joinedAt := time.Now()
	if body.JoinedAt != nil {
		t, err := time.Parse("2006-01-02", *body.JoinedAt)
		if err != nil {
			writeError(w, 400, "joined_at must be YYYY-MM-DD", "bad_request")
			return
		}
		joinedAt = t
	}

	m := &model.Member{
		DisplayName: body.DisplayName,
		Email:       body.Email,
		Phone:       body.Phone,
		Address:     body.Address,
		Tier:        tier,
		Status:      status,
		JoinedAt:    joinedAt,
		Notes:       body.Notes,
		Metadata:    body.Metadata,
	}
	created, err := h.repo.Create(r.Context(), m)
	if err != nil {
		writeRepoError(w, err, "", "create error")
		return
	}
	writeJSON(w, 201, created)
}

// Get handles fetching a single member by id.
func (h *MembersHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	if !canViewMemberScoped(w, r, id) {
		return
	}
	member, err := h.repo.Get(r.Context(), id)
	if err != nil {
		writeError(w, 404, "member not found", "not_found")
		return
	}
	writeJSON(w, 200, member)
}

// Update handles updating a member.
func (h *MembersHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body map[string]any
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return
	}
	allowed := map[string]bool{
		"display_name": true, "email": true, "phone": true, "address": true,
		"tier": true, "status": true, "joined_at": true, "notes": true, "metadata": true,
	}
	fields := map[string]any{}
	for k, v := range body {
		if allowed[k] {
			fields[k] = v
		}
	}
	if len(fields) == 0 {
		writeError(w, 400, "no valid fields provided", "bad_request")
		return
	}
	if status, present := fields["status"]; present {
		s, ok := status.(string)
		if !ok || (s != "active" && s != "inactive" && s != "suspended") {
			writeError(w, 400, "invalid status: must be active, inactive, or suspended", "bad_request")
			return
		}
	}
	if joinedAt, present := fields["joined_at"]; present {
		s, ok := joinedAt.(string)
		if !ok {
			writeError(w, 400, "joined_at must be YYYY-MM-DD", "bad_request")
			return
		}
		if _, err := time.Parse("2006-01-02", s); err != nil {
			writeError(w, 400, "joined_at must be YYYY-MM-DD", "bad_request")
			return
		}
	}
	updated, err := h.repo.Update(r.Context(), id, fields)
	if err != nil {
		writeRepoError(w, err, "member not found", "update error")
		return
	}
	writeJSON(w, 200, updated)
}

// Delete soft-deletes a member (sets status to inactive). It requires the same
// type-to-confirm gate as every other destructive action: the caller must echo
// the member's display name via ?confirm=<name>.
func (h *MembersHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	m, err := h.repo.Get(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "member not found", "query error")
		return
	}
	if !confirmMatches(w, r, m.DisplayName) {
		return
	}
	if err := h.repo.Delete(r.Context(), id); err != nil {
		writeRepoError(w, err, "member not found", "delete error")
		return
	}
	w.WriteHeader(204)
}

// GetDues returns a member's invoices (ownership-scoped for restricted users).
func (h *MembersHandler) GetDues(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	if !canViewMemberScoped(w, r, id) {
		return
	}
	q := r.URL.Query()
	f := repo.InvoiceFilter{MemberID: id, Limit: 50}
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= maxPageSize {
		f.Limit = v
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil && v >= 0 {
		f.Offset = v
	}
	invoices, total, err := h.dues.ListInvoices(r.Context(), f)
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	writePage(w, invoices, total, f.Limit, f.Offset)
}

// GetActionItems returns a member's assigned action items (ownership-scoped).
func (h *MembersHandler) GetActionItems(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	if !canViewMemberScoped(w, r, id) {
		return
	}
	q := r.URL.Query()
	f := repo.ActionItemFilter{AssigneeID: id, Limit: 50}
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= maxPageSize {
		f.Limit = v
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil && v >= 0 {
		f.Offset = v
	}
	items, total, err := h.actionItems.List(r.Context(), f)
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	writePage(w, items, total, f.Limit, f.Offset)
}

// Erase fulfils a right-to-erasure ("right to be forgotten") request: it strips
// the member's personal data in place, keeping the financial and governance
// records that reference them intact and consistent. Superadmin only, and gated
// on ?confirm= matching the member's display name like the other destructive
// actions, because it cannot be undone.
func (h *MembersHandler) Erase(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	member, err := h.repo.Get(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "member not found", "query error")
		return
	}
	if !confirmMatches(w, r, member.DisplayName) {
		return
	}
	if err := h.repo.Erase(r.Context(), id); err != nil {
		writeRepoError(w, err, "member not found", "erase error")
		return
	}
	// The AuditMiddleware records who erased which member id; that entry is the
	// durable evidence the request was fulfilled.
	w.WriteHeader(http.StatusNoContent)
}
