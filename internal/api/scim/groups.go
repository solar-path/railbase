package scim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	scimauth "github.com/railbase/railbase/internal/auth/scim"
	"github.com/railbase/railbase/internal/rbac"
)

const scimGroupSchema = "urn:ietf:params:scim:schemas:core:2.0:Group"

// scimGroup is the SCIM 2.0 Group resource per RFC 7643 §4.2.
type scimGroup struct {
	Schemas     []string         `json:"schemas"`
	ID          string           `json:"id"`
	ExternalID  string           `json:"externalId,omitempty"`
	DisplayName string           `json:"displayName"`
	Members     []scimGroupMember `json:"members"`
	Meta        scimMeta         `json:"meta"`
}

type scimGroupMember struct {
	Value   string `json:"value"`            // user UUID
	Display string `json:"display,omitempty"` // username (denormalised — IdP wants it without joining)
	Type    string `json:"type,omitempty"`    // "User" or "Group"
	Ref     string `json:"$ref,omitempty"`
}

var groupColumns = scimauth.ColumnMap{
	"displayname": "lower(display_name)",
	"externalid":  "external_id",
	"id":          "id::text",
}

// GroupsDeps is the construction surface for the Groups handler.
type GroupsDeps struct {
	Pool *pgxpool.Pool
	// RBAC enables the SCIM-group → RBAC-role reconciliation path
	// added in the v1.7.51 follow-up. Nil = membership-only behaviour
	// (the original v1.7.51 ship contract). Set in Deps.RBAC and
	// threaded by Mount.
	RBAC *rbac.Store
}

// MountGroups registers the /scim/v2/Groups routes. Caller MUST wrap
// w/ AuthMiddleware first.
func MountGroups(r chi.Router, d *GroupsDeps) {
	r.Get("/Groups", d.listGroups)
	r.Post("/Groups", d.createGroup)
	r.Get("/Groups/{id}", d.getGroup)
	r.Put("/Groups/{id}", d.replaceGroup)
	r.Patch("/Groups/{id}", d.patchGroup)
	r.Delete("/Groups/{id}", d.deleteGroup)
}

func (d *GroupsDeps) listGroups(w http.ResponseWriter, r *http.Request) {
	tok := TokenFromContext(r.Context())
	q := r.URL.Query()
	startIndex := 1
	if v := q.Get("startIndex"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			startIndex = n
		}
	}
	count := 100
	if v := q.Get("count"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 500 {
			count = n
		}
	}
	filterFrag := ""
	args := []any{tok.Collection}
	if f := q.Get("filter"); strings.TrimSpace(f) != "" {
		node, err := scimauth.Parse(f)
		if err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		frag, fargs, err := scimauth.ToSQL(node, groupColumns)
		if err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		filterFrag = frag
		// Renumber the $N placeholders so they pick up after $1
		// (collection). Easiest: append filter args and let frag
		// already use $1..$N from ToSQL's perspective which doesn't
		// know about our $1 collection. ToSQL's args start at $1, so
		// we rewrite them here:
		filterFrag = renumberPlaceholders(filterFrag, 1)
		args = append(args, fargs...)
	}
	scope := "collection = $1"
	if filterFrag != "" {
		scope = scope + " AND (" + filterFrag + ")"
	}

	// RFC 7644 §3.4.2.3 — sorting. Same whitelist posture as Users:
	// unknown sortBy → 400 InvalidValue. Absent sortBy falls back to
	// `created_at ASC` to preserve prior pagination behaviour.
	orderBy, ok := sortClause(r, groupSortColumns)
	if !ok {
		WriteError(w, http.StatusBadRequest, "invalidValue: sortBy attribute is not sortable")
		return
	}
	if orderBy == "" {
		orderBy = "created_at ASC"
	}

	totalQuery := fmt.Sprintf(`SELECT COUNT(*) FROM _scim_groups WHERE %s`, scope)
	var total int
	if err := d.Pool.QueryRow(r.Context(), totalQuery, args...).Scan(&total); err != nil {
		WriteError(w, http.StatusInternalServerError, "count groups: "+err.Error())
		return
	}

	offset := startIndex - 1
	if offset < 0 {
		offset = 0
	}
	rowsArgs := append([]any{}, args...)
	rowsArgs = append(rowsArgs, count, offset)
	rowsQuery := fmt.Sprintf(`
        SELECT id, external_id, display_name, created_at, updated_at
          FROM _scim_groups
         WHERE %s
         ORDER BY %s
         LIMIT $%d OFFSET $%d
    `, scope, orderBy, len(args)+1, len(args)+2)
	rows, err := d.Pool.Query(r.Context(), rowsQuery, rowsArgs...)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "list groups: "+err.Error())
		return
	}
	defer rows.Close()

	out := []any{}
	for rows.Next() {
		g, _, err := scanGroup(r.Context(), d.Pool, rows, tok.Collection, r)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, g)
	}
	writeJSON(w, http.StatusOK, scimListResponse{
		Schemas:      []string{listResponseSchema},
		TotalResults: total,
		StartIndex:   startIndex,
		ItemsPerPage: len(out),
		Resources:    out,
	})
}

