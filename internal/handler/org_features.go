package handler

import (
	"context"
	"net/http"
	"strings"

	"quorum/internal/model"
)

// orgFeaturesRepo is satisfied by *repo.OrgFeaturesRepo.
type orgFeaturesRepo interface {
	AddOfficeTerm(ctx context.Context, memberID, title, startedOn string) (*model.OfficeTerm, error)
	EndOfficeTerm(ctx context.Context, id, endedOn string) error
	ListOfficeTerms(ctx context.Context, current bool) ([]model.OfficeTerm, error)
	CreateCommittee(ctx context.Context, name string, purpose, chairID *string) (*model.Committee, error)
	GetCommittee(ctx context.Context, id string) (*model.Committee, error)
	ListCommittees(ctx context.Context) ([]model.Committee, error)
	UpdateCommittee(ctx context.Context, id, name string, purpose, chairID *string) error
	DeleteCommittee(ctx context.Context, id string) error
	SetCommitteeMembers(ctx context.Context, id string, memberIDs []string) error
	AddRecusal(ctx context.Context, subjectType, subjectID, memberID, reason string) error
	ListRecusals(ctx context.Context, subjectType, subjectID string) ([]model.Recusal, error)
	CreateJoinRequest(ctx context.Context, name, email, message string) error
	ListJoinRequests(ctx context.Context, status string, limit int) ([]model.JoinRequest, error)
	ApproveJoinRequest(ctx context.Context, id, tier, resolvedBy string) (string, error)
	RejectJoinRequest(ctx context.Context, id, resolvedBy string) error
}

// OrgFeaturesHandler serves office terms, committees, recusals, and the public
// membership-application flow. None of these grant access — they are records.
type OrgFeaturesHandler struct {
	repo orgFeaturesRepo
}

// NewOrgFeaturesHandler constructs the handler.
func NewOrgFeaturesHandler(r orgFeaturesRepo) *OrgFeaturesHandler {
	return &OrgFeaturesHandler{repo: r}
}

// ---- office terms ----

// ListOfficeTerms returns terms (?current=true for present holders). member+.
func (h *OrgFeaturesHandler) ListOfficeTerms(w http.ResponseWriter, r *http.Request) {
	terms, err := h.repo.ListOfficeTerms(r.Context(), r.URL.Query().Get("current") == "true")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, terms)
}

// AddOfficeTerm records a member taking an office (admin).
func (h *OrgFeaturesHandler) AddOfficeTerm(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MemberID  string `json:"member_id"`
		Title     string `json:"title"`
		StartedOn string `json:"started_on"`
	}
	if err := decodeJSON(r, &body); err != nil || !isValidUUID(body.MemberID) ||
		strings.TrimSpace(body.Title) == "" || len(body.Title) > 80 {
		writeError(w, http.StatusBadRequest, "member_id and title (≤80 chars) required", "bad_request")
		return
	}
	if body.StartedOn != "" && !validISODate(body.StartedOn) {
		writeError(w, http.StatusBadRequest, "started_on must be YYYY-MM-DD", "bad_request")
		return
	}
	t, err := h.repo.AddOfficeTerm(r.Context(), body.MemberID, strings.TrimSpace(body.Title), body.StartedOn)
	if err != nil {
		writeRepoError(w, err, "member not found", "create error")
		return
	}
	setAuditDetail(r, map[string]any{"member": t.MemberName, "title": t.Title})
	writeJSON(w, http.StatusCreated, t)
}

