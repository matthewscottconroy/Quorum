package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
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
	// IANA zone name (e.g. "America/New_York"). Everything rendered
	// server-side — minutes documents, notification emails — uses it; an
	// empty value falls back to the server's own zone.
	// Flat late fee in minor units posted by the nightly job once an invoice
	// is past due + grace; 0/empty disables the feature entirely.
	"late_fee_minor": {validate: func(v string) bool {
		if v == "" {
			return true
		}
		n, err := strconv.ParseInt(v, 10, 64)
		return err == nil && n >= 0 && n <= 100_000_000
	}},
	"late_fee_grace_days": {validate: func(v string) bool {
		if v == "" {
			return true
		}
		n, err := strconv.Atoi(v)
		return err == nil && n >= 0 && n <= 365
	}},
	// Named agenda templates for the meeting scheduler: a JSON array of
	// {"name": "...", "agenda": "..."} objects, managed on the Settings page.
	"agenda_templates": {validate: func(v string) bool {
		if v == "" {
			return true
		}
		if len(v) > 8000 {
			return false
		}
		var arr []struct {
			Name   string `json:"name"`
			Agenda string `json:"agenda"`
		}
		return json.Unmarshal([]byte(v), &arr) == nil
	}},
	"timezone": {validate: func(v string) bool {
		if v == "" {
			return true
		}
		if len(v) > 64 {
			return false
		}
		_, err := time.LoadLocation(v)
		return err == nil
	}},
	// Org-wide two-factor mandate: accounts at/above this role must enroll.
	// Member-visible so the enrollment screen can explain the policy.
	"require_2fa": {validate: func(v string) bool {
		return v == "off" || v == "admin" || v == "officer" || v == "member"
	}},
	// Org vocabulary overrides: a JSON object mapping canonical UI labels to
	// the org's own terms ({"Dues":"Assessments","Members":"Residents"}).
	// Member-visible; the client applies it to nav and page labels.
	"vocab_overrides": {validate: func(v string) bool {
		if len(v) > 2000 {
			return false
		}
		var m map[string]string
		return json.Unmarshal([]byte(v), &m) == nil
	}},
	// A payment link template shown to members next to open invoices. Any
	// https URL; optional {amount_major} and {reference} placeholders are
	// filled per invoice ({reference} carries the invoice id for webhook
	// reconciliation). Org-agnostic: works for Stripe payment links,
	// PayPal.me, or anything else that accepts URL parameters.
	"payment_link_template": {validate: func(v string) bool {
		return v == "" || (len(v) <= 500 && strings.HasPrefix(v, "https://"))
	}},
	// Auto-lapse: members with an invoice overdue by more than this many days
	// are moved to inactive by the nightly job (0 = off). A renewal policy knob.
	"lapse_after_days": {validate: intRange(0, 3650)},
	// The Top Secret arcade switch. Unset/on = visible (the shipped default);
	// off hides the sidebar section AND disables the arcade API + websocket.
	// Member-visible so the client nav knows what to draw.
	"arcade_visible": {validate: func(v string) bool { return v == "on" || v == "off" }},
	// Legal hold: when 'on', the nightly job stops pruning the audit log
	// regardless of the retention window, preserving everything for an
	// investigation or litigation hold. Admin-only.
	"audit_legal_hold": {validate: func(v string) bool { return v == "on" || v == "off" }, adminOnly: true},
	// Scheduled CPA pack: an email address that receives the accounting pack
	// summary monthly (empty = off). Admin-only. Reuses the nightly job.
	"monthly_report_email": {validate: func(v string) bool {
		return v == "" || (len(v) <= 200 && strings.Contains(v, "@"))
	}, adminOnly: true},
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
