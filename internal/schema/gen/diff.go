package gen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/railbase/railbase/internal/schema/builder"
)

// Diff is the result of comparing two Snapshots. Categories are kept
// separate so the migration generator can render them in a sensible
// order (creates first, alters next, drops last) and so callers can
// detect "no changes" without parsing SQL.
type Diff struct {
	NewCollections     []builder.CollectionSpec
	DroppedCollections []string

	// FieldChanges has one entry per collection that exists in both
	// snapshots and has at least one added or dropped field.
	FieldChanges []FieldChange

	// IndexChanges captures user-declared index additions/drops on
	// existing collections. Implicit indexes (auto-FK, FTS, .Indexed)
	// are re-emitted via fieldIndexes when their owning column changes,
	// not tracked here.
	IndexChanges []IndexChange

	// AuthToggles captures Collection ↔ AuthCollection transitions.
	// FEEDBACK #B1 — the auth-injected columns (email, password_hash,
	// verified, token_key, last_login_at) live on the spec.Auth flag
	// rather than spec.Fields, so a plain field-by-field diff missed
	// the toggle entirely. The migration now emits the three-step
	// backfill pattern for each auth column on toggle-on, and DROP
	// COLUMN statements on toggle-off.
	AuthToggles []AuthToggle

	// IncompatibleChanges are diff hits we refuse to auto-handle in
	// v0.2 (column type change, .Tenant() toggle, FK action change,
	// etc.). The CLI prints them and aborts so the user can write a
	// hand-rolled migration.
	IncompatibleChanges []string
}

// AuthToggle records a Collection ↔ AuthCollection transition on a
// pre-existing collection. NewState == true means the collection
// became Auth; false means it lost Auth.
type AuthToggle struct {
	Collection string
	NewState   bool
}

// FieldChange enumerates the per-column adds/drops on one collection.
// Renamed columns currently surface as a Drop + Add pair — rename
// detection is a v0.3 concern.
type FieldChange struct {
	Collection string
	Added      []builder.FieldSpec
	Dropped    []string
}

// IndexChange captures user-declared composite index changes.
type IndexChange struct {
	Collection string
	Added      []builder.IndexSpec
	Dropped    []string
}

// Empty reports whether the diff carries any change at all.
func (d Diff) Empty() bool {
	return len(d.NewCollections) == 0 &&
		len(d.DroppedCollections) == 0 &&
		len(d.FieldChanges) == 0 &&
		len(d.IndexChanges) == 0 &&
		len(d.AuthToggles) == 0 &&
		len(d.IncompatibleChanges) == 0
}

// HasIncompatible is a fast check whether the user must edit the
// generated SQL by hand. CLI uses this to decide between exit 0 and
// non-zero after surfacing the warnings.
func (d Diff) HasIncompatible() bool { return len(d.IncompatibleChanges) > 0 }