// EndOfficeTerm closes an open term (admin).
func (h *OrgFeaturesHandler) EndOfficeTerm(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		EndedOn string `json:"ended_on"`
	}
	_ = decodeJSON(r, &body)
	if body.EndedOn != "" && !validISODate(body.EndedOn) {
		writeError(w, http.StatusBadRequest, "ended_on must be YYYY-MM-DD", "bad_request")
		return
	}
	if err := h.repo.EndOfficeTerm(r.Context(), id, body.EndedOn); err != nil {
		writeError(w, http.StatusInternalServerError, "update error", "internal_error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- committees ----

// ListCommittees (member+).
func (h *OrgFeaturesHandler) ListCommittees(w http.ResponseWriter, r *http.Request) {
	cs, err := h.repo.ListCommittees(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

// GetCommittee (member+).
func (h *OrgFeaturesHandler) GetCommittee(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	c, err := h.repo.GetCommittee(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "committee not found", "not_found")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

type committeeBody struct {
	Name    string  `json:"name"`
	Purpose *string `json:"purpose"`
	ChairID *string `json:"chair_id"`
}

func (b committeeBody) valid() bool {
	if strings.TrimSpace(b.Name) == "" || len(b.Name) > 80 {
		return false
	}
	if b.ChairID != nil && *b.ChairID != "" && !isValidUUID(*b.ChairID) {
		return false
	}
	return true
}

// CreateCommittee (admin).
func (h *OrgFeaturesHandler) CreateCommittee(w http.ResponseWriter, r *http.Request) {
	var body committeeBody
	if err := decodeJSON(r, &body); err != nil || !body.valid() {
		writeError(w, http.StatusBadRequest, "name (≤80 chars) required; chair_id must be a UUID", "bad_request")
		return
	}
	c, err := h.repo.CreateCommittee(r.Context(), strings.TrimSpace(body.Name), body.Purpose, body.ChairID)
	if err != nil {
		writeRepoError(w, err, "", "create error")
		return
	}
	setAuditDetail(r, map[string]any{"committee": c.Name})
	writeJSON(w, http.StatusCreated, c)
}

// UpdateCommittee (admin).
func (h *OrgFeaturesHandler) UpdateCommittee(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body committeeBody
	if err := decodeJSON(r, &body); err != nil || !body.valid() {
		writeError(w, http.StatusBadRequest, "name (≤80 chars) required; chair_id must be a UUID", "bad_request")
		return
	}
	if err := h.repo.UpdateCommittee(r.Context(), id, strings.TrimSpace(body.Name), body.Purpose, body.ChairID); err != nil {
		writeRepoError(w, err, "committee not found", "update error")
		return
	}
	c, _ := h.repo.GetCommittee(r.Context(), id)
	writeJSON(w, http.StatusOK, c)
}

// DeleteCommittee (admin).
func (h *OrgFeaturesHandler) DeleteCommittee(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	if err := h.repo.DeleteCommittee(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "delete error", "internal_error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetCommitteeMembers replaces the roster (admin).
func (h *OrgFeaturesHandler) SetCommitteeMembers(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		MemberIDs []string `json:"member_ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	for _, mid := range body.MemberIDs {
		if !isValidUUID(mid) {
			writeError(w, http.StatusBadRequest, "member_ids must be UUIDs", "bad_request")
			return
		}
	}
	if err := h.repo.SetCommitteeMembers(r.Context(), id, body.MemberIDs); err != nil {
		writeRepoError(w, err, "committee or member not found", "update error")
		return
	}
	c, _ := h.repo.GetCommittee(r.Context(), id)
	writeJSON(w, http.StatusOK, c)
}

// ---- recusals ----

var validRecusalSubject = map[string]bool{"motion": true, "purchase": true}

// ListRecusals for a subject (member+). ?type=motion|purchase.
func (h *OrgFeaturesHandler) ListRecusals(w http.ResponseWriter, r *http.Request) {
	subjectType := r.URL.Query().Get("type")
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	if !validRecusalSubject[subjectType] {
		writeError(w, http.StatusBadRequest, "type must be motion or purchase", "bad_request")
		return
	}
	rs, err := h.repo.ListRecusals(r.Context(), subjectType, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, rs)
}

// AddRecusal records the caller recusing from a motion/purchase (member+ with a
// linked member record). Body: {type, reason}.
func (h *OrgFeaturesHandler) AddRecusal(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	memberID := memberIDFromCtx(r)
	if memberID == "" {
		writeError(w, http.StatusForbidden, "your login isn't linked to a member record", "forbidden")
		return
	}
	var body struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	}
	if err := decodeJSON(r, &body); err != nil || !validRecusalSubject[body.Type] || len(body.Reason) > 500 {
		writeError(w, http.StatusBadRequest, "type (motion|purchase) required; reason ≤500 chars", "bad_request")
		return
	}
	if err := h.repo.AddRecusal(r.Context(), body.Type, id, memberID, strings.TrimSpace(body.Reason)); err != nil {
		writeRepoError(w, err, "subject not found", "create error")
		return
	}
	setAuditDetail(r, map[string]any{"subject_type": body.Type, "subject_id": id})
	w.WriteHeader(http.StatusNoContent)
}

// ---- join requests ----

// CreateJoinRequest is PUBLIC: anyone can apply to join.
func (h *OrgFeaturesHandler) CreateJoinRequest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		Email   string `json:"email"`
		Message string `json:"message"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	body.Email = strings.TrimSpace(body.Email)
	if body.Name == "" || len(body.Name) > 200 || !strings.Contains(body.Email, "@") ||
		len(body.Email) > 200 || len(body.Message) > 1000 {
		writeError(w, http.StatusBadRequest, "name and a valid email are required", "bad_request")
		return
	}
	if err := h.repo.CreateJoinRequest(r.Context(), body.Name, body.Email, strings.TrimSpace(body.Message)); err != nil {
		writeError(w, http.StatusInternalServerError, "could not submit", "internal_error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "submitted"})
}

// ListJoinRequests returns the application queue (officer+). ?status=.
func (h *OrgFeaturesHandler) ListJoinRequests(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && status != "pending" && status != "approved" && status != "rejected" {
		writeError(w, http.StatusBadRequest, "status must be pending, approved, or rejected", "bad_request")
		return
	}
	list, err := h.repo.ListJoinRequests(r.Context(), status, clampLimit(r.URL.Query().Get("limit"), 100))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// ApproveJoinRequest creates a member from an application (officer+).
func (h *OrgFeaturesHandler) ApproveJoinRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Tier string `json:"tier"`
	}
	_ = decodeJSON(r, &body)
	memberID, err := h.repo.ApproveJoinRequest(r.Context(), id, strings.TrimSpace(body.Tier), userIDFromCtx(r))
	if err != nil {
		writeRepoError(w, err, "request not found or already resolved", "approve error")
		return
	}
	setAuditDetail(r, map[string]any{"join_request": id, "member": memberID})
	writeJSON(w, http.StatusOK, map[string]string{"member_id": memberID})
}

// RejectJoinRequest declines an application (officer+).
func (h *OrgFeaturesHandler) RejectJoinRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	if err := h.repo.RejectJoinRequest(r.Context(), id, userIDFromCtx(r)); err != nil {
		writeError(w, http.StatusInternalServerError, "reject error", "internal_error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
