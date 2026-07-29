package handler

import (
	"context"
	"net/http"

	"quorum/internal/model"
)

// Shared CRUD mechanics. The resource handlers (contacts, resources, plans,
// action items, …) repeat the same List envelope, Get-by-id, allowed-field
// filtering, and confirm-then-delete-then-notify dance. These typed generics
// carry that once so a cross-cutting change (e.g. the delete confirmation gate)
// lands in a single place, while each handler keeps its own bespoke validation
// inline and readable.

// writePage writes a paginated list response, normalizing a nil slice to [] so
// the JSON is always an array rather than null.
func writePage[T any](w http.ResponseWriter, data []T, total, limit, offset int) {
	if data == nil {
		data = []T{}
	}
	writeJSON(w, http.StatusOK, model.Page[T]{Data: data, Total: total, Limit: limit, Offset: offset})
}

// genericGet resolves the "id" route param, fetches via get, and writes the
// object (or a 404 with notFoundMsg).
func genericGet[T any](w http.ResponseWriter, r *http.Request, get func(context.Context, string) (*T, error), notFoundMsg string) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	obj, err := get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, notFoundMsg, "not_found")
		return
	}
	writeJSON(w, http.StatusOK, obj)
}

// filterAllowedFields keeps only the whitelisted keys from a PATCH body, so
// unknown or non-updatable fields are silently ignored (never written).
func filterAllowedFields(body map[string]any, allowed map[string]bool) map[string]any {
	out := make(map[string]any, len(body))
	for k, v := range body {
		if allowed[k] {
			out[k] = v
		}
	}
	return out
}

// deleteSpec describes how to delete one resource kind for crudDelete.
type deleteSpec[T any] struct {
	entity      string                                          // audit/notice label, e.g. "contact"
	notFoundMsg string                                          // 404/query error message
	get         func(context.Context, string) (*T, error)       // fetch for the confirm name
	name        func(*T) string                                 // human name confirmed against ?confirm=
	del         func(context.Context, string) error             // the delete
	notifier    deletionNotifier                                // optional deletion notice
	affected    func(context.Context, string) ([]string, error) // optional affected-member emails
}

// singleEmail adapts a repo lookup that returns one affected address into the
// slice-returning shape deleteSpec.affected expects. An empty address yields no
// recipients.
func singleEmail(lookup func(context.Context, string) (string, error)) func(context.Context, string) ([]string, error) {
	return func(ctx context.Context, id string) ([]string, error) {
		email, err := lookup(ctx, id)
		if email == "" {
			return nil, err
		}
		return []string{email}, err
	}
}

// crudDelete performs the standard destructive-delete flow: resolve id, fetch,
// require ?confirm= to match the record's name, gather affected emails (if any),
// delete, then fire a deletion notice. Returns 204 on success.
func crudDelete[T any](w http.ResponseWriter, r *http.Request, spec deleteSpec[T]) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	obj, err := spec.get(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, spec.notFoundMsg, "query error")
		return
	}
	name := spec.name(obj)
	if !confirmMatches(w, r, name) {
		return
	}
	// Gather affected emails before deleting — related rows may cascade away.
	var affected []string
	if spec.affected != nil {
		affected, _ = spec.affected(r.Context(), id)
	}
	if err := spec.del(r.Context(), id); err != nil {
		writeRepoError(w, err, spec.notFoundMsg, "delete error")
		return
	}
	if spec.notifier != nil {
		spec.notifier.NotifyDeletion(r.Context(), userIDFromCtx(r), spec.entity, name, affected)
	}
	w.WriteHeader(http.StatusNoContent)
}
