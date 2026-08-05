package handler

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"

	"quorum/internal/model"
	"quorum/internal/pdf"
	"quorum/internal/repo"
)

// cardLinksRepo is satisfied by *repo.CardLinksRepo.
type cardLinksRepo interface {
	ListForCard(ctx context.Context, cardID string) ([]model.CardLink, error)
	Create(ctx context.Context, fromID, toID, kind, createdBy string) (*model.CardLink, error)
	Delete(ctx context.Context, id string) error
	SprintAnalytics(ctx context.Context, sprintID string) (*model.SprintAnalytics, error)
}

// CardLinksHandler manages typed relationships between cards and the
// sprint-analytics read they power.
type CardLinksHandler struct {
	repo    cardLinksRepo
	items   actionItemsRepo
	sprints sprintsRepo
}

// NewCardLinksHandler constructs the handler.
func NewCardLinksHandler(r cardLinksRepo, items actionItemsRepo, sprints sprintsRepo) *CardLinksHandler {
	return &CardLinksHandler{repo: r, items: items, sprints: sprints}
}

var validLinkKind = map[string]bool{"depends_on": true, "blocked_by": true, "related_to": true}

// List returns every relationship touching a card (member+).
func (h *CardLinksHandler) List(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	links, err := h.repo.ListForCard(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	if links == nil {
		links = []model.CardLink{}
	}
	writeJSON(w, http.StatusOK, links)
}

// Create links this card to another (officer+): body {to_id, kind}.
func (h *CardLinksHandler) Create(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		ToID string `json:"to_id"`
		Kind string `json:"kind"`
	}
	if err := decodeJSON(r, &body); err != nil || !isValidUUID(body.ToID) || !validLinkKind[body.Kind] {
		writeError(w, http.StatusBadRequest,
			"to_id (uuid) and kind (depends_on|blocked_by|related_to) required", "bad_request")
		return
	}
	if body.ToID == id {
		writeError(w, http.StatusBadRequest, "a card cannot link to itself", "bad_request")
		return
	}
	if _, err := h.items.Get(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "work item not found", "not_found")
		return
	}
	if _, err := h.items.Get(r.Context(), body.ToID); err != nil {
		writeError(w, http.StatusNotFound, "linked work item not found", "not_found")
		return
	}
	created, err := h.repo.Create(r.Context(), id, body.ToID, body.Kind, userIDFromCtx(r))
	if err != nil {
		writeRepoError(w, err, "work item not found", "create error")
		return
	}
	setAuditDetail(r, map[string]any{"kind": created.Kind, "from": created.FromTitle, "to": created.ToTitle})
	writeJSON(w, http.StatusCreated, created)
}

// Delete removes a relationship (officer+).
func (h *CardLinksHandler) Delete(w http.ResponseWriter, r *http.Request) {
	lid := chi.URLParam(r, "lid")
	if !isValidUUID(lid) {
		writeError(w, http.StatusBadRequest, "invalid link id", "bad_request")
		return
	}
	if err := h.repo.Delete(r.Context(), lid); err != nil {
		writeRepoError(w, err, "link not found", "delete error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Analytics returns a sprint's health picture (member+).
func (h *CardLinksHandler) Analytics(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	sp, err := h.sprints.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "sprint not found", "not_found")
		return
	}
	a, err := h.repo.SprintAnalytics(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	a.Sprint = *sp
	writeJSON(w, http.StatusOK, a)
}

// ---- sprint report PDF (lives on ReportsHandler siblings' pattern but the
// data deps are board-side, so it stays here) ----

// SprintReportHandler produces the exportable sprint analytics PDF with the
// standard export controls: exporter watermark, embedded SHA-256 integrity
// seal, and an EXPORT audit entry carrying the digest.
type SprintReportHandler struct {
	links   cardLinksRepo
	items   actionItemsRepo
	sprints sprintsRepo
	audit   auditRepo
	users   exporterLookup
}

// NewSprintReportHandler constructs the handler.
func NewSprintReportHandler(l cardLinksRepo, items actionItemsRepo, sprints sprintsRepo, a auditRepo, u exporterLookup) *SprintReportHandler {
	return &SprintReportHandler{links: l, items: items, sprints: sprints, audit: a, users: u}
}

// PDF renders the sprint report (officer+).
func (h *SprintReportHandler) PDF(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	sp, err := h.sprints.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "sprint not found", "not_found")
		return
	}
	a, err := h.links.SprintAnalytics(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	cards, _, err := h.items.List(r.Context(), repo.ActionItemFilter{SprintID: id, Limit: 500})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}

	d := pdf.New("Sprint report")
	d.Title("Sprint report — " + sp.Name)
	d.Line(fmt.Sprintf("%s to %s - status %s", sp.StartsOn, sp.EndsOn, sp.Status))
	if sp.Goal != nil && *sp.Goal != "" {
		d.Line("Goal: " + *sp.Goal)
	}
	d.Space()

	d.Heading("Totals")
	pct := 0
	if a.Points > 0 {
		pct = a.DonePoints * 100 / a.Points
	} else if a.Cards > 0 {
		pct = a.DoneCards * 100 / a.Cards
	}
	d.Line(fmt.Sprintf("Cards: %d (%d done, %d cancelled) - Points: %d (%d done) - %d%% complete",
		a.Cards, a.DoneCards, a.CancelledCards, a.Points, a.DonePoints, pct))
	d.Line(fmt.Sprintf("Blocked cards: %d - Unpointed cards: %d", a.BlockedCards, a.UnpointedCards))
	d.Space()

	section := func(title string, rows []model.SprintBucket) {
		d.Heading(title)
		for _, b := range rows {
			d.Line(fmt.Sprintf("%-24s %3d cards / %3d pts   done: %d cards / %d pts",
				b.Key, b.Cards, b.Points, b.DoneCards, b.DonePoints))
		}
		if len(rows) == 0 {
			d.Line("(none)")
		}
		d.Space()
	}
	section("By type", a.ByType)
	section("By status", a.ByStatus)
	section("By assignee", a.ByAssignee)

	d.Heading("Cards")
	sorted := append([]model.ActionItem(nil), cards...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].CardType < sorted[j].CardType })
	for _, c := range sorted {
		pts := "-"
		if c.StoryPoints != nil {
			pts = fmt.Sprintf("%d pts", *c.StoryPoints)
		}
		who := "unassigned"
		if c.AssigneeName != nil {
			who = *c.AssigneeName
		}
		d.Line(fmt.Sprintf("[%s] %s - %s - %s - %s", c.CardType, c.Title, c.Status, pts, who))
		if c.ParentTitle != nil {
			d.Indented("in: " + *c.ParentTitle)
		}
	}
	if len(sorted) == 0 {
		d.Line("(no cards)")
	}

	// Standard export controls: watermark + integrity seal + audit entry.
	who := userIDFromCtx(r)
	if h.users != nil {
		if u, err := h.users.GetUserByID(r.Context(), who); err == nil && u.Email != "" {
			who = u.Email
		}
	}
	d.Watermark(fmt.Sprintf("Exported by %s - %s", who, time.Now().UTC().Format("2006-01-02 15:04 UTC")))
	data, digest := d.Finalize()
	auditExport(r, h.audit, "sprint-report.pdf", map[string]any{
		"sprint": sp.Name, "cards": a.Cards, "sha256": digest,
	})
	servePDF(w, data, digest, "sprint-report.pdf")
}