// Compute compares prev (last applied snapshot) with curr (current
// in-memory schema registry). The diff is computed by name —
// renames look like drop+add and v0.2 does not try to reconcile.
func Compute(prev, curr Snapshot) Diff {
	prevByName := indexByName(prev.Collections)
	currByName := indexByName(curr.Collections)

	var d Diff

	// New collections (in curr, not in prev).
	for _, c := range curr.Collections {
		if _, ok := prevByName[c.Name]; !ok {
			d.NewCollections = append(d.NewCollections, c)
		}
	}

	// Dropped collections (in prev, not in curr).
	for _, c := range prev.Collections {
		if _, ok := currByName[c.Name]; !ok {
			d.DroppedCollections = append(d.DroppedCollections, c.Name)
		}
	}

	// Existing collections — diff fields and indexes.
	for _, currColl := range curr.Collections {
		prevColl, ok := prevByName[currColl.Name]
		if !ok {
			continue
		}
		fc := diffFields(prevColl, currColl, &d)
		if len(fc.Added) > 0 || len(fc.Dropped) > 0 {
			d.FieldChanges = append(d.FieldChanges, fc)
		}
		ic := diffIndexes(prevColl, currColl)
		if len(ic.Added) > 0 || len(ic.Dropped) > 0 {
			d.IndexChanges = append(d.IndexChanges, ic)
		}
		if prevColl.Tenant != currColl.Tenant {
			d.IncompatibleChanges = append(d.IncompatibleChanges,
				fmt.Sprintf("collection %q: .Tenant() toggle is not auto-migratable in v0.2 (RLS policies + tenant_id column would need a hand-rolled migration)",
					currColl.Name))
		}
		// FEEDBACK #B1 — detect Collection ↔ AuthCollection toggle.
		// The auth-injected system columns aren't in spec.Fields, so a
		// plain field diff missed the change silently and `migrate diff`
		// reported "schema unchanged". The blogger project hit this
		// exactly: schema.Collection("authors") → schema.AuthCollection
		// produced no migration, the embedder had to hand-roll one.
		if prevColl.Auth != currColl.Auth {
			d.AuthToggles = append(d.AuthToggles, AuthToggle{
				Collection: currColl.Name,
				NewState:   currColl.Auth,
			})
		}
	}

	// Determinism for testing / human review.
	//
	// NewCollections: topological order by FK dependency, so the
	// generated migration's CREATE TABLE statements resolve FKs at
	// emit time. Alphabetical order (the v0.3 behaviour) regularly
	// produced broken migrations — Sentinel had to manually swap
	// users → projects → tasks (its `1000_initial_schema.up.sql:1-3`
	// carries an explicit "reviewed: reordered from alphabetical"
	// comment). Topo-sort eliminates that papercut.
	//
	// Cycle case: if collections form a cycle (FK A→B, B→A), we
	// fall back to alphabetical with the cycle members trailing,
	// because Postgres can't CREATE the cycle in one shot anyway —
	// the operator must split into multi-step migration (CREATE
	// without FK, ALTER ADD FK afterwards). We surface the cycle as
	// an IncompatibleChange so the human notices.
	d.NewCollections = topoSortCollections(d.NewCollections, &d)
	sort.Strings(d.DroppedCollections)
	sort.Slice(d.FieldChanges, func(i, j int) bool {
		return d.FieldChanges[i].Collection < d.FieldChanges[j].Collection
	})
	sort.Slice(d.IndexChanges, func(i, j int) bool {
		return d.IndexChanges[i].Collection < d.IndexChanges[j].Collection
	})
	sort.Slice(d.AuthToggles, func(i, j int) bool {
		return d.AuthToggles[i].Collection < d.AuthToggles[j].Collection
	})
	sort.Strings(d.IncompatibleChanges)
	return d
}