// getGroup — GET /Groups/{id}
//
// RFC 7644 §3.7 — emits a weak ETag for every successful read +
// honours `If-None-Match` with a 304 short-circuit.
func (d *GroupsDeps) getGroup(w http.ResponseWriter, r *http.Request) {
	tok := TokenFromContext(r.Context())
	id := chi.URLParam(r, "id")
	gid, err := uuid.Parse(id)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "id must be a UUID")
		return
	}
	g, updated, err := loadGroup(r.Context(), d.Pool, tok.Collection, gid, r)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			WriteError(w, http.StatusNotFound, "group not found")
			return
		}
		WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tag := etagFor(updated)
	setETag(w, tag)
	if checkIfNoneMatch(r, tag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (d *GroupsDeps) createGroup(w http.ResponseWriter, r *http.Request) {
	tok := TokenFromContext(r.Context())
	var body scimGroup
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(body.DisplayName) == "" {
		WriteError(w, http.StatusBadRequest, "displayName is required")
		return
	}
	id := uuid.Must(uuid.NewV7())
	if _, err := d.Pool.Exec(r.Context(), `
        INSERT INTO _scim_groups (id, external_id, display_name, collection)
        VALUES ($1, $2, $3, $4)
    `, id, nullIfEmpty(body.ExternalID), body.DisplayName, tok.Collection); err != nil {
		WriteError(w, http.StatusInternalServerError, "create group: "+err.Error())
		return
	}
	// Members on create — wire the join rows.
	if len(body.Members) > 0 {
		if err := setGroupMembers(r.Context(), d.Pool, id, tok.Collection, body.Members); err != nil {
			WriteError(w, http.StatusInternalServerError, "set members: "+err.Error())
			return
		}
		// v1.7.51+ role reconciliation — each newly-added user gets
		// every role mapped to this group via _scim_group_role_map.
		for _, m := range body.Members {
			uid, err := uuid.Parse(m.Value)
			if err != nil {
				continue
			}
			_ = reconcileGroupGrants(r.Context(), d.Pool, d.RBAC, tok.Collection, id, uid, true)
		}
	}
	g, updated, err := loadGroup(r.Context(), d.Pool, tok.Collection, id, r)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	setETag(w, etagFor(updated))
	writeJSON(w, http.StatusCreated, g)
}

// replaceGroup — PUT /Groups/{id}. Full replacement.
//
// RFC 7644 §3.7 — honours `If-Match`: precondition is loaded against
// current `updated_at` ETag; mismatch → 412 with SCIM error envelope.
func (d *GroupsDeps) replaceGroup(w http.ResponseWriter, r *http.Request) {
	tok := TokenFromContext(r.Context())
	id := chi.URLParam(r, "id")
	gid, err := uuid.Parse(id)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "id must be a UUID")
		return
	}
	var body scimGroup
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(body.DisplayName) == "" {
		WriteError(w, http.StatusBadRequest, "displayName is required")
		return
	}
	if r.Header.Get("If-Match") != "" {
		_, currentMtime, err := loadGroup(r.Context(), d.Pool, tok.Collection, gid, r)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				WriteError(w, http.StatusNotFound, "group not found")
				return
			}
			WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if _, ok := checkIfMatch(r, etagFor(currentMtime)); !ok {
			writePreconditionFailed(w, "If-Match precondition failed: resource version has changed")
			return
		}
	}
	q := `UPDATE _scim_groups
	         SET display_name = $2,
	             external_id = $3,
	             updated_at = now()
	       WHERE id = $1 AND collection = $4
	      RETURNING id`
	var got uuid.UUID
	if err := d.Pool.QueryRow(r.Context(), q, gid, body.DisplayName,
		nullIfEmpty(body.ExternalID), tok.Collection).Scan(&got); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			WriteError(w, http.StatusNotFound, "group not found")
			return
		}
		WriteError(w, http.StatusInternalServerError, "replace group: "+err.Error())
		return
	}
	// PUT replaces membership entirely. Collect OLD members BEFORE
	// the wipe so we can reconcile RBAC role grants — anyone in
	// `old\new` has been effectively removed; `new\old` has been
	// added; intersection is unchanged.
	oldMembers, err := listGroupMemberUUIDs(r.Context(), d.Pool, gid)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "list old members: "+err.Error())
		return
	}
	if _, err := d.Pool.Exec(r.Context(),
		`DELETE FROM _scim_group_members WHERE group_id = $1`, gid); err != nil {
		WriteError(w, http.StatusInternalServerError, "clear members: "+err.Error())
		return
	}
	if err := setGroupMembers(r.Context(), d.Pool, gid, tok.Collection, body.Members); err != nil {
		WriteError(w, http.StatusInternalServerError, "set members: "+err.Error())
		return
	}
	// Diff old vs new and reconcile.
	newMembers := map[uuid.UUID]struct{}{}
	for _, m := range body.Members {
		if uid, err := uuid.Parse(m.Value); err == nil {
			newMembers[uid] = struct{}{}
		}
	}
	oldMembersSet := map[uuid.UUID]struct{}{}
	for _, u := range oldMembers {
		oldMembersSet[u] = struct{}{}
	}
	for uid := range newMembers {
		if _, present := oldMembersSet[uid]; !present {
			_ = reconcileGroupGrants(r.Context(), d.Pool, d.RBAC, tok.Collection, gid, uid, true)
		}
	}
	for _, uid := range oldMembers {
		if _, present := newMembers[uid]; !present {
			_ = reconcileGroupGrants(r.Context(), d.Pool, d.RBAC, tok.Collection, gid, uid, false)
		}
	}
	g, updated, err := loadGroup(r.Context(), d.Pool, tok.Collection, gid, r)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	setETag(w, etagFor(updated))
	writeJSON(w, http.StatusOK, g)
}

