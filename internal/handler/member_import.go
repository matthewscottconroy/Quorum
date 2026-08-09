package handler

import (
	"context"
	"encoding/csv"
	"io"
	"net/http"
	"strings"
	"time"

	"quorum/internal/model"
)

// memberImporter is the slice of *repo.MembersRepo the CSV import needs.
type memberImporter interface {
	ExistingEmails(ctx context.Context) (map[string]bool, error)
	BatchCreate(ctx context.Context, members []*model.Member) (int, error)
}

// MemberImportHandler ingests a members CSV. It runs in two modes on the same
// endpoint: dry-run (default) returns a report — how many rows are new, dup, or
// invalid — so an admin verifies before committing; ?commit=true inserts the
// valid, non-duplicate rows. Column headers are matched case-insensitively;
// recognized: name/display_name, email, phone, address, tier, status,
// joined_at. Only name is required.
type MemberImportHandler struct {
	repo memberImporter
}

// NewMemberImportHandler constructs the handler.
func NewMemberImportHandler(r memberImporter) *MemberImportHandler {
	return &MemberImportHandler{repo: r}
}

type importRow struct {
	Line    int    `json:"line"`
	Name    string `json:"name"`
	Email   string `json:"email,omitempty"`
	Problem string `json:"problem,omitempty"` // set when the row can't be imported
	Dup     bool   `json:"duplicate,omitempty"`
}

var validImportStatus = map[string]bool{"active": true, "inactive": true, "suspended": true, "": true}

// Import handles POST /members/import (admin). Multipart field "file" (CSV) or
// a raw text/csv body. ?commit=true to actually insert.
func (h *MemberImportHandler) Import(w http.ResponseWriter, r *http.Request) {
	commit := r.URL.Query().Get("commit") == "true"

	var reader io.Reader = r.Body
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			writeError(w, http.StatusBadRequest, "could not read upload", "bad_request")
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			writeError(w, http.StatusBadRequest, "attach the CSV as field \"file\"", "bad_request")
			return
		}
		defer f.Close()
		reader = f
	}

	cr := csv.NewReader(io.LimitReader(reader, 5<<20))
	cr.FieldsPerRecord = -1 // tolerate ragged rows; we validate per field
	header, err := cr.Read()
	if err != nil {
		writeError(w, http.StatusBadRequest, "empty or unreadable CSV", "bad_request")
		return
	}
	col := map[string]int{}
	for i, name := range header {
		col[strings.ToLower(strings.TrimSpace(name))] = i
	}
	nameIdx := -1
	for _, k := range []string{"name", "display_name", "full name"} {
		if i, ok := col[k]; ok {
			nameIdx = i
			break
		}
	}
	if nameIdx < 0 {
		writeError(w, http.StatusBadRequest, "CSV needs a name (or display_name) column", "bad_request")
		return
	}
	get := func(rec []string, key string) string {
		if i, ok := col[key]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}

	existing, err := h.repo.ExistingEmails(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	seen := map[string]bool{}

	var toCreate []*model.Member
	var rows []importRow
	newCount, dupCount, badCount := 0, 0, 0
	line := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			rows = append(rows, importRow{Line: line, Problem: "unreadable row"})
			badCount++
			continue
		}
		if len(rec) > 100000 {
			continue
		}
		name := ""
		if nameIdx < len(rec) {
			name = strings.TrimSpace(rec[nameIdx])
		}
		email := strings.ToLower(get(rec, "email"))
		status := strings.ToLower(get(rec, "status"))
		row := importRow{Line: line, Name: name, Email: email}

		if name == "" || len(name) > 200 {
			row.Problem = "missing or too-long name"
			badCount++
			rows = append(rows, row)
			continue
		}
		if !validImportStatus[status] {
			row.Problem = "invalid status (use active/inactive/suspended)"
			badCount++
			rows = append(rows, row)
			continue
		}
		if email != "" && (existing[email] || seen[email]) {
			row.Dup = true
			dupCount++
			rows = append(rows, row)
			continue
		}
		if email != "" {
			seen[email] = true
		}
		if status == "" {
			status = "active"
		}
		tier := get(rec, "tier")
		if tier == "" {
			tier = "standard"
		}
		joined := time.Now()
		if j := get(rec, "joined_at"); j != "" {
			if t, perr := time.Parse("2006-01-02", j); perr == nil {
				joined = t
			}
		}
		em, ph, ad, nt := get(rec, "email"), get(rec, "phone"), get(rec, "address"), get(rec, "notes")
		toCreate = append(toCreate, &model.Member{
			DisplayName: name, Email: &em, Phone: &ph, Address: &ad,
			Tier: tier, Status: status, JoinedAt: joined, Notes: &nt,
		})
		newCount++
		rows = append(rows, row)
	}

	resp := map[string]any{
		"committed": false,
		"new":       newCount,
		"duplicate": dupCount,
		"invalid":   badCount,
		"rows":      rows,
	}
	if commit && newCount > 0 {
		n, err := h.repo.BatchCreate(r.Context(), toCreate)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "import failed", "internal_error")
			return
		}
		resp["committed"] = true
		resp["imported"] = n
		setAuditDetail(r, map[string]any{"imported": n, "skipped_dup": dupCount, "skipped_bad": badCount})
	}
	writeJSON(w, http.StatusOK, resp)
}
