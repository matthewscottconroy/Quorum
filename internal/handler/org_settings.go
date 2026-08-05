package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"
)

// orgSettingsRepo is satisfied by *repo.OrgSettingsRepo.
type orgSettingsRepo interface {
	All(ctx context.Context) (map[string]string, error)
	Set(ctx context.Context, key, value, updatedBy string) error
}

// Allowlisted settings; anything else is refused. Values are free text with
// per-key validation. Org-agnostic by design: these are the only knobs that
// assume nothing about the org.
type orgSetting struct {
	validate  func(string) bool
	adminOnly bool
}

func intRange(lo, hi int) func(string) bool {
	return func(v string) bool {
		n, err := strconv.Atoi(v)
		return err == nil && n >= lo && n <= hi
	}
}

var orgSettingKeys = map[string]orgSetting{
	// 1-12; drives fiscal-year defaults and labels only, never math.
	"fiscal_year_start_month": {validate: intRange(1, 12)},
	// Free text shown to members on My Account ("how to pay": Zelle address,
	// Venmo handle, checks payable to...).
	"how_to_pay": {validate: func(v string) bool { return len(v) <= 4000 }},
	// Continuity (roadmap E1-E3); admin-only visibility - these describe
	// where the org's keys live and who gets the bus-factor alarm.
	"infrastructure_facts":   {validate: func(v string) bool { return len(v) <= 4000 }, adminOnly: true},
	"continuity_watch_days":  {validate: intRange(0, 365), adminOnly: true},
	"continuity_contacts":    {validate: func(v string) bool { return len(v) <= 2000 }, adminOnly: true},
	"continuity_attest_days": {validate: intRange(7, 365), adminOnly: true},
}

// OrgSettingsHandler manages the small allowlisted org configuration.
type OrgSettingsHandler struct {
	repo orgSettingsRepo
}

// NewOrgSettingsHandler constructs the handler.
func NewOrgSettingsHandler(r orgSettingsRepo) *OrgSettingsHandler {
	return &OrgSettingsHandler{repo: r}
}

// Get returns all settings (member+; every key here is member-visible).
func (h *OrgSettingsHandler) Get(w http.ResponseWriter, r *http.Request) {
	all, err := h.repo.All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	admin := roleAtLeast(roleFromCtx(r), "admin")
	out := map[string]string{}
	for k, spec := range orgSettingKeys {
		if spec.adminOnly && !admin {
			continue
		}
		if v, ok := all[k]; ok {
			out[k] = v
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// Put updates settings (admin): body is a flat {key: value} map.
func (h *OrgSettingsHandler) Put(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	if err := decodeJSON(r, &body); err != nil || len(body) == 0 {
		writeError(w, http.StatusBadRequest, "a {key: value} object is required", "bad_request")
		return
	}
	for k, v := range body {
		spec, ok := orgSettingKeys[k]
		if !ok {
			writeError(w, http.StatusBadRequest, "unknown setting: "+k, "bad_request")
			return
		}
		if !spec.validate(strings.TrimSpace(v)) {
			writeError(w, http.StatusBadRequest, "invalid value for "+k, "bad_request")
			return
		}
	}
	for k, v := range body {
		if err := h.repo.Set(r.Context(), k, strings.TrimSpace(v), userIDFromCtx(r)); err != nil {
			writeError(w, http.StatusInternalServerError, "save error", "internal_error")
			return
		}
	}
	setAuditDetail(r, map[string]any{"keys": len(body)})
	h.Get(w, r)
}
