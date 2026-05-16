# 26 — Org-structure audit for DoA approver types

> **Дата**: 2026-05-16. Bounded audit (~30 min) для закрытия v2.0 DoA
> blocker A: «есть ли в Railbase org-structure для `position` /
> `department_head` approver types?»
>
> **TL;DR**: нет. Reality для v2.0 — `role` + `user` only.
> `position` / `department_head` deferred до **v2.x** параллельно с
> отдельным org-chart primitive, который ещё не задизайнен.

## Что Railbase имеет сейчас (2026-05-16, v1.7.49)

### RBAC — flat per-tenant model

| Table | Содержимое | Hierarchy? |
|---|---|---|
| `_roles` | Каталог ролей (id, name, scope) | ❌ Flat list. Roles не nested. |
| `_role_actions` | Role → permission action keys | ❌ — |
| `_user_roles` | User → role assignment (site OR tenant scope) | ❌ Flat. Один user может иметь N ролей, no precedence model |
| `_admins` | Site-admin users | — |
| `_users` (collection) | App users — flat list per tenant | ❌ |

**Implication**: user has roles, period. No notion of «position» distinct
from role; no «manager of X» relationship.

### Tenant — flat model, no parent/child

| Table | Hierarchy? |
|---|---|
| `tenant_id` UUID column на каждой tenant-scoped table | ❌ Flat. UUID — opaque tenant identifier. Нет parent_tenant_id. |

**Implication**: cross-tenant approval (site legal → child tenant) —
не имеет structural representation. Site-scope = `tenant_id IS NULL`,
tenant-scope = `tenant_id = X`. Между ними — flat division, не tree.

### SCIM (v1.7.45)

| Table | Содержимое |
|---|---|
| `_scim_users` / `_scim_groups` | External IdP-managed users + groups |
| `_scim_group_members` | Group membership (flat) |
| Group→role mapping в `_scim_group_roles` | Groups assigned to RBAC roles |

**Hierarchy?** Нет. SCIM groups могут быть nested **в IdP**, но Railbase
flattens их при sync — каждый group membership стирается до `(user_id,
group_id)` пары. Org-chart inheritance не моделируется.

### `railbase-orgs` plugin — designed, не имплементирован

[docs/15-plugins.md §railbase-orgs](15-plugins.md) описывает:
- `organizations` table (name, slug, settings, billing context)
- `organization_members` (user_id, org_id, role_id, status)
- Invite lifecycle, seats, ownership transfer
- 38-role catalog seed для `--template saas-erp`

**Hierarchy?** Plugin design **не упоминает** departments / positions /
reports-to. Это organization-as-tenant-with-billing, не organization-as-org-chart.

Даже когда `railbase-orgs` будет имплементирован, он **не покрывает**:
- Department membership
- Position titles distinct from roles
- Reporting line / manager-of relationship
- Acting / interim head designation
- Multi-head departments (co-leadership)
- Historical org snapshots (как выглядела структура полгода назад)

## Что было бы нужно для `position` + `department_head` approver types

Отдельный primitive («org-chart subsystem»), не описан нигде в plan.md
/ docs/16-roadmap.md. Минимум:

```sql
CREATE TABLE _org_departments (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    parent_id UUID REFERENCES _org_departments(id),  -- tree, может быть NULL для root
    key TEXT NOT NULL,                                -- "finance", "legal", "editorial"
    name TEXT NOT NULL,
    ...
);

CREATE TABLE _org_positions (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    department_id UUID NOT NULL REFERENCES _org_departments(id),
    key TEXT NOT NULL,                                -- "cfo", "editor_in_chief", "head_of_legal"
    name TEXT NOT NULL,
    is_head BOOLEAN DEFAULT FALSE,                    -- this position heads its department
    ...
);

CREATE TABLE _org_assignments (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    user_id UUID NOT NULL,
    position_id UUID NOT NULL REFERENCES _org_positions(id),
    effective_from TIMESTAMPTZ NOT NULL,
    effective_to TIMESTAMPTZ,                         -- NULL = current
    is_acting BOOLEAN DEFAULT FALSE,                  -- interim assignment
    ...
);

-- Plus: org snapshots для historical replay (rev2 §Open Question 2 closure)
CREATE TABLE _org_snapshots (
    snapshot_id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    captured_at TIMESTAMPTZ NOT NULL,
    payload JSONB NOT NULL                            -- serialized org tree
);
```

Plus admin UI for org-chart editing (drag-drop tree, position assignment,
acting/interim flag), migration tooling, snapshot management.

**Effort estimate**: 2-3 месяца focused work. Это **сопоставимо** с
v2.0 DoA subsystem'ом. Делать оба одновременно — over-commit.

## Решение

**v2.0 DoA** ограничен двумя approver types:
- `role` — works today via `_user_roles`
- `user` — direct UUID reference

**v2.x** добавляет:
- `position` — через `_org_positions` + `_org_assignments`
- `department_head` — runtime lookup `requester → _org_assignments → _org_departments.head_id`

Timing **v2.x** — после того, как отдельный org-chart primitive
landed. Это пока **не запланировано** в plan.md / roadmap. Когда
embedder pressure появится (sentinel financial chain, large org
deployments) — открываем design pass для org-chart, тогда добавляем
эти approver types в DoA.

## Forward compatibility — что нужно зафиксировать в v2.0 data model сейчас

Хотя `position` / `department_head` не реализуются, чтобы v2.x
migration был **additive**, не **breaking**, нужно сейчас зарезервировать
space в `_doa_workflow_decisions`:

```sql
CREATE TABLE _doa_workflow_decisions (
    ...
    approver_role     TEXT,                          -- snapshot of role at sign time (v2.0)
    approver_resolution TEXT,                        -- "role:editor" / "user:abc" / "delegate_of:xyz"
    -- Forward-compat for v2.x org-aware approvers (see 26-org-structure-audit.md):
    approver_position TEXT,                          -- snapshot of position key at sign time (v2.x+)
    approver_org_path TEXT,                          -- snapshot of department path (e.g. "company/finance/treasury")
    approver_acting   BOOLEAN,                       -- whether sign was via acting/interim assignment
    ...
);
```

Эти 3 nullable columns — zero cost в v2.0 (всегда NULL), позволяют v2.x
наполнять их без table-altering migration. Snapshot decision из rev2
§Open Question 2 закрывается consistently.

Также — для `_doa_delegations` нужно зарезервировать на v2.x:

```sql
ALTER TABLE _doa_delegations ADD COLUMN scope_org_path TEXT;   -- v2.x: "delegate во все approvals в department finance"
```

## Связанные документы

- [04-identity.md](04-identity.md) — RBAC primitives (используются для `role` approver type)
- [15-plugins.md §railbase-orgs](15-plugins.md) — designed plugin для organizations entity
- [26-authority.md](26-authority.md) — main DoA spec (rev2)
- [26-27-design-review.md](26-27-design-review.md) — critique pass
- [plan.md §6.1](../plan.md) — v2.0 task breakdown
