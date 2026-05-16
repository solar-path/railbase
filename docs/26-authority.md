# 26 — Authority: Delegation of Authority в ядре

> **Статус**: design proposal (v2.0), **rev2** после ysollo comparison.
> Drafted 2026-05-16, rev2 — same day.
>
> **Rev1** (initial draft) использовал schema-as-code модель: правила
> подписи зашиты в Go DSL через `Authority(AuthorityConfig{...})` с
> inline ролями, signatures, conditionals.
>
> **Rev2** — фундаментальный shift к **hybrid schema+matrix-data**
> после изучения ysollo's DoA model (см. translation keys в
> `lib/translations/en.ts`, 248 doa.* ключей). Schema объявляет
> **где** DoA-gate стоит (compile-time); matrix data объявляет
> **какие именно правила** применяются (runtime, edit-time через
> admin UI, версионируется). Embedder = разработчик пишет schema
> единожды; ops admin меняет матрицы без redeploy.
>
> Сохранены из rev1: bypass policy (Decision #1-7), audit chain
> через `target='authority'`, tasks integration, RBAC composition,
> v2.0 scope. Изменены: data model (3 → 6 sys-tables), approver
> types (1 → 4), approval modes (1 → 3 per-level), delegation
> (deferred → first-class), workflow runtime (flat → multi-level
> с escalation), simplification recycle path. Полный diff между
> rev1 и rev2 — в [26-rev1-vs-rev2.md](26-rev1-vs-rev2.md) (будет
> сгенерирован при первом slice'е).
>
> Cross-refs `(см. design-review §P1.N)` или `(ysollo)` помечают
> происхождение каждого design decision'а.

## Что это и зачем в ядре

**Delegation of Authority (DoA)** — primitive «материальное
изменение состояния должно быть санкционировано N независимыми
акторами с distinct ролями / должностями, прежде чем оно
произойдёт».

Не путать с RBAC:

| RBAC | DoA |
|---|---|
| «Может ли пользователь сделать это **сейчас**?» | «Можно ли этому изменению **вообще произойти**?» |
| Gating predicate, синхронный | Workflow, асинхронный |
| Один актор | N≥2 актора (initiator + approvers) |
| Stateless | Stateful (matrix → workflow → decisions) |
| Один чокпоинт (REST handler) | Множественные чокпоинты + явная bypass-политика |

Композируются — DoA-gated action всё равно проходит RBAC-чек
сначала.

### Почему в ядре, не в plugin'е

(unchanged from rev1) — два независимых embedder feedback'а,
невозможность bypass discipline из plugin'а, audit chain
integration требует core-level доступа, аналогия с per-tenant RBAC.

## Two-layer model: schema-as-code + matrix-as-data

Главный архитектурный сдвиг rev2 — разделение **где** DoA от
**какие правила**. Заимствовано из ysollo (см. ysollo-comparison
section ниже).

### Слой 1: Schema declares gate points (compile-time)

Embedder декларирует в Go: «вот эта коллекция/transition требует
санкции, и вот ключ матрицы, по которому искать правила».

```go
var Articles = railschema.Collection("articles").
    Field("title",      railschema.Text().Required()).
    Field("status",     railschema.Select("draft","in_review","published","archived")).
    Field("total_cents", railschema.Number().Int()).

    // ОДНО объявление gate point'а. NO inline rules.
    Authority(railschema.AuthorityConfig{
        On: railschema.AuthorityOn{
            Op:    "update",
            Field: "status",
            To:    []string{"published"},
        },
        Matrix:          "article.publish",      // ← reference на matrix-by-key
        AmountField:     "total_cents",          // optional: для materiality fast-path
        Currency:        "USD",                  // optional: matched against matrix.currency
        ProtectedFields: []string{"title","body","total_cents"},
                                                 // см. design-review §P1.4 — anti-bait-and-switch
        Required:        true,                   // startup-time check: matrix должна существовать
    }).

    // Несколько gate points на одной коллекции — обычное дело.
    Authority(railschema.AuthorityConfig{
        On:       railschema.AuthorityOn{Op: "delete"},
        Matrix:   "article.takedown",
        Required: true,
    })
```

**Schema-сторона теперь почти пустая.** Roles / signatures / amount
ranges / approval modes / escalation timeouts / delegation — НИЧЕГО
из этого в schema нет. Schema только говорит: «здесь DoA-gate;
ищи правила в матрице по этому ключу».

### Слой 2: Matrix data declares actual rules (runtime)

Operator через admin UI создаёт **матрицу** — versioned,
time-bounded, amount-scoped набор правил:

```
Matrix: "article.publish"
  Version 1, Status: approved, Effective: 2026-05-16 → ∞
  Currency: USD, Amount range: [0, ∞)

  Level 1: "Editorial review"
    Mode: any (любой один из approvers)
    Approvers:
      - Type: role,             Ref: "editor"
      - Type: role,             Ref: "senior_editor"
    Escalation: 24 hours → auto-escalate to level 2

  Level 2: "Chief sign-off"
    Mode: any
    Approvers:
      - Type: role,             Ref: "chief"
      - Type: position,         Ref: "editor_in_chief"
    Escalation: 48 hours → auto-reject

  Conditional matrix (separate row, same key, different amount range):
Matrix: "article.publish"
  Version 1, Status: approved
  Amount range: [50000, ∞)             ← high-value content (premium)
  
  Level 1: "Legal review"
    Mode: all (все approvers должны подписать)
    Approvers: [role=legal]
    Escalation: 72 hours

  Level 2: "Chief + Editor combined"
    Mode: threshold (≥2 из 3)
    Approvers: [role=chief, role=senior_editor, role=editor]
```

Матрицы редактируются через `/admin/authority/matrices/edit/{id}`,
имеют **lifecycle**: `draft → approved → archived` (или `revoked`),
с **version history**. Ops admin планирует следующую версию матрицы
наперёд (e.g. «с 1 января действует новый порог $100k»), apply'ит
в `draft`, approves заранее, becomes active автоматически по
`effective_from`.

### Матричный selection algorithm

Когда gate срабатывает на mutation, runtime ищет применимую
матрицу:

```sql
SELECT m.* FROM _doa_matrices m
WHERE m.key = $key                            -- e.g., 'article.publish'
  AND m.status = 'approved'
  AND m.effective_from <= NOW()
  AND (m.effective_to IS NULL OR m.effective_to > NOW())
  AND (m.tenant_id = $tenant OR m.tenant_id IS NULL)   -- prefer tenant-specific
  AND ($amount IS NULL
       OR ((m.min_amount IS NULL OR m.min_amount <= $amount)
           AND (m.max_amount IS NULL OR $amount <= m.max_amount)))
  AND (m.currency IS NULL OR m.currency = $currency)
  AND (m.condition_expr IS NULL OR eval_filter(m.condition_expr, $record) = TRUE)
ORDER BY
  m.tenant_id NULLS LAST,        -- prefer tenant-specific over site-wide
  m.min_amount DESC NULLS LAST   -- prefer more-specific (higher floor) amount range
LIMIT 1;
```

Если match → workflow создаётся по этой матрице. Если no match —
**hard error** (`409 authority: no applicable matrix for this
transition + amount + tenant`) — embedder либо корректирует matrix
data, либо `.Authority(Required: false)` в schema (тогда no match
= no gate, mutation проходит).

## Approver type resolution

Из ysollo — четыре типа approvers, **но в v2.0 реализуются только 2**
(см. [26-org-structure-audit.md](26-org-structure-audit.md) для
обоснования):

| Type | Ref | Resolution logic | v2.0? |
|---|---|---|---|
| `role` | role key (e.g. `"editor"`) | Любой user with this role in current tenant via `_user_roles`. Set of qualified actors. | ✅ |
| `user` | user UUID | Один конкретный user. Set = {user}. | ✅ |
| `position` | position key (e.g. `"editor_in_chief"`) | User assigned to this position в `_org_positions` (tenant-scoped). | ❌ v2.x (требует org-chart primitive, ~2-3 месяца отдельной работы) |
| `department_head` | department key OR auto | Head of department, в котором сейчас сидит requester. Runtime lookup: requester → `_org_members.department_id` → `_org_departments.head_user_id`. | ❌ v2.x |

**v2.0 scope** строго ограничен `role` + `user`. Это покрывает
blogger newsroom (editor / chief через role) и любой role-attribute
workaround для department membership (embedder моделирует
«finance_lead» как role до org-chart). Не покрывает true org-aware
DoA где «head of finance approves» — это honest gap до v2.x.

Resolution возвращает **set of qualified user UUIDs** для уровня.
Этот set определяет:
- Кто видит workflow в inbox (`role:editor` channel → all users with
  role editor; `user:abc` → specific user).
- Кто может signe (filter на `/sign` endpoint: signer ∈ set).
- Кому идут notifications (push per user).

Snapshot (rev2 decision §Open Question 2): set resolution **fixed на
момент create workflow** — не re-evaluated при каждой signature.
Если user X получил role editor сегодня в 10:00 и в 11:00 workflow
создан — он qualified. Если role revoked в 11:30 — он **всё ещё**
qualified для этого workflow (snapshot). Это audit-honest и
race-free.

Resolution возвращает **set of qualified user UUIDs** для уровня.
Этот set определяет:
- Кто видит workflow в inbox (`role:editor` channel → all users with
  role editor; `user:abc` → specific user).
- Кто может signe (filter на `/sign` endpoint: signer ∈ set).
- Кому идут notifications (push per user).

## Workflow lifecycle (multi-level state machine)

```
                                            ┌─ cancel (initiator) ──────────────────→ cancelled
                                            │
created ──→ running (current_level=1) ──┬─── level mode satisfied ──→ running (current_level=2) ──→ ...
                                        │                              │
                                        │                              │ ...последний level OK
                                        │                              ↓
                                        │                            running ──→ consume successful ──→ completed
                                        │                                     ──→ consume rejected by hook ──→ running (P1.7 — request стays approved retry-able)
                                        │                                     ──→ TTL expired (overall) ──→ expired
                                        │
                                        ├─── any decision='reject' on any level ──→ rejected (terminal veto)
                                        │
                                        ├─── escalation timeout on level N (no decision) ──→ auto-promote to level N+1
                                        │                                                  (or auto-reject if no next level — matrix decides)
                                        │
                                        └─── reassign by current approver ─────────→ running (same level, новый approver set)
```

**Inv:** ровно один live state. `current_level` валиден только в
`running`. Completed/rejected/cancelled/expired — terminal.

### Level mode behavior

Каждый level имеет mode (из ysollo):

- **`any`** — один любой approver из set'а подписывает «approved»
  → level satisfied → переход к level N+1. Один любой подписывает
  «reject» → terminal reject.
- **`all`** — **все** approvers из set'а должны подписать
  «approved» (или один «reject» → terminal). Используется для
  high-stakes согласований («все три члена правления»).
- **`threshold`** — нужно ≥ `min_approvals` «approved» подписей.
  Reject обходит threshold check (один reject = terminal). Это
  M-of-N quorum.

### Escalation per level (из ysollo)

Каждый level имеет `escalation_hours` (default null = no escalation).
Если на уровне нет terminal decision в течение этого времени:
- Builtin `authority_escalation_reaper` job (раз в минуту)
  поднимает workflow на следующий level автоматически.
- Audit event `escalated` с `from_level / to_level / reason='timeout'`.
- Если нет следующего level'а — workflow → `expired` или
  `rejected` (matrix.on_final_escalation enum).

### Reassign action (из ysollo)

Approver уровня может reassign request другому qualified
approver: `POST /api/_authority/workflows/{id}/reassign body:{to_user, note}`. Эффект:
- Текущий approver больше не qualifies для этого workflow (его
  task cancels).
- Target user добавляется в approver-set этого уровня (если ещё
  не там) с meta-флагом `assigned_by_reassign=true`.
- Audit event `reassigned`.

Полезно когда level mode=`any`, в пуле 10 actors, текущий «занят»,
передаёт конкретному коллеге.

### Simplified rejection model (rev1 → rev2)

**Убрано из rev1**: `requested_changes` decision + recycle path
(см. rev1 §Lifecycle). Заменено на:
- Approver рejects с comment'ом `"need revisions: <text>"`.
- Workflow terminal — `rejected`.
- Initiator видит rejection + reason → может создать **новый
  workflow** с пересмотренным diff.

Это сильно чище — нет UNIQUE-constraint проблем, нет stale
signatures, нет recycle complexity. Цена: initiator делает второй
mutation request заново вместо «отредактировать тот же». Но это
**более audit-honest** — каждая попытка-санкция = отдельная row в
истории.

Из ysollo translation keys: `doa.workflow.action.reject` существует;
`doa.workflow.action.requested_changes` — **не существует**.
Подтверждает, что ysollo выбрал ту же модель.

## Data model

Sys-миграция `0034_authority` — шесть sys-tables.

### `_doa_matrices` — основная таблица матриц

```sql
CREATE TABLE _doa_matrices (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID,                              -- NULL для site-scope
    key             TEXT NOT NULL,                     -- "article.publish" — references schema .Authority({Matrix: ...})
    version         INTEGER NOT NULL DEFAULT 1,
    name            TEXT NOT NULL,                     -- human-readable, для admin UI
    description     TEXT,

    status          TEXT NOT NULL DEFAULT 'draft',
                    -- draft / approved / archived / revoked
    revoked_reason  TEXT,                              -- required if status=revoked
    approved_by     UUID,
    approved_at     TIMESTAMPTZ,

    effective_from  TIMESTAMPTZ,                       -- inclusive lower bound
    effective_to    TIMESTAMPTZ,                       -- exclusive upper bound; NULL = open-ended

    -- Selection criteria (combined в WHERE).
    min_amount      BIGINT,                            -- nullable; minor units (cents)
    max_amount      BIGINT,                            -- nullable; NULL = no upper bound
    currency        TEXT,                              -- nullable; matched if not null
    condition_expr  TEXT,                              -- optional filter-expression
                                                       -- evaluated против record (post-merge state)

    on_final_escalation TEXT NOT NULL DEFAULT 'expire',
                    -- expire / reject — что делать если на последнем
                    -- level escalation timeout

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by      UUID,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    CHECK (status IN ('draft','approved','archived','revoked')),
    CHECK (on_final_escalation IN ('expire','reject')),
    CHECK (effective_to IS NULL OR effective_from < effective_to),
    CHECK (min_amount IS NULL OR max_amount IS NULL OR min_amount <= max_amount)
);

-- Partial unique: на (tenant, key, version) ровно одна matrix.
CREATE UNIQUE INDEX uniq__doa_matrices_version
    ON _doa_matrices (COALESCE(tenant_id::text, ''), key, version);

-- Index для matrix selection (главный hot path).
CREATE INDEX idx__doa_matrices_selection
    ON _doa_matrices (key, status, effective_from, effective_to)
    WHERE status = 'approved';
```

### `_doa_matrix_levels` — конфигурация уровней

```sql
CREATE TABLE _doa_matrix_levels (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    matrix_id       UUID NOT NULL REFERENCES _doa_matrices(id) ON DELETE CASCADE,
    level_n         INTEGER NOT NULL,                  -- 1, 2, 3, ...
    name            TEXT NOT NULL,                     -- "Editorial review", "Chief sign-off"

    mode            TEXT NOT NULL,                     -- any / all / threshold
    min_approvals   INTEGER,                           -- required if mode='threshold'

    escalation_hours INTEGER,                          -- nullable; NULL = no escalation
    -- on_escalation handled at matrix-level (on_final_escalation)

    CHECK (mode IN ('any','all','threshold')),
    CHECK (mode != 'threshold' OR min_approvals IS NOT NULL),
    CHECK (level_n > 0),
    UNIQUE (matrix_id, level_n)
);
```

### `_doa_matrix_approvers` — кто approver per level

```sql
CREATE TABLE _doa_matrix_approvers (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    level_id        UUID NOT NULL REFERENCES _doa_matrix_levels(id) ON DELETE CASCADE,
    approver_type   TEXT NOT NULL,
                    -- role / user / position / department_head
    approver_ref    TEXT NOT NULL,
                    -- role key OR user UUID OR position key OR department key
    auto_resolve    BOOLEAN DEFAULT FALSE,
                    -- для department_head: TRUE = "head of requester's own department"

    CHECK (approver_type IN ('role','user','position','department_head'))
);

CREATE INDEX idx__doa_matrix_approvers_level
    ON _doa_matrix_approvers (level_id);
```

### `_doa_workflows` — runtime instance

```sql
CREATE TABLE _doa_workflows (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID,

    matrix_id       UUID NOT NULL REFERENCES _doa_matrices(id),
    matrix_version  INTEGER NOT NULL,                  -- snapshot for audit

    -- What triggered this workflow.
    collection      TEXT NOT NULL,
    record_id       UUID NOT NULL,
    action_key      TEXT NOT NULL,                     -- derived from Schema (`<collection>.<Name>`)
    requested_diff  JSONB NOT NULL,                    -- normative content of the sanction (см. design-review §P1.4)
    amount          BIGINT,                            -- copied from record at create time
    currency        TEXT,
    initiator_id    UUID NOT NULL,
    notes           TEXT,

    -- Lifecycle.
    status          TEXT NOT NULL DEFAULT 'running',
                    -- running / completed / rejected / cancelled / expired
    current_level   INTEGER,                           -- valid only when status='running'

    -- Cancellation / rejection / expiry metadata.
    terminal_reason TEXT,                              -- required for rejected/cancelled/expired
    terminal_by     UUID,
    terminal_at     TIMESTAMPTZ,

    -- Consumption — set after handler write completes.
    consumed_at     TIMESTAMPTZ,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,              -- overall TTL (from matrix или default 7d)

    CHECK (status IN ('running','completed','rejected','cancelled','expired'))
);

-- Не больше одного running workflow на (record, action) одновременно.
CREATE UNIQUE INDEX uniq__doa_workflows_active
    ON _doa_workflows (collection, record_id, action_key)
    WHERE status = 'running';

CREATE INDEX idx__doa_workflows_initiator
    ON _doa_workflows (initiator_id, status, created_at DESC);

CREATE INDEX idx__doa_workflows_inbox
    ON _doa_workflows (tenant_id, status, current_level, expires_at)
    WHERE status = 'running';
```

### `_doa_workflow_decisions` — per-approver decisions

```sql
CREATE TABLE _doa_workflow_decisions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workflow_id     UUID NOT NULL REFERENCES _doa_workflows(id) ON DELETE CASCADE,
    level_n         INTEGER NOT NULL,                  -- какой уровень
    approver_id     UUID NOT NULL,
    approver_role   TEXT,                              -- snapshot of role at sign time
    approver_resolution TEXT,
                    -- "role:editor" / "user:abc" / "delegate_of:xyz" / "reassigned_by:abc"

    -- Forward-compat v2.x (см. 26-org-structure-audit.md) — snapshot
    -- approver's org context at sign time. Nullable, zero cost в v2.0
    -- (всегда NULL без org-chart primitive); additive для v2.x.
    approver_position TEXT,                            -- v2.x: snapshot of position key
    approver_org_path TEXT,                            -- v2.x: snapshot of department path
                                                       --       (e.g. "company/finance/treasury")
    approver_acting   BOOLEAN,                         -- v2.x: signed via acting/interim assignment?

    decision        TEXT NOT NULL,
                    -- approved / rejected
    memo            TEXT,                              -- approval memo (из ysollo)
    decided_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    CHECK (decision IN ('approved','rejected')),
    UNIQUE (workflow_id, level_n, approver_id)
);

CREATE INDEX idx__doa_workflow_decisions_workflow
    ON _doa_workflow_decisions (workflow_id);
```

### `_doa_delegations` — first-class delegation (из ysollo)

```sql
CREATE TABLE _doa_delegations (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID,

    delegator_id    UUID NOT NULL,                     -- who delegates authority
    delegate_id     UUID NOT NULL,                     -- who receives it

    -- Scope: всё / только specific matrix / только specific document type.
    scope           TEXT NOT NULL DEFAULT 'all',
                    -- all / matrix / document_type / (v2.x:) org_path
    scope_ref       TEXT,                              -- matrix key OR document_type
                                                       -- v2.x: department path для scope='org_path' (e.g. "company/finance")
    -- Forward-compat: scope='org_path' добавляется в v2.x когда org-chart primitive landed
    -- (см. 26-org-structure-audit.md). v2.0 — только 'all' / 'matrix' / 'document_type'.

    -- Limits.
    max_amount      BIGINT,                            -- delegate cannot approve beyond this
    currency        TEXT,

    -- Time bounds.
    effective_from  TIMESTAMPTZ NOT NULL,
    effective_to    TIMESTAMPTZ NOT NULL,

    -- Sub-delegation.
    allow_sub_delegation BOOLEAN NOT NULL DEFAULT FALSE,

    -- Lifecycle.
    status          TEXT NOT NULL DEFAULT 'active',
                    -- active / expired / revoked
    revoked_at      TIMESTAMPTZ,
    revoked_by      UUID,
    revoked_reason  TEXT,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    CHECK (scope IN ('all','matrix','document_type')),  -- v2.x add: 'org_path'
    CHECK (scope = 'all' OR scope_ref IS NOT NULL),
    CHECK (status IN ('active','expired','revoked')),
    CHECK (effective_from < effective_to),
    CHECK (delegator_id != delegate_id)               -- nobody delegates to themselves
);

CREATE INDEX idx__doa_delegations_active
    ON _doa_delegations (tenant_id, delegate_id, status, effective_from, effective_to)
    WHERE status = 'active';
```

При resolution approvers для workflow — runtime проверяет active
delegations:
1. `delegator_id` входит в qualified set для этого level?
2. Delegation scope матчит? (all / matrix.key / document_type)
3. Delegation effective сейчас?
4. Amount workflow'а ≤ delegation.max_amount?
→ Если все 4 — `delegate_id` добавляется в qualified set с
`approver_resolution="delegate_of:{delegator_id}"`.

Sub-delegation (allow_sub_delegation=TRUE): delegate может создать
свой delegation третьему лицу, но max_amount ограничен исходным
лимитом, scope не может быть шире исходного.

### `_authority_audit` — sealed chain (unchanged from rev1)

```sql
CREATE TABLE _authority_audit (
    id            BIGSERIAL PRIMARY KEY,
    tenant_id     UUID,
    workflow_id   UUID,                                -- nullable для matrix-management events
    matrix_id     UUID,                                -- nullable для workflow events
    event         TEXT NOT NULL,
        -- workflow.{created, decided, escalated, reassigned, cancelled, rejected, expired, consumed, bypassed}
        -- matrix.{drafted, approved, revoked, archived, version_bumped}
        -- delegation.{created, revoked, expired}
    actor_id      UUID,
    payload       JSONB NOT NULL,
    occurred_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    prev_hash     BYTEA NOT NULL,
    row_hash      BYTEA NOT NULL                       -- SHA-256(prev_hash || canonical(row))
);
```

`_audit_seals` (см. [19-unified-audit.md](19-unified-audit.md))
расширяется `target='authority'` — те же Ed25519 seals.

## API surface

### Matrix management (admin, `RequireAdmin`)

```
GET    /api/_admin/authority/matrices
       — list матриц с filter (key, status, tenant)

GET    /api/_admin/authority/matrices/{id}
       — full matrix с levels + approvers

POST   /api/_admin/authority/matrices
       body: {key, name, currency?, min_amount?, max_amount?, levels[]}
       → 201; matrix создаётся в status='draft'

PATCH  /api/_admin/authority/matrices/{id}
       — edit draft matrix; approved/archived/revoked — immutable

POST   /api/_admin/authority/matrices/{id}/approve
       body: {effective_from, effective_to?}
       → 200; status → approved; от этого момента starts matching new workflows

POST   /api/_admin/authority/matrices/{id}/revoke
       body: {reason: <required>}
       → 200; status → revoked; existing running workflows продолжают со своей snapshot'нутой version

POST   /api/_admin/authority/matrices/{id}/version
       — duplicate matrix как version+1 в status='draft' для редактирования next version

GET    /api/_admin/authority/matrices/{id}/versions
       — version history
```

### Workflow operations (user, principal-authenticated)

```
GET    /api/authority/workflows/mine
       — workflows where principal is initiator OR qualified approver
       filters: status, role, collection

GET    /api/authority/workflows/{id}
       — full state: matrix snapshot, decisions per level, current_level,
         expires_at, SLA countdown

POST   /api/authority/workflows/{id}/approve
       body: {memo?}
       → 200 if decision recorded; 403 если principal не qualifies

POST   /api/authority/workflows/{id}/reject
       body: {reason: <required>}
       → 200; workflow terminal

POST   /api/authority/workflows/{id}/cancel
       body: {reason?}
       → 200; initiator-only; workflow terminal

POST   /api/authority/workflows/{id}/reassign
       body: {to_user_id, note?}
       → 200; principal должен быть current qualified approver

POST   /api/authority/workflows/{id}/comment
       body: {text}
       → 200; добавляет comment в audit без decision

GET    /api/authority/queue?role=editor&collection=articles
       — workflows where principal qualifies (через role / user / delegation)
```

### Delegation management

```
GET    /api/authority/delegations/mine
       — где principal либо delegator, либо delegate

POST   /api/authority/delegations
       body: {delegate_id, scope, scope_ref?, max_amount?, currency?,
              effective_from, effective_to, allow_sub_delegation?}
       → 201

POST   /api/authority/delegations/{id}/revoke
       body: {reason: <required>}
       → 200

GET    /api/authority/delegations/{id}
```

### Workflow creation (происходит automatically от gate)

Workflow создаётся **handler'ом** при 409 — embedder не вызывает
`POST /workflows/create` явно. Handler возвращает структурированный
envelope, SDK ловит и UX'ом предлагает создать workflow:

```json
{
  "error": {
    "code": "authority_required",
    "message": "this transition requires approval",
    "details": {
      "matrix_key": "article.publish",
      "matrix_id": "<uuid>",
      "level_count": 2,
      "estimated_lead_time_hours": 72,
      "create_url": "/api/authority/workflows",
      "suggested_body": {
        "collection": "articles",
        "record_id": "<row-id>",
        "action_key": "articles.publish",
        "diff": {...},
        "amount": 0,
        "currency": "USD"
      }
    }
  }
}
```

```
POST   /api/authority/workflows
       body: {collection, record_id, action_key, diff, amount?, currency?, notes?}
       → 201 {workflow_id, current_level, expires_at, ...}
       → 409 если уже есть running workflow на эту (collection, record, action)
```

## Matrix key namespacing (plugin lifecycle integration)

Matrix `key` живёт в одном глобальном (per-tenant) namespace. С
plugin'ами это означает потенциальные коллизии: embedder `articles`
collection + plugin `railbase-cms` тоже пытается зарегистрировать
`articles.*` matrices. Решение — **three-tier ownership rule**:

| Tier | Pattern | Кто владеет | Enforcement |
|---|---|---|---|
| Core | `system.*` | Railbase core (audit, compliance built-ins) | Reserved hard — никто другой не может registerать |
| Plugin | `<plugin-id>.*` | Plugin регистрирует свой namespace на install | Strict — plugin не может писать вне своего prefix; на install проверяется conflict с уже-installed plugins |
| Embedder | bare (e.g. `articles.publish`) OR `app.*` (opt-in) | Default — bare names matching collection naming convention. `app.*` доступен как opt-in для embedder'ов с многими plugins, где визуальная disambiguation важна | Soft — registry validates on schema register, ловит conflict с зарегистрированными plugin namespaces |

### Примеры

```go
// Core (только Railbase code):
.Authority({Matrix: "system.tenant.delete"})         // GDPR right-to-erasure

// Plugin railbase-billing:
.Authority({Matrix: "billing.refund.large"})         // refund > $1k
.Authority({Matrix: "billing.subscription.cancel"})

// Embedder (default bare):
.Authority({Matrix: "articles.publish"})             // newsroom
.Authority({Matrix: "expenses.approve"})             // financial domain

// Embedder (opt-in app.* для disambiguation в complex install):
.Authority({Matrix: "app.articles.publish"})
```

### Conflict detection

**На schema register** (embedder):
- Берём `Matrix` ключ из каждого `.Authority({...})` declaration
- Сверяем prefix против реестра installed plugins
- Если prefix matches plugin → registry error: «matrix key `billing.foo`
  conflicts with installed plugin `railbase-billing`; rename to
  bare `<entity>.<verb>` или используйте `app.<entity>.<verb>`»

**На plugin install**:
- Berём `<plugin-id>` из plugin manifest
- Сверяем против already-registered embedder matrix keys
- Если bare embedder key starts with `<plugin-id>.` → install warns
  с migration suggestion («embedder owns `billing.refund` key;
  install plugin `railbase-billing` would shadow it — rename
  embedder's matrix to `app.billing.refund` first»)
- Hard-fail если conflict не resolved

### Plugin uninstall с running workflows

Plugin зарегистрировал `billing.refund.large` matrix, есть running
workflows (`status='running'`) ссылающиеся на этот matrix. Uninstall
поведение:

**Default (strict)**: uninstall **отказывается** пока есть active
workflows из plugin namespace:

```
$ railbase plugin uninstall railbase-billing
✗ Cannot uninstall: 3 active workflows depend on plugin matrices.
  - workflow X (matrix billing.refund.large, pending level 1)
  - ...
  Either wait for completion or cancel them explicitly.
```

**Opt-in (`--force --archive-workflows`)**: cancel all running
workflows с `terminal_reason='plugin_uninstalled'`, audit event per
workflow, **затем** uninstall. Audit chain ловит — admin
явно ответственен за решение.

**Не делается в v2.0**: «disable plugin but keep handlers registered»
вариант — это complex enough что отдельный design pass.

### Why этот compromise

Embedder bare-names — соответствует Railbase convention (PocketBase
heritage: bare collection names, `_*` reserved). Не вводит ceremony
для simple case (single-tenant project без plugins). `app.*` opt-in
оставляет escape hatch для complex installs. `<plugin-id>.*` strict
— защищает plugin authors от accidental embedder collision.

См. [15-plugins.md](15-plugins.md) для plugin namespace ownership
detail.

---

## Bypass policy (unchanged from rev1, Decisions #1-#7)

Все 7 bypass-decisions сохраняются:
1. Версия — v2.0 major
2. Site-admin UI — НЕТ bypass; emergency takedown через `/_admin/.../bypass`
3. JS/Go hooks — НЕТ bypass; explicit `$app.authority()` API
4. Job/cron — partial: scheduled job — это уже approved action
5. Bulk import — bypass через explicit CLI флаг `--bypass-authority --reason "..."`
6. Migration / DDL — bypass всегда
7. Raw SQL через `app.Pool()` — bypass (защита через audit-seal)

(Полный текст — см. rev1, без изменений.)

## Интеграция с existing подсистемами

### RBAC ([04-identity.md](04-identity.md))

DoA-gate стоит **после** RBAC. Семантика как в rev1 — RBAC даёт
«permission писать в эту строку», DoA даёт «permission делать
именно этот transition». Helpers:
- `authority.QualifiedSigner(workflow_id)` — RBAC predicate.

### Audit chain ([19-unified-audit.md](19-unified-audit.md))

Расширение `target='authority'` в `_audit_seals`. События:
- `workflow.created / decided / escalated / reassigned / cancelled / rejected / expired / consumed / bypassed`
- `matrix.drafted / approved / revoked / archived / version_bumped`
- `delegation.created / revoked / expired`

### Hooks ([06-hooks.md](06-hooks.md))

Новые hook events (after-only):
- `authority.workflow.created` — workflow spawned
- `authority.workflow.decided` — каждое decision (approve / reject)
- `authority.workflow.level_advanced` — переход к next level
- `authority.workflow.escalated` — auto-escalation timeout
- `authority.workflow.consumed` — write actually went through
- `authority.matrix.approved` — matrix activated
- `authority.delegation.created` — new delegation

JS API (legitimate workflow creation programmatically):
```js
$app.authority.workflow().create({
  collection: "articles", record_id, action_key: "articles.publish",
  diff: {...}, amount: 0
});
```

Go: `app.Authority().Workflow().Create(ctx, ...)`.

### Tasks ([27-tasks.md](27-tasks.md))

Каждый level workflow'а порождает tasks per qualified approver
(user-resolution из role/user/position/department_head + applicable
delegations). Decision на workflow → completes matching tasks.

**Recycle behavior removed** (rev1 simplified): не нужно — reject
теперь terminal, новый workflow ре-spawn'ит fresh tasks.

### Notifications ([20-notifications.md](20-notifications.md))

Per-level routing:
- `workflow.created` → push qualified approvers level 1.
- `level_advanced` → push qualified approvers level N+1.
- `decided` → push initiator (level-by-level).
- `escalated` → push next-level approvers + supervisor.
- `expired/rejected` → push initiator.

User digest preferences применяются — single-user-with-many-pending
получает one dilution «5 approvals waiting» вместо 5 push'ей.

### Realtime ([05-realtime.md](05-realtime.md))

- `authority:workflow:{id}` — live updates конкретного workflow (timeline view).
- `authority:queue:role:{role}` — live inbox per role.
- `authority:queue:user:{user_id}` — personal inbox.

### Jobs ([10-jobs.md](10-jobs.md))

Builtin jobs:
- `authority_escalation_reaper` — раз в минуту проверяет workflows
  с истёкшим level escalation_hours, переводит на next level
  (или final escalation handler).
- `authority_expiry_reaper` — раз в минуту переводит running с
  expires_at < now() в `expired`.
- `authority_chain_seal` — расширение audit_seal builtin'а через
  `target='authority'`.
- `authority_delegation_expirer` — раз в час переводит delegations
  с истёкшим effective_to в `expired`.

### Tenant scope ([04-identity.md](04-identity.md))

- Matrices могут быть site-scope (tenant_id NULL) или tenant-scope.
- Workflows всегда tenant-scope (наследует от collection).
- Tenant-scoped workflow может match'ить site-scope matrix — fallback
  «нет своей — берём общую». Selection algorithm prefers tenant-specific.
- Delegations всегда tenant-scope (нет cross-tenant delegations в v2.0).

### CLI ([13-cli.md](13-cli.md))

```
railbase authority matrix list / show / create / approve / revoke / version
railbase authority workflow list / show
railbase authority delegation list / show / create / revoke
railbase authority audit verify
railbase authority bypass <workflow-id> --reason "..."   # emergency
```

### Admin UI ([12-admin-ui.md](12-admin-ui.md))

Из ysollo translation keys (`doa.*`):
- `/_/authority/matrices` — список матриц с filter
- `/_/authority/matrices/new`, `/_/authority/matrices/{id}` — full editor (multi-level, multi-approver UI)
- `/_/authority/matrices/{id}/versions` — version diff view
- `/_/authority/workflows` — site-wide oversight queue
- `/_/authority/workflows/{id}` — workflow detail (timeline, SLA countdown, decisions, transaction details)
- `/_/authority/inbox` — admin's personal inbox
- `/_/authority/delegations` — list + create
- `/_/authority/audit?target=authority` — chain integrity

### Testing infrastructure ([23-testing.md](23-testing.md))

```go
mock := testing.NewMockAuthority(t)
mock.AddMatrix(testing.Matrix{
    Key: "article.publish",
    Levels: []testing.Level{
        {Mode: "any", Approvers: []testing.Approver{{Type: "role", Ref: "editor"}}},
    },
})
mock.AutoApprove("article.publish")  // для тестов где DoA не объект теста
mock.RequireNoBypass(t)               // assert helper
```

## Migration story

### Adding `.Authority()` to live collection

Schema migration emits informational header:
```sql
-- AUTHORITY ADDED: collection "articles", gate-point "publish"
-- (matrix key: "article.publish")
-- This migration ONLY declares the gate-point. You must also create
-- the matrix data (via admin UI at /_/authority/matrices/new or via
-- `railbase authority matrix create`) BEFORE this gate activates.
-- Without an applicable matrix, all gated transitions will 409 hard.
```

Существующие rows: gate triggers **только на новые transition events**,
не на прошлые data states (см. rev1).

### Creating first matrix

После schema migration — operator идёт в admin UI:
1. Создаёт matrix `article.publish` в `draft`
2. Конфигурирует levels + approvers
3. `approve` matrix → effective immediately
4. Gate активирован

`Required: true` в schema = startup-time check: bootstrap fails если
applicable matrix не существует для каждого gated action_key.
Защищает от deploy-without-matrix-data race.

### Single-admin install (см. rev1 Decision #2)

`.Authority()` collection требует, чтобы существовала matrix с
≥1 level с ≥1 approver. Если matrix configured to require role
"editor" и в системе никого с этой ролью нет — startup gate fails
с явным message.

## Что не входит в v2.0

- **Acknowledgment workflow** (ysollo `documents.ack.*`) — отдельный
  primitive «user должен подтвердить, что прочитал document». v2.1+.
- **Document access propagation** (ysollo `documents.access.via*`) —
  context-inherited access (через chat/meeting/task). v2.x —
  требует entity-relationship graph.
- **Sub-request workflows** (recursive DoA — approval требует
  своего sub-approval). v2.1+.
- **External signer integration** (DocuSign-style cryptographic
  signatures). v2.x+.
- **Cross-tenant matrix sharing** (site legal approve'ит child
  tenant). v2.1 через explicit `cross_tenant_approvers` matrix flag.
- **Approval-on-behalf** (PA подписывает за chief'а без
  delegation). Запрещён в v2.0; легитимный flow — `_doa_delegations`
  + role-of-PA.

## Открытые вопросы — закрыты 2026-05-16

После rev2 переписки + org-structure audit все 4 truly-open questions
получили зафиксированные ответы:

### 1. `condition_expr` на matrix — opt-in, nullable, feature-flagged

**Решение**: `_doa_matrices.condition_expr TEXT NULL` остаётся в
schema, но **не часть happy-path v2.0 core**. Включается opt-in
когда embedder pressure требует конкретно — 80% case'ов покрываются
комбинацией `key` + `amount range` + `currency` + `tenant`. Когда
включается — evaluator против **post-merge state** (`merge(current_row,
requested_diff)`), reuse RBAC filter compiler (no новой sandboxing
работы). Implication: 6.1.7 (plan §6.1) теперь conditional task —
реализуется только если concrete embedder request до v2.0 code freeze.

### 2. Approver resolution timing — snapshot only

**Решение**: всё approver resolution делается на момент **workflow
create**. Snapshot хранится в `_doa_workflow_decisions`:
- `approver_role` — какая роль у actor'а была на момент signing
- `approver_resolution` — путь через который actor стал qualified
  («role:editor», «user:abc», «delegate_of:xyz»)
- `approver_position` (forward-compat v2.x) — снимок position title
- `approver_org_path` (forward-compat v2.x) — снимок department path
- `approver_acting` (forward-compat v2.x) — был ли actor в acting
  capacity

Dynamic resolution отверг по причинам: ломает audit honesty,
делает historical replay ambiguous, создаёт race при re-org,
делает impossible deterministic approval-chain reconstruction.

### 3. Matrix immutability после `approved` — fully immutable

**Решение**: `_doa_matrices` row с `status='approved'` **полностью
неизменяема**. Любая правка → создать `version+1` в `status='draft'`,
edit её, потом `approve` (которая deactivates previous version).
`PATCH /api/_admin/authority/matrices/{id}` возвращает `409 matrix is
approved, only drafts can be edited; create version+1 first`.

Реализация: `_doa_matrices` уже имеет `(key, version)` unique pair;
selection algorithm уже фильтрует `status='approved'`. В-flight
workflows ссылаются на `matrix_version` snapshot — изменение
matrix не влияет на running workflows. Все три простоты сразу.

### 4. `position` + `department_head` approver types — НЕ в v2.0

**Решение**: v2.0 DoA имеет только `role` + `user` approver types.
`position` + `department_head` deferred до **v2.x** параллельно с
отдельным org-chart primitive, которого нет в roadmap.

См. [26-org-structure-audit.md](26-org-structure-audit.md) для
полного аудита existing capabilities. TL;DR: Railbase сейчас
flat RBAC (`_user_roles` flat list per tenant); `railbase-orgs`
plugin (designed, не имплементирован) тоже не покрывает
departments/positions/hierarchy. Org-chart — отдельный primitive
~2-3 месяца focused work. Не в scope v2.0.

**Cost of this decision**: blogger newsroom flow (role-based,
editor + chief) полностью покрыт. Sentinel ERP financial chain
(department-aware «head of finance approves expense in finance
department») **не покрыт** — там approver type = `role` + condition
«requester is in same department as approver» работает только
если department membership доступен; runtime context dependency
вне DoA scope.

Workaround для sentinel-class кейсов: embedder моделирует
department membership через role attribute (`role: "finance_lead"`)
до того, как org-chart primitive landed.

## Сравнение с rev1

| Аспект | Rev1 | Rev2 |
|---|---|---|
| Rules location | In schema (`.Authority(AuthorityConfig{...})` с inline rules) | Schema declares gate point; matrix-data declares rules |
| Editing rules | Migration + redeploy | Admin UI, no code change |
| Approver types | `Roles []string` only | `role / user / position / department_head` |
| Approval modes | Global `Min` | Per-level: `any / all / threshold` |
| Materiality | `Conditional[].When` filter expr | First-class amount range + currency + optional condition_expr |
| Workflow shape | Flat N-of-M | Multi-level с per-level mode |
| Escalation | Global TTL | Per-level escalation_hours + final-escalation handler |
| Delegation | Deferred v2.1+ | First-class, v2.0 |
| `requested_changes` | First-class recycle | Removed — reject is terminal, fresh workflow |
| Reassign | None | First-class workflow action |
| Versioning | None on rules | First-class matrix versions с lifecycle |
| Bulk preflight | Separate endpoint | Removed — стандартный queue filter |
| Sys-tables | 3 | 6 |
| Embedder write effort | High (rule logic в коде) | Low (just gate-point declaration) |
| Ops admin effort | None (всё в коде) | Higher (matrix editor UI) |

Главный win: **policy management отделён от schema management**.
Compliance/ops teams могут менять authority structure без developer
involvement. Schema-side остаётся узким и стабильным.

## Связанные документы

- [03-data-layer.md](03-data-layer.md) — Schema DSL fundamentals
- [04-identity.md](04-identity.md) — RBAC composition
- [06-hooks.md](06-hooks.md) — `authority.*` hook namespace
- [10-jobs.md](10-jobs.md) — `authority_*_reaper` builtins
- [12-admin-ui.md](12-admin-ui.md) — matrix editor + workflow detail
- [14-observability.md](14-observability.md) — audit chain
- [15-plugins.md](15-plugins.md) — почему НЕ в plugin
- [16-roadmap.md](16-roadmap.md) — v2.0 schedule
- [19-unified-audit.md](19-unified-audit.md) — sealed chain `target='authority'`
- [20-notifications.md](20-notifications.md) — push routing
- [23-testing.md](23-testing.md) — `MockAuthority`
- [26-27-design-review.md](26-27-design-review.md) — rev1 critique
  pass; references `(см. design-review §P1.N)` помечают rev1
  findings, которые перешли в rev2 без изменений
- [27-tasks.md](27-tasks.md) — DoA → tasks spawning, inbox