// SQL renders the diff into ordered SQL statements suitable for a
// .up.sql migration file. The order is:
//   1. CREATE TABLE for new collections (with all their indexes/RLS).
//   2. ALTER TABLE ADD COLUMN for added fields, in collection order.
//   3. ALTER TABLE DROP COLUMN for dropped fields, in collection order.
//   4. CREATE INDEX for added user indexes.
//   5. DROP INDEX for dropped user indexes.
//   6. DROP TABLE for removed collections (last so any FK from them
//      to non-dropped tables is gone before we try to drop the parent).
func (d Diff) SQL() string {
	var b strings.Builder

	for _, c := range d.NewCollections {
		fmt.Fprintf(&b, "-- create collection %q\n", c.Name)
		b.WriteString(CreateCollectionSQL(c))
		b.WriteString("\n")
	}

	// Auth-flag toggles are emitted BEFORE the field diff for that
	// collection so the three-step ALTER for `email`/`password_hash`/…
	// lands first. Field additions on the same collection (e.g. a new
	// `display_name` column) follow afterwards.
	for _, at := range d.AuthToggles {
		fmt.Fprintf(&b, "-- toggle auth on %q: NewState=%v\n", at.Collection, at.NewState)
		b.WriteString(AuthToggleSQL(at.Collection, at.NewState))
		b.WriteString("\n")
	}

	for _, fc := range d.FieldChanges {
		for _, f := range fc.Added {
			fmt.Fprintf(&b, "-- add %q.%q\n", fc.Collection, f.Name)
			b.WriteString(AddColumnSQL(fc.Collection, f))
			b.WriteString("\n")
		}
	}
	for _, fc := range d.FieldChanges {
		for _, name := range fc.Dropped {
			fmt.Fprintf(&b, "-- drop %q.%q\n", fc.Collection, name)
			b.WriteString(DropColumnSQL(fc.Collection, name))
			b.WriteString("\n")
		}
	}

	for _, ic := range d.IndexChanges {
		for _, idx := range ic.Added {
			fmt.Fprintf(&b, "-- add index %q on %q\n", idx.Name, ic.Collection)
			b.WriteString(indexStmt(ic.Collection, idx))
			b.WriteString("\n\n")
		}
	}
	for _, ic := range d.IndexChanges {
		for _, name := range ic.Dropped {
			fmt.Fprintf(&b, "-- drop index %q on %q\n", name, ic.Collection)
			fmt.Fprintf(&b, "DROP INDEX IF EXISTS %s;\n\n", name)
		}
	}

	for _, name := range d.DroppedCollections {
		fmt.Fprintf(&b, "-- drop collection %q\n", name)
		b.WriteString(DropCollectionSQL(name))
		b.WriteString("\n")
	}

	return b.String()
}

// topoSortCollections returns collections in FK-dependency order:
// a collection that has FKs to others is emitted AFTER its targets.
// The FK graph is built from each FieldSpec's RelatedCollection
// (covers TypeRelation; TypeRelations is M2M via a junction table
// and doesn't impose a CREATE-time ordering on the parent).
//
// Self-references and FKs to collections NOT in the new-set (e.g.
// referencing a pre-existing table) are ignored — neither blocks
// CREATE order.
//
// Tie-break: alphabetical at each topological level, so the output
// is deterministic for tests + human review.
//
// Cycle handling: Kahn's algorithm leaves nodes with non-zero
// in-degree after the queue drains; we append them alphabetically
// and record an IncompatibleChange so the operator splits the
// migration manually (one-shot CREATE of an FK cycle is impossible
// in plain Postgres).
func topoSortCollections(in []builder.CollectionSpec, d *Diff) []builder.CollectionSpec {
	if len(in) <= 1 {
		return in
	}
	// Index for O(1) "is this collection in the new-set?"
	inNew := make(map[string]struct{}, len(in))
	for _, c := range in {
		inNew[c.Name] = struct{}{}
	}
	// Build the FK graph: edge target → source (so we sort sources
	// AFTER targets). Self-references and external refs are ignored.
	indegree := make(map[string]int, len(in))
	adj := make(map[string][]string, len(in)) // target → []source
	for _, c := range in {
		indegree[c.Name] = 0 // initialise so nodes without FKs participate
	}
	for _, c := range in {
		for _, f := range c.Fields {
			if f.Type != builder.TypeRelation {
				continue
			}
			target := f.RelatedCollection
			if target == "" || target == c.Name {
				continue // self-FK doesn't constrain CREATE order
			}
			if _, ok := inNew[target]; !ok {
				continue // external FK — target already exists
			}
			adj[target] = append(adj[target], c.Name)
			indegree[c.Name]++
		}
	}
	// Initial queue: nodes with no incoming edges, alphabetical.
	var ready []string
	for name, deg := range indegree {
		if deg == 0 {
			ready = append(ready, name)
		}
	}
	sort.Strings(ready)

	byName := indexByName(in)
	var out []builder.CollectionSpec
	emitted := make(map[string]struct{}, len(in))
	for len(ready) > 0 {
		n := ready[0]
		ready = ready[1:]
		out = append(out, byName[n])
		emitted[n] = struct{}{}
		// Relax outgoing edges; collect new zero-indegree nodes,
		// re-sort alphabetically each round so cross-level ties
		// stay deterministic.
		var newReady []string
		for _, dep := range adj[n] {
			indegree[dep]--
			if indegree[dep] == 0 {
				newReady = append(newReady, dep)
			}
		}
		sort.Strings(newReady)
		ready = append(ready, newReady...)
	}
	if len(out) == len(in) {
		return out
	}
	// Cycle: append unsorted remainder alphabetically + flag.
	var remainder []builder.CollectionSpec
	var cycleNames []string
	for _, c := range in {
		if _, done := emitted[c.Name]; done {
			continue
		}
		remainder = append(remainder, c)
		cycleNames = append(cycleNames, c.Name)
	}
	sort.Slice(remainder, func(i, j int) bool { return remainder[i].Name < remainder[j].Name })
	sort.Strings(cycleNames)
	d.IncompatibleChanges = append(d.IncompatibleChanges,
		fmt.Sprintf("collections %v form an FK cycle; one-shot CREATE TABLE can't satisfy a circular FK in Postgres. Split into two steps: CREATE without the FK, then ALTER ADD FOREIGN KEY in a follow-up migration.",
			cycleNames))
	return append(out, remainder...)
}

