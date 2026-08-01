package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	"quorum/internal/model"
)

// sprintsRepo is satisfied by *repo.SprintsRepo.
type sprintsRepo interface {
	List(ctx context.Context) ([]model.Sprint, error)
	Get(ctx context.Context, id string) (*model.Sprint, error)
	Create(ctx context.Context, s *model.Sprint, createdBy string) (*model.Sprint, error)
	Update(ctx context.Context, id string, name, goal, startsOn, endsOn, status *string) (*model.Sprint, error)
	Delete(ctx context.Context, id string) error
}

// SprintsHandler manages sprints: the time-boxes the board scopes work into.
type SprintsHandler struct {
	repo sprintsRepo
}

// NewSprintsHandler constructs a SprintsHandler.
func NewSprintsHandler(r sprintsRepo) *SprintsHandler {
	return &SprintsHandler{repo: r}
}

// List returns all sprints (member+).
func (h *SprintsHandler) List(w http.ResponseWriter, r *http.Request) {
	sprints, err := h.repo.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	if sprints == nil {
		sprints = []model.Sprint{}
	}
	writeJSON(w, http.StatusOK, sprints)
}

func validSprintDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// Create adds a sprint (officer+).
func (h *SprintsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string  `json:"name"`
		Goal     *string `json:"goal"`
		StartsOn string  `json:"starts_on"`
		EndsOn   string  `json:"ends_on"`
		Status   string  `json:"status"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required", "bad_request")
		return
	}
	if !validSprintDate(body.StartsOn) || !validSprintDate(body.EndsOn) {
		writeError(w, http.StatusBadRequest, "starts_on and ends_on must be YYYY-MM-DD", "bad_request")
		return
	}
	if body.EndsOn < body.StartsOn {
		writeError(w, http.StatusBadRequest, "ends_on must not be before starts_on", "bad_request")
		return
	}
	if body.Status == "" {
		body.Status = "planned"
	}
	if !model.ValidSprintStatuses[body.Status] {
		writeError(w, http.StatusBadRequest, "status must be planned, active, or completed", "bad_request")
		return
	}
	created, err := h.repo.Create(r.Context(), &model.Sprint{
		Name: body.Name, Goal: body.Goal,
		StartsOn: body.StartsOn, EndsOn: body.EndsOn, Status: body.Status,
	}, userIDFromCtx(r))
	if err != nil {
		writeRepoError(w, err, "", "create error")
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// Update patches a sprint (officer+).
func (h *SprintsHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Name     *string `json:"name"`
		Goal     *string `json:"goal"`
		StartsOn *string `json:"starts_on"`
		EndsOn   *string `json:"ends_on"`
		Status   *string `json:"status"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	if body.Name != nil && strings.TrimSpace(*body.Name) == "" {
		writeError(w, http.StatusBadRequest, "name must not be blank", "bad_request")
		return
	}
	for _, d := range []*string{body.StartsOn, body.EndsOn} {
		if d != nil && !validSprintDate(*d) {
			writeError(w, http.StatusBadRequest, "dates must be YYYY-MM-DD", "bad_request")
			return
		}
	}
	if body.Status != nil && !model.ValidSprintStatuses[*body.Status] {
		writeError(w, http.StatusBadRequest, "status must be planned, active, or completed", "bad_request")
		return
	}
	updated, err := h.repo.Update(r.Context(), id, body.Name, body.Goal, body.StartsOn, body.EndsOn, body.Status)
	if err != nil {
		// The ends_on >= starts_on CHECK arbitrates cross-field validity when
		// only one side changes.
		writeRepoError(w, err, "sprint not found", "update error")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// Delete removes a sprint; its items return to the backlog (officer+).
func (h *SprintsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	sp, err := h.repo.Get(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "sprint not found", "query error")
		return
	}
	if !confirmMatches(w, r, sp.Name) {
		return
	}
	if err := h.repo.Delete(r.Context(), id); err != nil {
		writeRepoError(w, err, "sprint not found", "delete error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
