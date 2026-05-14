package rest

import (
	"context"
	"fmt"

	authmw "github.com/railbase/railbase/internal/auth/middleware"
	"github.com/railbase/railbase/internal/filter"
	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/tenant"
)

// compiledFragment is one parsed-and-compiled rule or user filter.
// Empty Where means "no constraint" — caller should not splice it
// into the SQL at all.
type compiledFragment struct {
	Where string
	Args  []any
}

// composeWhere combines rule + user filter into a single AND'd
// fragment, renumbering placeholders from `startParam`. Either side
// may be empty.
func composeWhere(rule, user compiledFragment, startParam int) (compiledFragment, int) {
	switch {
	case rule.Where == "" && user.Where == "":
		return compiledFragment{}, startParam
	case rule.Where == "":
		return user.shifted(startParam)
	case user.Where == "":
		return rule.shifted(startParam)
	}
	a, n1 := rule.shifted(startParam)
	b, n2 := user.shifted(n1)
	return compiledFragment{
		Where: "(" + a.Where + ") AND (" + b.Where + ")",
		Args:  append(append([]any{}, a.Args...), b.Args...),
	}, n2
}

// shifted returns a copy whose placeholders run from startParam upward.
// Necessary when concatenating two fragments emitted with independent
// $N counters.
func (f compiledFragment) shifted(startParam int) (compiledFragment, int) {
	if f.Where == "" {
		return compiledFragment{}, startParam
	}
	out := f
	// We DON'T need to renumber the SQL string because the filter
	// compiler always starts from the correct param index. This is
	// only a concern when *concatenating* two independently-compiled
	// fragments. So we accept a single shift and let callers compile
	// at the right offset to begin with.
	_ = out
	return f, startParam + len(f.Args)
}

// compileFilter parses+compiles src (a user-supplied ?filter=...) into
// a fragment whose placeholders start at startParam. Returns
// (empty, startParam, nil) when src is empty.
func compileFilter(src string, spec builder.CollectionSpec, ctx filter.Context, startParam int) (compiledFragment, int, error) {
	if src == "" {
		return compiledFragment{}, startParam, nil
	}
	ast, err := filter.Parse(src)
	if err != nil {
		return compiledFragment{}, startParam, fmt.Errorf("filter: %w", err)
	}
	if ast == nil {
		return compiledFragment{}, startParam, nil
	}
	sql, args, next, err := filter.Compile(ast, spec, ctx, startParam)
	if err != nil {
		return compiledFragment{}, startParam, fmt.Errorf("filter: %w", err)
	}
	return compiledFragment{Where: sql, Args: args}, next, nil
}

// compileRule is the rule-specific wrapper. It uses the same compiler
// but expects the rule string is admin-controlled (registered at
// schema declaration time) — a parse failure here is a programming
// error rather than a 400.
//
// startParam threads through the same way as compileFilter so a rule
// + user filter can share placeholders within one query.
//
// SECURITY — empty rule = LOCKED. An empty rule string does NOT mean
// "no constraint"; it means the operation is not exposed on the public
// API at all (the RuleSet contract: "empty string = no rule, server-
// only"). It compiles to a constant-false fragment so List returns
// nothing and View/Update/Delete/Create match no row. There is no
// bypass path — a collection is reachable through the public CRUD API
// only via an explicit rule (e.g. "true" for unconditional access, or
// `@request.auth.id != ''` for any authenticated caller). This is the
// secure-by-default posture: forgetting to set a rule fails closed,
// not open.
func compileRule(rule string, spec builder.CollectionSpec, ctx filter.Context, startParam int) (compiledFragment, int, error) {
	if rule == "" {
		return compiledFragment{Where: "false"}, startParam, nil
	}
	ast, err := filter.Parse(rule)
	if err != nil {
		return compiledFragment{}, startParam, fmt.Errorf("rule on %q: %w", spec.Name, err)
	}
	if ast == nil {
		return compiledFragment{}, startParam, nil
	}
	sql, args, next, err := filter.Compile(ast, spec, ctx, startParam)
	if err != nil {
		return compiledFragment{}, startParam, fmt.Errorf("rule on %q: %w", spec.Name, err)
	}
	return compiledFragment{Where: sql, Args: args}, next, nil
}

// filterCtx builds the magic-var context from the request principal.
func filterCtx(p authmw.Principal) filter.Context {
	if !p.Authenticated() {
		return filter.Context{}
	}
	return filter.Context{
		AuthID:         p.UserID.String(),
		AuthCollection: p.CollectionName,
	}
}

// combineFragments AND'd two compiledFragments. Either may be empty.
// Doesn't shift placeholders — that's the caller's job (compile rule
// at $1, then user filter starting at the rule's nextParam).
func combineFragments(a, b compiledFragment) compiledFragment {
	switch {
	case a.Where == "" && b.Where == "":
		return compiledFragment{}
	case a.Where == "":
		return b
	case b.Where == "":
		return a
	}
	return compiledFragment{
		Where: "(" + a.Where + ") AND (" + b.Where + ")",
		Args:  append(append([]any{}, a.Args...), b.Args...),
	}
}

// composeRowExtras builds the WHERE-suffix used by view/update/delete.
// They share the shape `id = $1 AND <extras>`; extras are: optional
// tenant_id constraint + the operation's rule. Returns a single
// fragment whose placeholders begin at startParam.
func composeRowExtras(ctx context.Context, spec builder.CollectionSpec, fctx filter.Context, rule string, startParam int) (compiledFragment, error) {
	out := compiledFragment{}
	if spec.Tenant && tenant.HasID(ctx) {
		var tf compiledFragment
		tf, startParam = tenantFragment(tenant.ID(ctx).String(), startParam)
		out = combineFragments(out, tf)
	}
	rf, _, err := compileRule(rule, spec, fctx, startParam)
	if err != nil {
		return compiledFragment{}, err
	}
	out = combineFragments(out, rf)
	return out, nil
}

// tenantFragment returns a fragment that constrains tenant_id to the
// given tenantID, using the placeholder `$startParam`. nextParam is
// startParam+1 so the caller can chain.
//
// This is belt-and-braces: the schema generator already emits an RLS
// policy that does the same check at the DB layer. Dev with the
// embedded-postgres superuser bypasses RLS, though, so the application
// must do its own filtering. Production deployments running as a
// non-superuser get both layers; this is by design.
func tenantFragment(tenantID string, startParam int) (compiledFragment, int) {
	return compiledFragment{
		Where: fmt.Sprintf("tenant_id = $%d", startParam),
		Args:  []any{tenantID},
	}, startParam + 1
}