// --- internal helpers ---

func indexByName(specs []builder.CollectionSpec) map[string]builder.CollectionSpec {
	out := make(map[string]builder.CollectionSpec, len(specs))
	for _, s := range specs {
		out[s.Name] = s
	}
	return out
}

func diffFields(prev, curr builder.CollectionSpec, d *Diff) FieldChange {
	prevByName := make(map[string]builder.FieldSpec, len(prev.Fields))
	for _, f := range prev.Fields {
		prevByName[f.Name] = f
	}
	currByName := make(map[string]builder.FieldSpec, len(curr.Fields))
	for _, f := range curr.Fields {
		currByName[f.Name] = f
	}

	fc := FieldChange{Collection: curr.Name}

	for _, f := range curr.Fields {
		if _, ok := prevByName[f.Name]; !ok {
			fc.Added = append(fc.Added, f)
		}
	}
	for _, f := range prev.Fields {
		if _, ok := currByName[f.Name]; !ok {
			fc.Dropped = append(fc.Dropped, f.Name)
		}
	}
	// Type / constraint changes on existing fields aren't auto-handled
	// in v0.2. Surface them as incompatible so the user knows to take
	// action.
	for _, c := range curr.Fields {
		p, ok := prevByName[c.Name]
		if !ok {
			continue
		}
		if p.Type != c.Type {
			d.IncompatibleChanges = append(d.IncompatibleChanges,
				fmt.Sprintf("collection %q field %q: type change %s → %s requires a hand-rolled migration in v0.2",
					curr.Name, c.Name, p.Type, c.Type))
		}
	}

	sort.Slice(fc.Added, func(i, j int) bool { return fc.Added[i].Name < fc.Added[j].Name })
	sort.Strings(fc.Dropped)
	return fc
}

func diffIndexes(prev, curr builder.CollectionSpec) IndexChange {
	prevByName := make(map[string]builder.IndexSpec, len(prev.Indexes))
	for _, i := range prev.Indexes {
		prevByName[i.Name] = i
	}
	currByName := make(map[string]builder.IndexSpec, len(curr.Indexes))
	for _, i := range curr.Indexes {
		currByName[i.Name] = i
	}

	ic := IndexChange{Collection: curr.Name}
	for _, i := range curr.Indexes {
		if _, ok := prevByName[i.Name]; !ok {
			ic.Added = append(ic.Added, i)
		}
	}
	for _, i := range prev.Indexes {
		if _, ok := currByName[i.Name]; !ok {
			ic.Dropped = append(ic.Dropped, i.Name)
		}
	}
	sort.Slice(ic.Added, func(i, j int) bool { return ic.Added[i].Name < ic.Added[j].Name })
	sort.Strings(ic.Dropped)
	return ic
}
