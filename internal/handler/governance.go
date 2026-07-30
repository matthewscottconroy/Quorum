package handler

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"quorum/internal/auth"
	"quorum/internal/config"
	"quorum/internal/model"
	"quorum/internal/repo"
)

// ballotTokenTTL bounds how long an emailed ballot link stays valid.
const ballotTokenTTL = 14 * 24 * time.Hour

// GovernanceHandler handles quorum settings, motions, voting, proxies, and
// emailed async ballots.
type GovernanceHandler struct {
	repo     governanceRepo
	cfg      *config.Config
	mailer   mailer
	notifier eventNotifier
}

// SetEventNotifier attaches the notification service used to alert members when
// a motion opens for voting or is decided. Optional (no-op when unset).
func (h *GovernanceHandler) SetEventNotifier(n eventNotifier) { h.notifier = n }

// notify emits an event notification if a notifier is attached.
func (h *GovernanceHandler) notify(notifType, title string, body, link *string) {
	if h.notifier != nil {
		h.notifier.NotifyMembers(notifType, title, body, link)
	}
}

// NewGovernanceHandler constructs a GovernanceHandler.
func NewGovernanceHandler(r governanceRepo, cfg *config.Config) *GovernanceHandler {
	return &GovernanceHandler{repo: r, cfg: cfg}
}

// SetMailer attaches the email service used to deliver ballot links.
func (h *GovernanceHandler) SetMailer(m mailer) { h.mailer = m }

// terminalMotionStatus reports whether a motion status is final (no more edits/votes).
func terminalMotionStatus(s string) bool {
	switch s {
	case "carried", "failed", "tabled", "withdrawn":
		return true
	}
	return false
}

// ---- Settings ----

