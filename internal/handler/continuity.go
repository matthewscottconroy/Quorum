package handler

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"quorum/internal/model"
)

// continuityRepo is satisfied by *repo.ContinuityRepo.
type continuityRepo interface {
	ListCustody(ctx context.Context) ([]model.SecretCustody, error)
	CreateCustody(ctx context.Context, name, location, holder string) error
	UpdateCustody(ctx context.Context, id, name, location, holder string) error
	DeleteCustody(ctx context.Context, id string) error
	Attest(ctx context.Context, id, userID string) error
	Checks(ctx context.Context) (*model.ContinuityChecks, error)
}

// PackConfigSources gathers the org snapshot for the continuity pack.
type PackConfigSources struct {
	Settings orgSettingsRepo
	GL       glRepoC
	Funds    packFundsSource
	Groups   interface {
		List(ctx context.Context) ([]model.Group, error)
	}
	Users interface {
		ListUsers(ctx context.Context) ([]model.User, error)
	}
}

// ContinuityHandler: custody registry, health checks, and the successor
// pack (roadmap/continuity.md E1-E2).
type ContinuityHandler struct {
	repo     continuityRepo
	src      PackConfigSources
	audit    auditRepo
	users    exporterLookup
	baseURL  string
	smtpOn   bool
	stripeOn bool
	paypalOn bool
}

// NewContinuityHandler constructs the handler.
func NewContinuityHandler(r continuityRepo, src PackConfigSources, a auditRepo, u exporterLookup, baseURL string, smtpOn, stripeOn, paypalOn bool) *ContinuityHandler {
	return &ContinuityHandler{repo: r, src: src, audit: a, users: u, baseURL: baseURL,
		smtpOn: smtpOn, stripeOn: stripeOn, paypalOn: paypalOn}
}

// Custody lists the registry (admin).
func (h *ContinuityHandler) Custody(w http.ResponseWriter, r *http.Request) {
	rows, err := h.repo.ListCustody(r.Context())
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	if rows == nil {
		rows = []model.SecretCustody{}
	}
	writeJSON(w, 200, rows)
}

func custodyBody(w http.ResponseWriter, r *http.Request) (name, location, holder string, ok bool) {
	var body struct{ Name, Location, Holder string }
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return "", "", "", false
	}
	name, location, holder = strings.TrimSpace(body.Name), strings.TrimSpace(body.Location), strings.TrimSpace(body.Holder)
	if name == "" || location == "" || holder == "" || len(name) > 120 || len(location) > 300 || len(holder) > 120 {
		writeError(w, 400, "name, location, and holder are required", "bad_request")
		return "", "", "", false
	}
	return name, location, holder, true
}

// CreateCustody adds a row (admin).
func (h *ContinuityHandler) CreateCustody(w http.ResponseWriter, r *http.Request) {
	name, location, holder, ok := custodyBody(w, r)
	if !ok {
		return
	}
	if err := h.repo.CreateCustody(r.Context(), name, location, holder); err != nil {
		writeRepoError(w, err, "", "create error")
		return
	}
	setAuditDetail(r, map[string]any{"name": name})
	w.WriteHeader(201)
}

// UpdateCustody edits a row; verification resets (admin).
func (h *ContinuityHandler) UpdateCustody(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	name, location, holder, ok := custodyBody(w, r)
	if !ok {
		return
	}
	if err := h.repo.UpdateCustody(r.Context(), id, name, location, holder); err != nil {
		writeRepoError(w, err, "custody row not found", "update error")
		return
	}
	setAuditDetail(r, map[string]any{"name": name})
	w.WriteHeader(204)
}

// DeleteCustody removes a row (admin).
func (h *ContinuityHandler) DeleteCustody(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	if err := h.repo.DeleteCustody(r.Context(), id); err != nil {
		writeRepoError(w, err, "custody row not found", "delete error")
		return
	}
	w.WriteHeader(204)
}

// AttestCustody records a verification (admin).
func (h *ContinuityHandler) AttestCustody(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	if err := h.repo.Attest(r.Context(), id, userIDFromCtx(r)); err != nil {
		writeRepoError(w, err, "custody row not found", "attest error")
		return
	}
	setAuditDetail(r, map[string]any{"custody_id": id})
	w.WriteHeader(204)
}

// Checks returns the continuity health picture, plus TLS days-left when the
// public endpoint is reachable (admin).
func (h *ContinuityHandler) Checks(w http.ResponseWriter, r *http.Request) {
	c, err := h.repo.Checks(r.Context())
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	resp := map[string]any{
		"superadmins": c.Superadmins, "superadmins_ok": c.Superadmins >= 2,
		"custody_rows": c.CustodyRows, "custody_stale": c.CustodyStale,
		"attest_days": c.AttestDays, "watch_configured": c.WatchConfigured,
	}
	if u, err := url.Parse(h.baseURL); err == nil && u.Scheme == "https" {
		if days, err := tlsDaysLeft(u.Host); err == nil {
			resp["tls_days_left"] = days
		}
	}
	writeJSON(w, 200, resp)
}