// patchGroup — PATCH /Groups/{id}. Real-world IdPs send "add members"
// + "remove members" PATCH operations; SCIM clients rarely send a
// full PUT for group membership churn (it doesn't scale).
//
// RFC 7644 §3.7 — honours `If-Match`; mismatch → 412 PreconditionFailed.
func (d *GroupsDeps) patchGroup(w http.ResponseWriter, r *http.Request) {
	tok := TokenFromContext(r.Context())
	id := chi.URLParam(r, "id")
	gid, err := uuid.Parse(id)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "id must be a UUID")
		return
	}

	// If-Match precondition (RFC 7644 §3.7). The existence check below
	// would also catch a 404, but doing the load here makes the 412
	// vs 404 distinction crisp.
	if r.Header.Get("If-Match") != "" {
		_, currentMtime, err := loadGroup(r.Context(), d.Pool, tok.Collection, gid, r)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				WriteError(w, http.StatusNotFound, "group not found")
				return
			}
			WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if _, ok := checkIfMatch(r, etagFor(currentMtime)); !ok {
			writePreconditionFailed(w, "If-Match precondition failed: resource version has changed")
			return
		}
	}

	var body struct {
		Schemas    []string `json:"schemas"`
		Operations []struct {
			Op    string          `json:"op"`
			Path  string          `json:"path,omitempty"`
			Value json.RawMessage `json:"value"`
		} `json:"Operations"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Sanity-check the group exists + belongs to this collection.
	var exists bool
	if err := d.Pool.QueryRow(r.Context(),
		`SELECT TRUE FROM _scim_groups WHERE id = $1 AND collection = $2`, gid, tok.Collection).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			WriteError(w, http.StatusNotFound, "group not found")
			return
		}
		WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	tx, err := d.Pool.Begin(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "begin tx: "+err.Error())
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	// Collect role-reconciliation deltas as we walk the ops; apply
	// them AFTER tx.Commit so the RBAC store sees the post-commit
	// membership state (the "userHasMappedRoleViaOtherGroup" check
	// inside reconcileGroupGrants depends on it for the REMOVE path).
	type memberDelta struct {
		userID uuid.UUID
		added  bool
	}
	var deltas []memberDelta
	// For "remove all members" (path == "members") we need the list
	// of users to remove BEFORE the DELETE, because after the DELETE
	// the userHasMappedRoleViaOtherGroup check would still return
	// false (correct) but we'd have no enumeration of which users
	// to reconcile. Capture the list at op-handling time.

	for _, op := range body.Operations {
		opName := strings.ToLower(op.Op)
		path := op.Path
		switch {
		case opName == "replace" && strings.EqualFold(path, "displayName"):
			var v string
			if err := json.Unmarshal(op.Value, &v); err != nil {
				WriteError(w, http.StatusBadRequest, "displayName must be string")
				return
			}
			if _, err := tx.Exec(r.Context(),
				`UPDATE _scim_groups SET display_name = $2, updated_at = now() WHERE id = $1`, gid, v); err != nil {
				WriteError(w, http.StatusInternalServerError, err.Error())
				return
			}
		case opName == "add" && strings.EqualFold(path, "members"):
			var members []scimGroupMember
			if err := json.Unmarshal(op.Value, &members); err != nil {
				WriteError(w, http.StatusBadRequest, "members must be an array")
				return
			}
			for _, m := range members {
				uid, err := uuid.Parse(m.Value)
				if err != nil {
					continue
				}
				if _, err := tx.Exec(r.Context(),
					`INSERT INTO _scim_group_members (group_id, user_id, user_collection)
					     VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
					gid, uid, tok.Collection); err != nil {
					WriteError(w, http.StatusInternalServerError, err.Error())
					return
				}
				deltas = append(deltas, memberDelta{userID: uid, added: true})
			}
		case opName == "remove" && strings.HasPrefix(strings.ToLower(path), "members"):
			// Two valid SCIM forms here:
			//   "path": "members"                    -- remove all
			//   "path": `members[value eq "<uuid>"]` -- remove one
			lower := strings.ToLower(path)
			if lower == "members" {
				// Enumerate the to-be-removed members BEFORE the
				// DELETE — needed for post-commit reconciliation.
				toRemove, err := txListGroupMemberUUIDs(r.Context(), tx, gid)
				if err != nil {
					WriteError(w, http.StatusInternalServerError, "enumerate members: "+err.Error())
					return
				}
				if _, err := tx.Exec(r.Context(),
					`DELETE FROM _scim_group_members WHERE group_id = $1`, gid); err != nil {
					WriteError(w, http.StatusInternalServerError, err.Error())
					return
				}
				for _, uid := range toRemove {
					deltas = append(deltas, memberDelta{userID: uid, added: false})
				}
				continue
			}
			// Best-effort extraction of `"value eq "<uuid>"` from the
			// path. SCIM clients don't standardise spacing, so we
			// scan for a UUID anywhere in the path string.
			extracted := extractUUIDFromPath(path)
			if extracted == uuid.Nil {
				WriteError(w, http.StatusBadRequest, "could not extract user UUID from path: "+path)
				return
			}
			if _, err := tx.Exec(r.Context(),
				`DELETE FROM _scim_group_members WHERE group_id = $1 AND user_id = $2`, gid, extracted); err != nil {
				WriteError(w, http.StatusInternalServerError, err.Error())
				return
			}
			deltas = append(deltas, memberDelta{userID: extracted, added: false})
		default:
			// Unknown op — accept silently (SCIM client may patch
			// fields we don't model).
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		WriteError(w, http.StatusInternalServerError, "commit: "+err.Error())
		return
	}

	// Post-commit: reconcile RBAC for every membership delta. Errors
	// are intentionally absorbed (logged at the helper layer) — the
	// SCIM PATCH succeeded; a partial role-grant failure shouldn't
	// surface as a 500 to the IdP and trigger retry storms.
	for _, dlt := range deltas {
		_ = reconcileGroupGrants(r.Context(), d.Pool, d.RBAC, tok.Collection, gid, dlt.userID, dlt.added)
	}

	g, updated, err := loadGroup(r.Context(), d.Pool, tok.Collection, gid, r)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	setETag(w, etagFor(updated))
	writeJSON(w, http.StatusOK, g)
}