// GetSettings returns the org-wide governance settings (member+).
func (h *GovernanceHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	s, err := h.repo.GetSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// UpdateSettings writes the governance settings (admin+).
func (h *GovernanceHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		QuorumMode               string `json:"quorum_mode"`
		QuorumValue              int    `json:"quorum_value"`
		ProxiesCountTowardQuorum bool   `json:"proxies_count_toward_quorum"`
		DefaultThreshold         string `json:"default_threshold"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	if !model.ValidQuorumModes[body.QuorumMode] {
		writeError(w, http.StatusBadRequest, "quorum_mode must be majority, percent, or fixed", "bad_request")
		return
	}
	if !model.ValidThresholds[body.DefaultThreshold] {
		writeError(w, http.StatusBadRequest, "default_threshold must be majority, two_thirds, or unanimous", "bad_request")
		return
	}
	if body.QuorumValue < 0 || (body.QuorumMode == "percent" && (body.QuorumValue < 1 || body.QuorumValue > 100)) {
		writeError(w, http.StatusBadRequest, "quorum_value out of range (1–100 for percent)", "bad_request")
		return
	}
	out, err := h.repo.UpdateSettings(r.Context(), &model.GovernanceSettings{
		QuorumMode:               body.QuorumMode,
		QuorumValue:              body.QuorumValue,
		ProxiesCountTowardQuorum: body.ProxiesCountTowardQuorum,
		DefaultThreshold:         body.DefaultThreshold,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- Quorum ----

// Quorum returns the live quorum status for a meeting (member+).
func (h *GovernanceHandler) Quorum(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	q, err := h.repo.ComputeQuorum(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "quorum computation failed", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, q)
}

// ---- Motions ----

// ListMotions returns all motions for a meeting with tallies (member+).
func (h *GovernanceHandler) ListMotions(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	motions, err := h.repo.ListMotions(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	if motions == nil {
		motions = []model.Motion{}
	}
	writeJSON(w, http.StatusOK, motions)
}

// GetMotion returns one motion with its ballots (member+).
func (h *GovernanceHandler) GetMotion(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	m, err := h.repo.GetMotion(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "motion not found", "query error")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// CreateMotion adds a draft motion to a meeting (officer+).
func (h *GovernanceHandler) CreateMotion(w http.ResponseWriter, r *http.Request) {
	meetingID, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Title      string  `json:"title"`
		Detail     *string `json:"detail"`
		MoverID    *string `json:"mover_id"`
		SeconderID *string `json:"seconder_id"`
		Threshold  string  `json:"threshold"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	if body.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required", "bad_request")
		return
	}
	if body.Threshold == "" {
		// Default to the org's configured threshold.
		if s, err := h.repo.GetSettings(r.Context()); err == nil {
			body.Threshold = s.DefaultThreshold
		} else {
			body.Threshold = "majority"
		}
	}
	if !model.ValidThresholds[body.Threshold] {
		writeError(w, http.StatusBadRequest, "invalid threshold", "bad_request")
		return
	}
	if !validOptionalUUID(body.MoverID) || !validOptionalUUID(body.SeconderID) {
		writeError(w, http.StatusBadRequest, "mover_id/seconder_id must be UUIDs", "bad_request")
		return
	}
	status := "draft"
	if body.SeconderID != nil {
		status = "seconded"
	}
	m, err := h.repo.CreateMotion(r.Context(), &model.Motion{
		MeetingID:  meetingID,
		Title:      body.Title,
		Detail:     body.Detail,
		MoverID:    body.MoverID,
		SeconderID: body.SeconderID,
		Threshold:  body.Threshold,
		Status:     status,
	}, userIDFromCtx(r))
	if err != nil {
		if isFKViolation(err) {
			writeError(w, http.StatusBadRequest, "meeting, mover, or seconder does not exist", "bad_request")
			return
		}
		writeError(w, http.StatusInternalServerError, "create error", "internal_error")
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

// UpdateMotion edits a non-terminal motion (officer+).
func (h *GovernanceHandler) UpdateMotion(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	status, _, err := h.repo.MotionStatus(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "motion not found", "query error")
		return
	}
	if terminalMotionStatus(status) {
		writeError(w, http.StatusConflict, "a decided motion can no longer be edited", "conflict")
		return
	}
	var body struct {
		Title      *string `json:"title"`
		Detail     *string `json:"detail"`
		MoverID    *string `json:"mover_id"`
		SeconderID *string `json:"seconder_id"`
		Threshold  *string `json:"threshold"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	if body.Threshold != nil {
		if !model.ValidThresholds[*body.Threshold] {
			writeError(w, http.StatusBadRequest, "invalid threshold", "bad_request")
			return
		}
		// Changing the passing bar once voting is open would retroactively flip
		// the meaning of ballots already cast — forbid it.
		if status == "open" {
			writeError(w, http.StatusConflict, "the threshold cannot be changed once voting is open", "conflict")
			return
		}
	}
	if !validOptionalUUID(body.MoverID) || !validOptionalUUID(body.SeconderID) {
		writeError(w, http.StatusBadRequest, "mover_id/seconder_id must be UUIDs", "bad_request")
		return
	}
	m, err := h.repo.UpdateMotion(r.Context(), id, body.Title, body.Detail, body.MoverID, body.SeconderID, body.Threshold)
	if err != nil {
		if isFKViolation(err) {
			writeError(w, http.StatusBadRequest, "mover or seconder does not exist", "bad_request")
			return
		}
		writeRepoError(w, err, "motion not found", "update error")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// SecondMotion records a seconder and advances the motion to "seconded" (officer+).
func (h *GovernanceHandler) SecondMotion(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		SeconderID string `json:"seconder_id"`
	}
	if err := decodeJSON(r, &body); err != nil || !isValidUUID(body.SeconderID) {
		writeError(w, http.StatusBadRequest, "seconder_id (a member UUID) is required", "bad_request")
		return
	}
	status, _, err := h.repo.MotionStatus(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "motion not found", "query error")
		return
	}
	if status != "draft" && status != "seconded" {
		writeError(w, http.StatusConflict, "only a draft motion can be seconded", "conflict")
		return
	}
	sid := body.SeconderID
	m, err := h.repo.SetMotionStatus(r.Context(), id, "seconded", &sid)
	if err != nil {
		if isFKViolation(err) {
			writeError(w, http.StatusBadRequest, "seconder does not exist", "bad_request")
			return
		}
		writeRepoError(w, err, "motion not found", "update error")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// OpenMotion opens voting on a motion (officer+). Requires a seconder on record.
func (h *GovernanceHandler) OpenMotion(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	m, err := h.repo.GetMotion(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "motion not found", "query error")
		return
	}
	if terminalMotionStatus(m.Status) || m.Status == "open" {
		writeError(w, http.StatusConflict, "motion is not in a state that can be opened", "conflict")
		return
	}
	if m.SeconderID == nil {
		writeError(w, http.StatusBadRequest, "a motion must be seconded before voting opens", "bad_request")
		return
	}
	out, err := h.repo.SetMotionStatus(r.Context(), id, "open", nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update error", "internal_error")
		return
	}
	link := "#/meetings"
	body := "Voting is now open. Cast your ballot in Quorum."
	h.notify("motion.opened", "Motion open for voting: "+out.Title, &body, &link)
	writeJSON(w, http.StatusOK, out)
}

// CloseMotion ends voting. With no body it auto-decides carried/failed from the
// tally and threshold; a body of {"status":"tabled"|"withdrawn"} closes without
// a decision (officer+).
func (h *GovernanceHandler) CloseMotion(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	_ = decodeJSON(r, &body) // optional

	m, err := h.repo.GetMotion(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "motion not found", "query error")
		return
	}
	if terminalMotionStatus(m.Status) {
		writeError(w, http.StatusConflict, "motion is already decided", "conflict")
		return
	}

	final := body.Status
	switch final {
	case "tabled", "withdrawn":
		// explicit non-decision close — allowed from any non-terminal state.
	case "", "carried", "failed":
		// auto-decide from the tally — only a motion whose vote is actually open
		// can be decided, otherwise a never-opened draft would be recorded failed.
		if m.Status != "open" {
			writeError(w, http.StatusConflict, "voting must be open before a motion can be decided", "conflict")
			return
		}
		if m.Tally.Carried {
			final = "carried"
		} else {
			final = "failed"
		}
	default:
		writeError(w, http.StatusBadRequest, "status must be tabled or withdrawn (or omitted to auto-decide)", "bad_request")
		return
	}
	out, err := h.repo.SetMotionStatus(r.Context(), id, final, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update error", "internal_error")
		return
	}
	// Notify members only of an actual decision, not a table/withdraw.
	if final == "carried" || final == "failed" {
		link := "#/meetings"
		h.notify("motion."+final, "Motion "+final+": "+out.Title, nil, &link)
	}
	writeJSON(w, http.StatusOK, out)
}

// DeleteMotion removes a motion (officer+).
func (h *GovernanceHandler) DeleteMotion(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	// A decided motion is governance history: deleting it (votes cascade away
	// with it) would falsify the record the same way editing it would, and
	// UpdateMotion already refuses that. Drafts and still-open motions can go.
	m, err := h.repo.GetMotion(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "motion not found", "query error")
		return
	}
	if terminalMotionStatus(m.Status) {
		writeError(w, http.StatusConflict,
			"a decided motion is part of the governance record and cannot be deleted", "conflict")
		return
	}
	if err := h.repo.DeleteMotion(r.Context(), id); err != nil {
		writeRepoError(w, err, "motion not found", "delete error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Voting ----

// CastVote lets an authenticated member cast their own ballot on an open motion.
func (h *GovernanceHandler) CastVote(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	memberID := memberIDFromCtx(r)
	if memberID == "" {
		writeError(w, http.StatusBadRequest, "your account is not linked to a member record, so you cannot vote", "bad_request")
		return
	}
	var body struct {
		Choice string `json:"choice"`
	}
	if err := decodeJSON(r, &body); err != nil || !model.ValidVoteChoices[body.Choice] {
		writeError(w, http.StatusBadRequest, "choice must be for, against, or abstain", "bad_request")
		return
	}
	if active, err := h.repo.MemberIsActive(r.Context(), memberID); err != nil {
		writeError(w, http.StatusInternalServerError, "eligibility check failed", "internal_error")
		return
	} else if !active {
		writeError(w, http.StatusForbidden, "your membership is not active, so you cannot vote", "forbidden")
		return
	}
	status, _, err := h.repo.MotionStatus(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "motion not found", "query error")
		return
	}
	if status != "open" {
		writeError(w, http.StatusConflict, "voting is not open on this motion", "conflict")
		return
	}
	if err := h.repo.CastVote(r.Context(), id, memberID, body.Choice, false, userIDFromCtx(r)); err != nil {
		writeError(w, http.StatusInternalServerError, "could not record vote", "internal_error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RecordVote lets an officer record a ballot on behalf of a member (in-person
// tally or a proxy vote) on an open motion (officer+).
func (h *GovernanceHandler) RecordVote(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		MemberID string `json:"member_id"`
		Choice   string `json:"choice"`
		IsProxy  bool   `json:"is_proxy"`
	}
	if err := decodeJSON(r, &body); err != nil || !isValidUUID(body.MemberID) || !model.ValidVoteChoices[body.Choice] {
		writeError(w, http.StatusBadRequest, "member_id (UUID) and choice (for/against/abstain) are required", "bad_request")
		return
	}
	status, _, err := h.repo.MotionStatus(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "motion not found", "query error")
		return
	}
	if status != "open" {
		writeError(w, http.StatusConflict, "voting is not open on this motion", "conflict")
		return
	}
	if err := h.repo.CastVote(r.Context(), id, body.MemberID, body.Choice, body.IsProxy, userIDFromCtx(r)); err != nil {
		if isFKViolation(err) {
			writeError(w, http.StatusBadRequest, "member does not exist", "bad_request")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not record vote", "internal_error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Proxies ----

// ListProxies returns the proxy assignments for a meeting (member+).
func (h *GovernanceHandler) ListProxies(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	proxies, err := h.repo.ListProxies(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	if proxies == nil {
		proxies = []model.MeetingProxy{}
	}
	writeJSON(w, http.StatusOK, proxies)
}

// CreateProxy assigns a proxy for a meeting (officer+).
func (h *GovernanceHandler) CreateProxy(w http.ResponseWriter, r *http.Request) {
	meetingID, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		GrantorID string `json:"grantor_id"`
		HolderID  string `json:"holder_id"`
	}
	if err := decodeJSON(r, &body); err != nil || !isValidUUID(body.GrantorID) || !isValidUUID(body.HolderID) {
		writeError(w, http.StatusBadRequest, "grantor_id and holder_id (member UUIDs) are required", "bad_request")
		return
	}
	if body.GrantorID == body.HolderID {
		writeError(w, http.StatusBadRequest, "a member cannot hold their own proxy", "bad_request")
		return
	}
	p, err := h.repo.CreateProxy(r.Context(), meetingID, body.GrantorID, body.HolderID)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "this member has already granted a proxy for this meeting", "conflict")
			return
		}
		if isFKViolation(err) {
			writeError(w, http.StatusBadRequest, "meeting or member does not exist", "bad_request")
			return
		}
		writeError(w, http.StatusInternalServerError, "create error", "internal_error")
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

// DeleteProxy removes a proxy assignment (officer+).
func (h *GovernanceHandler) DeleteProxy(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	if err := h.repo.DeleteProxy(r.Context(), id); err != nil {
		writeRepoError(w, err, "proxy not found", "delete error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validOptionalUUID reports whether p is nil or a valid UUID string.
func validOptionalUUID(p *string) bool {
	return p == nil || isValidUUID(*p)
}

// ---- Async ballots ----

// SendBallots emails a single-use ballot link to every active member with an
// email who has not yet voted on an OPEN motion (officer+). Requires a mailer.
func (h *GovernanceHandler) SendBallots(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	if h.mailer == nil {
		writeError(w, http.StatusServiceUnavailable, "email is not configured, so ballots cannot be sent", "unavailable")
		return
	}
	m, err := h.repo.GetMotion(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "motion not found", "query error")
		return
	}
	if m.Status != "open" {
		writeError(w, http.StatusConflict, "ballots can only be sent while voting is open", "conflict")
		return
	}
	recipients, err := h.repo.EligibleBallotMembers(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load recipients", "internal_error")
		return
	}
	// Create tokens synchronously (fast, and the links must be valid on return),
	// but queue the SMTP sends to run in the background so a large roster cannot
	// exceed the HTTP write timeout mid-loop.
	base := strings.TrimRight(h.cfg.BaseURL, "/")
	type outbound struct{ to, subject, body string }
	var queue []outbound
	for _, rec := range recipients {
		plain, hashed, gerr := auth.GenerateResetToken()
		if gerr != nil {
			continue
		}
		if serr := h.repo.UpsertBallotToken(r.Context(), id, rec.MemberID, hashed, time.Now().Add(ballotTokenTTL)); serr != nil {
			continue
		}
		link := fmt.Sprintf("%s/#/ballot?token=%s", base, plain)
		body := fmt.Sprintf(
			"Hello %s,\n\nA vote is open on the motion \"%s\".\n\n"+
				"Cast your ballot here (valid for 14 days, one use):\n\n%s\n\n"+
				"If you did not expect this, you can ignore this email.\n", rec.Name, m.Title, link)
		queue = append(queue, outbound{rec.Email, "Your ballot: " + m.Title, body})
	}
	mailer := h.mailer
	go func(items []outbound) {
		for _, it := range items {
			if err := mailer.Send([]string{it.to}, it.subject, it.body); err != nil {
				log.Printf("ballot email error (%s): %v", it.to, err)
			}
		}
	}(queue)
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": len(queue), "eligible": len(recipients)})
}

// GetBallot returns the motion context for a ballot token (public, unauthenticated).
func (h *GovernanceHandler) GetBallot(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		writeError(w, http.StatusBadRequest, "token required", "bad_request")
		return
	}
	ctx, err := h.repo.GetBallotContext(r.Context(), auth.HashResetToken(token))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "this ballot link is invalid or has expired", "bad_request")
			return
		}
		writeError(w, http.StatusInternalServerError, "lookup failed", "internal_error")
		return
	}
	if ctx.Status != "open" {
		writeError(w, http.StatusConflict, "voting on this motion has closed", "conflict")
		return
	}
	writeJSON(w, http.StatusOK, ctx)
}

// SubmitBallot records a vote from a ballot token, then consumes it (public).
func (h *GovernanceHandler) SubmitBallot(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token  string `json:"token"`
		Choice string `json:"choice"`
	}
	if err := decodeJSON(r, &body); err != nil || !model.ValidVoteChoices[body.Choice] {
		writeError(w, http.StatusBadRequest, "choice must be for, against, or abstain", "bad_request")
		return
	}
	if strings.TrimSpace(body.Token) == "" {
		writeError(w, http.StatusBadRequest, "token required", "bad_request")
		return
	}
	// Consume the token and record the vote atomically: the token is only spent
	// if the vote lands (and only while the motion is open).
	_, err := h.repo.ConsumeBallotAndVote(r.Context(), auth.HashResetToken(body.Token), body.Choice)
	if err != nil {
		switch {
		case errors.Is(err, repo.ErrMotionNotOpen):
			writeError(w, http.StatusConflict, "voting on this motion has closed", "conflict")
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, http.StatusBadRequest, "this ballot link is invalid, expired, or already used", "bad_request")
		default:
			writeError(w, http.StatusInternalServerError, "could not record your vote", "internal_error")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