func tlsDaysLeft(host string) (int, error) {
	if !strings.Contains(host, ":") {
		host += ":443"
	}
	d := &tls.Dialer{Config: &tls.Config{MinVersion: tls.VersionTLS12}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return 0, err
	}
	defer conn.Close() //nolint:errcheck
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return 0, fmt.Errorf("no certificate")
	}
	return int(time.Until(certs[0].NotAfter).Hours() / 24), nil
}

// Pack renders the Continuity Pack (superadmin): the successor's map.
func (h *ContinuityHandler) Pack(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	settings, err := h.src.Settings.All(ctx)
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	rules, _ := h.src.GL.PostingRules(ctx)
	accounts, _ := h.src.GL.Accounts(ctx)
	funds, _ := h.src.Funds.ListFunds(ctx)
	groups, _ := h.src.Groups.List(ctx)
	users, _ := h.src.Users.ListUsers(ctx)
	custody, _ := h.repo.ListCustody(ctx)
	checks, _ := h.repo.Checks(ctx)

	type slimUser struct {
		Email, Role string
		TOTP        bool `json:"totp_enabled"`
	}
	slims := make([]slimUser, 0, len(users))
	for _, u := range users {
		slims = append(slims, slimUser{Email: u.Email, Role: u.Role, TOTP: u.TOTPEnabled})
	}
	cfg := map[string]any{
		"generated_at":  time.Now().UTC().Format(time.RFC3339),
		"base_url":      h.baseURL,
		"settings":      settings,
		"posting_rules": rules, "chart_of_accounts": accounts,
		"funds": funds, "visibility_groups": groups, "users": slims,
		"integrations":      map[string]bool{"smtp": h.smtpOn, "stripe": h.stripeOn, "paypal": h.paypalOn},
		"continuity_checks": checks,
	}
	cfgJSON, _ := json.MarshalIndent(cfg, "", "  ")

	custodyCSV := [][]string{{"secret", "location", "holder", "last_verified", "verified_by"}}
	for _, c := range custody {
		lv := ""
		if c.LastVerifiedAt != nil {
			lv = c.LastVerifiedAt.UTC().Format(time.RFC3339)
		}
		custodyCSV = append(custodyCSV, []string{c.Name, c.Location, c.Holder, lv, c.LastVerifiedBy})
	}

	who := userIDFromCtx(r)
	if u, err := h.users.GetUserByID(ctx, who); err == nil && u.Email != "" {
		who = u.Email
	}
	readme := fmt.Sprintf(`# You have inherited this system

This pack was generated %s by %s from the live system at %s.

Quorum is a self-hosted organization manager: members, dues, meetings,
documents, discussions, and double-entry books. It runs as one Go binary
plus PostgreSQL. The complete operations manual lives in the public source
repository (see infrastructure.md) - start with README.md, then RUNBOOK.md.

## Your first five moves

1. READ infrastructure.md in this pack: where everything runs and where
   each credential is held (custody.csv names the holders).
2. Gain server access (SSH key or cloud console - custody.csv), then
   confirm backups: 'make backup-list' on the server, and check the
   off-site bucket has recent files.
3. Verify nothing was tampered with: '/opt/quorum/quorum -verify-audit'.
4. Rotate what the previous operator held personally: QUORUM_JWT_SECRET
   (RUNBOOK 'Rotate'), server SSH keys, cloud credentials.
5. Establish yourself a superadmin login if you lack one: RUNBOOK,
   'Recover a locked-out admin', Case 3.

org-configuration.json is the full snapshot of how the org is configured
in-app (chart of accounts, posting rules, funds and their signers, roles).
Continuity checks at generation time are included there - anything stale
was already overdue before you arrived. Good luck; it is a sturdy ship.
`, time.Now().UTC().Format(time.RFC3339), who, h.baseURL)

	infra := settings["infrastructure_facts"]
	if infra == "" {
		infra = "(The infrastructure_facts org setting was never filled in.\nAn admin should complete it under Settings -> Organization settings.)"
	}

	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	for _, f := range []struct {
		name string
		data []byte
	}{
		{"SUCCESSOR-README.md", []byte(readme)},
		{"infrastructure.md", []byte("# Infrastructure facts (admin-maintained)\n\n" + infra + "\n")},
		{"org-configuration.json", cfgJSON},
		{"custody.csv", csvBytes(custodyCSV)},
	} {
		fw, err := zw.Create(f.name)
		if err == nil {
			_, err = fw.Write(f.data)
		}
		if err != nil {
			writeError(w, 500, "zip error", "internal_error")
			return
		}
	}
	if err := zw.Close(); err != nil {
		writeError(w, 500, "zip error", "internal_error")
		return
	}
	sum := sha256.Sum256(zbuf.Bytes())
	digest := hex.EncodeToString(sum[:])
	auditExport(r, h.audit, "continuity-pack.zip", map[string]any{"sha256": digest})
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="quorum-continuity-pack.zip"`)
	w.Header().Set("X-Document-SHA256", digest)
	_, _ = w.Write(zbuf.Bytes())
}