func (d *GroupsDeps) deleteGroup(w http.ResponseWriter, r *http.Request) {
	tok := TokenFromContext(r.Context())
	id := chi.URLParam(r, "id")
	gid, err := uuid.Parse(id)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "id must be a UUID")
		return
	}
	// Snapshot members BEFORE DELETE — the ON DELETE CASCADE on
	// _scim_group_members wipes them along with the group, and we
	// need the list to reconcile role grants. We also snapshot
	// _scim_group_role_map (CASCADE on it too) for the same reason.
	toReconcile, _ := listGroupMemberUUIDs(r.Context(), d.Pool, gid)
	mappedRoles, _ := mappedRolesForGroup(r.Context(), d.Pool, gid)

	tag, err := d.Pool.Exec(r.Context(),
		`DELETE FROM _scim_groups WHERE id = $1 AND collection = $2`, gid, tok.Collection)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		WriteError(w, http.StatusNotFound, "group not found")
		return
	}

	// Post-delete: for each ex-member, drop any role granted ONLY via
	// the deleted group. Other groups' mappings remain — handled by
	// the userHasMappedRoleViaOtherGroup check inside the helper
	// (which sees the deleted group is gone + excludes nothing).
	if d.RBAC != nil && len(toReconcile) > 0 && len(mappedRoles) > 0 {
		for _, uid := range toReconcile {
			for _, rid := range mappedRoles {
				stillOwed, _ := userHasMappedRoleViaOtherGroup(
					r.Context(), d.Pool, uid, tok.Collection, rid, gid)
				if !stillOwed {
					_ = d.RBAC.Unassign(r.Context(), tok.Collection, uid, rid, nil)
				}
			}
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- group helpers ---

// loadGroup reads one group + its members. Returns the row's
// `updated_at` mtime so callers can compute its weak ETag without
// re-querying.
func loadGroup(ctx context.Context, pool *pgxpool.Pool, coll string, id uuid.UUID, r *http.Request) (*scimGroup, time.Time, error) {
	row := pool.QueryRow(ctx, `
        SELECT id, external_id, display_name, created_at, updated_at
          FROM _scim_groups
         WHERE id = $1 AND collection = $2
    `, id, coll)
	return scanGroup(ctx, pool, row, coll, r)
}

// scanGroup materialises a `_scim_groups`-shaped row as a SCIM
// resource + returns the row's `updated_at` mtime alongside (same
// contract as scanUser).
func scanGroup(ctx context.Context, pool *pgxpool.Pool, s scanner, coll string, r *http.Request) (*scimGroup, time.Time, error) {
	var (
		id      uuid.UUID
		extID   *string
		display string
		created time.Time
		updated time.Time
	)
	if err := s.Scan(&id, &extID, &display, &created, &updated); err != nil {
		return nil, time.Time{}, err
	}
	g := &scimGroup{
		Schemas:     []string{scimGroupSchema},
		ID:          id.String(),
		DisplayName: display,
		Members:     []scimGroupMember{},
		Meta: scimMeta{
			ResourceType: "Group",
			Created:      created.UTC().Format(time.RFC3339),
			LastModified: updated.UTC().Format(time.RFC3339),
			Location:     fmt.Sprintf("%s/scim/v2/Groups/%s", baseURL(r), id),
			Version:      etagFor(updated),
		},
	}
	if extID != nil {
		g.ExternalID = *extID
	}
	memberRows, err := pool.Query(ctx, `
        SELECT m.user_id, COALESCE(u.email, '')
          FROM _scim_group_members m
          LEFT JOIN `+coll+` u ON u.id = m.user_id
         WHERE m.group_id = $1
         ORDER BY m.added_at ASC
    `, id)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer memberRows.Close()
	for memberRows.Next() {
		var uid uuid.UUID
		var display string
		if err := memberRows.Scan(&uid, &display); err != nil {
			return nil, time.Time{}, err
		}
		g.Members = append(g.Members, scimGroupMember{
			Value:   uid.String(),
			Display: display,
			Type:    "User",
			Ref:     fmt.Sprintf("%s/scim/v2/Users/%s", baseURL(r), uid),
		})
	}
	return g, updated, nil
}

// setGroupMembers writes the (group, user) join rows for a new group
// or a full-replace.
func setGroupMembers(ctx context.Context, pool *pgxpool.Pool, gid uuid.UUID, coll string, members []scimGroupMember) error {
	for _, m := range members {
		uid, err := uuid.Parse(m.Value)
		if err != nil {
			continue // skip malformed
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO _scim_group_members (group_id, user_id, user_collection)
			     VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			gid, uid, coll); err != nil {
			return err
		}
	}
	return nil
}

// extractUUIDFromPath pulls a UUID out of a SCIM path filter like
// `members[value eq "f81d4fae-..."]`. Returns uuid.Nil if none found.
func extractUUIDFromPath(path string) uuid.UUID {
	// Walk the string looking for a quoted segment.
	start := strings.IndexByte(path, '"')
	if start < 0 {
		// IdPs occasionally use single quotes.
		start = strings.IndexByte(path, '\'')
		if start < 0 {
			return uuid.Nil
		}
	}
	end := -1
	for i := start + 1; i < len(path); i++ {
		if path[i] == path[start] {
			end = i
			break
		}
	}
	if end < 0 {
		return uuid.Nil
	}
	val := path[start+1 : end]
	u, err := uuid.Parse(val)
	if err != nil {
		return uuid.Nil
	}
	return u
}

// listGroupMemberUUIDs returns every user_id currently in the group.
// Read against the bare pool — used pre/post tx for reconciliation.
func listGroupMemberUUIDs(ctx context.Context, pool *pgxpool.Pool, gid uuid.UUID) ([]uuid.UUID, error) {
	rows, err := pool.Query(ctx,
		`SELECT user_id FROM _scim_group_members WHERE group_id = $1`, gid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var u uuid.UUID
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// txListGroupMemberUUIDs is the tx-bound variant used inside the
// patchGroup transaction so the SELECT sees the in-flight membership
// state (any prior op in the same PATCH).
func txListGroupMemberUUIDs(ctx context.Context, tx pgx.Tx, gid uuid.UUID) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx,
		`SELECT user_id FROM _scim_group_members WHERE group_id = $1`, gid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var u uuid.UUID
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// renumberPlaceholders rewrites `$1`, `$2`, ... in `frag` by adding
// `offset` to each number. Used when concatenating a filter fragment
// (which starts from $1) with outer query args.
func renumberPlaceholders(frag string, offset int) string {
	if offset == 0 {
		return frag
	}
	var b strings.Builder
	i := 0
	for i < len(frag) {
		if frag[i] != '$' {
			b.WriteByte(frag[i])
			i++
			continue
		}
		// Find the run of digits.
		j := i + 1
		for j < len(frag) && frag[j] >= '0' && frag[j] <= '9' {
			j++
		}
		if j == i+1 {
			b.WriteByte('$')
			i++
			continue
		}
		n, _ := strconv.Atoi(frag[i+1 : j])
		b.WriteString(fmt.Sprintf("$%d", n+offset))
		i = j
	}
	return b.String()
}
