# 27 — Tasks: durable human-actionable work queue

> **Статус**: design proposal (v2.0). Drafted 2026-05-16 параллельно с
> [26-authority.md](26-authority.md). Tasks — это primitive, через
> который DoA назначает работу подписантам, но также первоклассный
> embedder API для любого «нужно, чтобы человек что-то сделал».
>
> **Updated 2026-05-16 (same day)**: spec прошёл adversarial critique
> pass — см. [26-27-design-review.md](26-27-design-review.md). Закрыты:
> P2.8 (i18n default → ICU templates rendered at GET), P2.9 (mixed
> assignment clarified), P2.10 (recycle behavior на DoA-spawned tasks),
> P2.7 (force-complete semantics для DoA-tasks).

## Что это и почему отдельно от Jobs / Notifications

В Railbase уже есть **три** различающихся primitive'а для «что-то
должно произойти». Tasks — четвёртый. Разница важна:

| Primitive | Actor | Granularity | Outcome | Stateful |
|---|---|---|---|---|
| **Jobs** ([10](10-jobs.md)) | machine (worker) | один call | success/failure | tx, не дольше |
| **Notifications** ([20](20-notifications.md)) | human (passive read) | one push | fire-and-forget | inbox read state |
| **Webhooks** ([21](21-webhooks.md)) | external system | HMAC-signed call | delivered/failed + retry | dispatch lifecycle |
| **Tasks** | human (active complete) | one work item | completed/cancelled/expired | open → claimed → completed |

**Task** = «человек должен **сделать что-то и закрыть** этот work
item; пока он открыт, состояние системы остаётся в подвешенном
виде». Это не «прочитать сообщение» (нотификация — read state без
side effect) и не «выполнить операцию» (job — машинный actor).

### Use cases

- **DoA approval signature** ([26-authority.md](26-authority.md)) —
  каждый authority request порождает one task per qualified
  signer-role.
- **Manual data review** — embedder помечает row «requires manual
  verification»; кому-то нужно прокликать и approve / mark dirty.
- **Long-running async checkpoint** — job нашёл аномалию, не может
  решить автоматически, эскалирует в task «check this batch
  manually».
- **Onboarding workflow** — пользователь зарегистрировался,
  admin должен provision'ить дополнительные ресурсы → admin task
  «provision storage for new user X».
- **Cleanup nudge** — system нашёл устаревшие записи; task для
  оператора «архивировать или продлить».

### Что НЕ tasks

- **Pure notification** — «backup завершён, ничего делать не нужно».
  Это notification, не task. Если из неё надо что-то сделать —
  тогда task.
- **Async job** — «отправить 10к emails». Это job; задача
  оператора (если что-то пошло не так) — task сверху.
- **Realtime presence** — кто online сейчас. Это session state, не
  durable work.

## Data model

Sys-миграция `0035_tasks` — одна таблица. Schema следует `_*`
системному именованию.

```sql
CREATE TABLE _tasks (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID,                              -- NULL для site-scope
    kind            TEXT NOT NULL,                     -- "authority.sign" / "manual_review" / ...
    title           TEXT NOT NULL,                     -- human-readable summary
    description     TEXT,                              -- detail, optionally markdown
    target_collection TEXT,                            -- "articles"
    target_record_id  UUID,                            -- ссылка на gated/related row
    payload         JSONB NOT NULL DEFAULT '{}'::jsonb,
                                                       -- kind-specific data (auth-req-id, diff, ...)

    -- Assignment: либо конкретный user, либо role pool (любой qualified
    -- актор может claim'ить и выполнить). Не оба одновременно.
    assignee_user_id UUID,
    assignee_role    TEXT,
    CHECK ((assignee_user_id IS NOT NULL) <> (assignee_role IS NOT NULL)),

    -- Lifecycle.
    status          TEXT NOT NULL DEFAULT 'open',
                    -- open / claimed / completed / cancelled / expired
    claimed_by      UUID,                              -- ставится при transition open → claimed
    claimed_at      TIMESTAMPTZ,
    completed_by    UUID,                              -- кто реально закрыл (может ≠ claimed_by)
    completed_at    TIMESTAMPTZ,
    cancelled_at    TIMESTAMPTZ,
    cancellation_reason TEXT,

    -- Origin / scheduling.
    created_by      UUID,                              -- NULL = system-spawned
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    due_at          TIMESTAMPTZ,                       -- optional deadline (UI sort)
    expires_at      TIMESTAMPTZ,                       -- after this → auto-cancel via reaper

    -- Outcome (kind-specific shape).
    result          JSONB,                             -- что completing actor вернул

    CHECK (status IN ('open','claimed','completed','cancelled','expired'))
);

CREATE INDEX idx__tasks_assignee_user_open
    ON _tasks (assignee_user_id, status, due_at)
    WHERE assignee_user_id IS NOT NULL
      AND status IN ('open','claimed');

CREATE INDEX idx__tasks_assignee_role_open
    ON _tasks (tenant_id, assignee_role, status, due_at)
    WHERE assignee_role IS NOT NULL
      AND status IN ('open','claimed');

CREATE INDEX idx__tasks_kind_status
    ON _tasks (kind, status);

-- Per-row audit handled by .Audit() — `_tasks` is registered with
-- Audit() so before/after diff goes into _audit_log_site / _audit_log_tenant.
```

### Assignment модель

**Один user OR одна role**, не оба. Rationale:

- `assignee_user_id` set: task lives in **specific user's inbox**.
  Никто другой её не видит / не закрывает (admin override
  существует).
- `assignee_role` set: task live в **role pool**. Любой пользователь
  с этой ролью может claim'ить (lock-style). После claim становится
  «owned by claimed_by», но другие могут видеть в queue (с пометкой
  «claimed by X»). Если claimed_by ничего не делает в TTL — auto-
  unclaim, возвращается в пул.

Mixed assignment (user + fallback to role) — **v2.1+**. Базовый case
покрывается двумя независимыми tasks.

## Lifecycle (state machine)

```
                       ┌─ cancel (creator/admin) ──→ cancelled
                       │
created ──→ open ──┬──── claim (qualified actor) ────→ claimed
                   │                                       │
                   │                                       ├─ complete ─→ completed
                   │                                       │
                   │                                       ├─ unclaim ──→ open (re-eligible)
                   │                                       │
                   │                                       └─ TTL exceeded ─→ open (auto-unclaim)
                   │
                   └─── TTL exceeded (reaper) ────────────────→ expired
```

- `open` → `claimed`: один актор берёт лок. Mutex per row через
  `UPDATE ... WHERE status='open' AND assignee_role/user matches
  RETURNING ...`. Гонка решается DB-level.
- `claimed` → `open`: explicit unclaim (актор передумал) или
  auto-unclaim после TTL (по умолчанию 1h без `complete` →
  возврат в пул).
- `claimed` → `completed`: успешное завершение. `result` поле
  заполняется.
- `open` / `claimed` → `cancelled`: explicit cancel (creator или
  admin) или transitions сверху (например, authority request
  rejected → все спавненные tasks cancel).
- `open` → `expired`: reaper job, после `expires_at`.

**Inv:** ровно один live state. Completed / cancelled / expired —
terminal.

## API surface

### Embedder / SPA (auth required)

```
GET    /api/tasks/mine?status=open&kind=authority.sign
       → tasks where assignee_user_id = me OR (assignee_role IN my_roles)

POST   /api/tasks/{id}/claim
       → 200 {status: "claimed", claimed_by, claimed_at}
       → 409 если уже claimed кем-то

POST   /api/tasks/{id}/unclaim
       → 200 {status: "open"}
       → 403 если не claimed_by актором

POST   /api/tasks/{id}/complete
       body: {result?: {...}}
       → 200 {status: "completed", ...}
       → 403 если не claimed_by актором (или unclaimed — нужно
              claim сначала)

GET    /api/tasks/{id}
       → детали; видны если актор qualified (user_id match OR role
         match OR creator OR admin)
```

### Embedder server-side (Go + JS)

```go
app.Tasks().Create(ctx, tasks.Spec{
    Kind:            "manual_review",
    Title:           "Verify import batch #423",
    AssigneeRole:    "data_steward",
    TargetCollection: "imports",
    TargetRecordID:  importID,
    Payload:         tasks.Payload{"rows": 4523},
    DueAt:           &dueAt,
})
```

```js
$app.tasks().create({
  kind: "manual_review",
  title: "Verify import batch #423",
  assigneeRole: "data_steward",
  targetCollection: "imports",
  targetRecordId: importId,
  payload: {rows: 4523},
});
```

### Admin (`RequireAdmin`)

```
GET    /api/_admin/tasks?status=open|all&kind=&assignee=
       — все tasks oversight, любые scopes

POST   /api/_admin/tasks/{id}/reassign
       body: {assignee_user_id?: uuid, assignee_role?: string}
       — admin может переназначить
       — auto-cancel'ит текущий claim, если был

POST   /api/_admin/tasks/{id}/force-complete
       body: {reason: string}
       — без participation актора (например, актор уволился);
         audit event 'force_completed'
```

### Generated TS SDK

```ts
rb.tasks.mine({status?, kind?})
rb.tasks.get(id)
rb.tasks.claim(id)
rb.tasks.unclaim(id)
rb.tasks.complete(id, {result?})

// Server-side (импорт из admin handler scope):
app.tasks.create({kind, title, ...})
```

## Интеграция с подсистемами

### DoA ([26-authority.md](26-authority.md)) — главный consumer

Когда `_authority_requests` row создаётся со `status='pending'`:

1. **Спавнятся tasks** per qualified signer-role:
   - `Requires.Min=2` + `DistinctRoles=["editor","chief"]` →
     2 tasks: одна с `assignee_role="editor"`, вторая с
     `assignee_role="chief"`.
   - `kind="authority.sign"`, `payload={authority_request_id: ...,
     action_key: ...}`.
   - `title="Approve {{action}} on {{collection}}/{{record}}"`,
     auto-i18n'ится через ICU template.
   - `target_collection`, `target_record_id` — копируются из
     authority request.
2. **Подпись** в `/api/_authority/requests/{id}/sign` транзитивно
   `tasks.complete()` на соответствующую (role-matching) task с
   `result={decision, signed_at}`.
3. **Терминал** (request approved / rejected / expired / withdrawn)
   — `tasks.cancel(reason=<status>)` на все ещё-открытые
   spawned tasks. Soft-cancel для claimed (5-min grace для claimer
   завершить или release).
4. **Recycle** (P2.10): при `requested_changes` signature — все
   open/claimed spawned tasks ре-set'ятся в `open` (auto-unclaim).
   Текущий claimer теряет lock, получает notification «request
   changed by author; please review again». Editor, который не
   успел подписать в первый раунд, получает второй шанс. Audit
   event `task_recycled` per task. См. [26-authority.md §Lifecycle](26-authority.md).

Это происходит **внутри handler transaction** — task spawn и
authority request create — atomic. Если spawn упал, request тоже
откатывается (DoA без visible task = workflow заблокирован).

**Inverse direction**: actor открывает task `authority.sign` →
admin UI рендерит diff + sign form из payload (без отдельного
fetch). Это shortcut UX, semantic source of truth остаётся в
`_authority_requests`.

### Notifications ([20-notifications.md](20-notifications.md))

- **Task created** → push assignee (user-scoped) или всем actor'ам
  с матчинговой role (role-scoped, deduped per user).
- **Task claimed by other** (для role-scoped) → optional silent
  push «X claimed this task» для информированности пула.
- **Task expires soon** (24h до `expires_at`) → reminder push.
- **Task cancelled** → push current claimer (если есть): «Task
  cancelled by Y, reason: ...».

Routing rules — стандартный `_notifications` pipeline. Recipients
вычисляются on-demand из task assignment.

### Realtime ([05-realtime.md](05-realtime.md))

- `tasks:mine:{user_id}` — live inbox updates.
- `tasks:role:{tenant_id}:{role}` — live role-pool updates
  (используется в admin task queue UI).

### RBAC ([04-identity.md](04-identity.md))

Per-row RBAC predicate helper'ы:
- `tasks.AssignedTo()` — task видна актору если assignee.
- `tasks.QualifiedClaimer()` — актор может claim'ить.

Default rules коллекции `_tasks` (созданы в migration):
- `ListRule`: `assignee_user_id = @request.auth.id OR
   assignee_role IN @request.auth.roles OR created_by =
   @request.auth.id`.
- `ViewRule`: то же.
- `CreateRule`: `false` — только через `$app.tasks().create()`
  API (DSL surface, не raw insert).
- `UpdateRule`: `false` — только через operation endpoints
  (`/claim`, `/complete`, `/unclaim`).
- `DeleteRule`: `false` — никто, ever; lifecycle через cancel.

### Hooks ([06-hooks.md](06-hooks.md))

Новый hook namespace:
- `task.created` — после spawn (после insert + RLS commit).
- `task.claimed` — после успешного claim.
- `task.completed` — после complete.
- `task.cancelled` / `task.expired` — terminal events.

After-only, как и для DoA. Embedder может слушать «task.completed
where kind=='authority.sign'» для side-effects вроде «отправить
автоматическое follow-up письмо».

### Jobs ([10-jobs.md](10-jobs.md))

Builtin: `tasks_reaper` — раз в минуту:
1. Auto-unclaim tasks где `claimed_at` старше unclaim-TTL
   (default 1h).
2. Expire tasks где `expires_at` прошёл.
3. Cancel orphan tasks (target_record_id указывает на удалённый
   row).

### Admin UI ([12-admin-ui.md](12-admin-ui.md))

Новые экраны (v2.0):
1. **My inbox** (`/_/tasks/inbox`) — admin's own task list,
   filter by kind/status/due. Bulk-claim для multi-assignment.
2. **All tasks** (`/_/tasks`) — oversight, filter по всем полям,
   bulk reassign / force-complete.
3. **Task detail** (`/_/tasks/{id}`) — kind-specific renderer.
   `kind='authority.sign'` renders diff + sign form inline
   (без navigating away to authority page). Generic kinds renderят
   payload через JSON tree + completion form.

### CLI ([13-cli.md](13-cli.md))

```
railbase tasks list --kind authority.sign --status open
railbase tasks show <id>
railbase tasks complete <id> --result '{...}'   # для скриптов
railbase tasks reaper-now                        # форсировать reaper для теста
```

### SDK ([11-frontend-sdk.md](11-frontend-sdk.md))

`rb.tasks.*` namespace — стандартный generator. Inbox client
ходит через realtime subscription, не polling — это первоклассный
UX-кейс real-time.

### Testing ([23-testing.md](23-testing.md))

`MockTasks`:
- `app.Tasks().Create(...)` доступна как обычно.
- `MockTasks.LastSpawn()` — для assertions «после X должен был
  заспавниться task с kind=Y».
- `MockTasks.AutoComplete(kind)` — все spawned-with-this-kind
  немедленно completed (для тестов, где actor — embedder, не
  DoA-actor).

## Migration story

Включение tasks для embedder'а — две формы:

1. **System tasks (DoA)** — автоматически при `.Authority()` на
   коллекции. Migration emits `_tasks` system collection если её
   ещё нет, плюс `_authority_*` tables (см. 26).
2. **Embedder-spawned tasks** — embedder вызывает
   `app.Tasks().Create(...)` явно. Это просто API call, никакой
   schema work не требуется.

`migrate up` подкатывает `_tasks` migration независимо от того,
есть ли в schema `.Authority()`-коллекции, потому что embedder
может использовать tasks напрямую без DoA.

## Что не входит в v2.0

- **Subtasks / dependencies** («task X requires task Y first»).
  v2.x — нужен граф зависимостей, нетривиальная UX.
- **Recurring tasks** (как cron, но human actor). Если нужно —
  embedder cron'ом сам спавнит.
- **Templates** (named task definitions для часто повторяющихся
  kind'ов). v2.1+ возможно.
- **SLA tracking / metrics** (sla breach %, mean time to
  complete). v2.x — это observability layer, не core task model.
- **Mixed assignment** (user + role fallback). v2.1+ если есть
  embedder pressure.
- **Time tracking** (сколько актор потратил на task). Out of scope
  — это HR/project-mgmt territory, не core framework.

## Открытые вопросы (фиксация default'ов после design-review)

| Q | Default v2.0 | Эскейп до v2.1 |
|---|---|---|
| **Task descriptions i18n** | `title` и `description` хранятся как **ICU templates** (plaintext с `{var}` placeholders), render at **GET time** через requestLocale (см. P2.8). Embedder регистрирует kind-keyed templates через `app.Tasks().RegisterTemplate(kind, locale, template)` (lookup в `internal/tasks/templates.go`). Преимущества: один template переживает language switching, embedder не дублирует строки per language, совместимо с auto-translate pipeline (translate template once, render N raз). | Per-task snapshot for languages without registered template — v2.1 если embedder pressure. |
| **Mixed assignment** (user + role fallback) | НЕ в v2.0 (см. P2.9). Если нужно — embedder спавнит ДВА task'а: один с `assignee_user_id=X`, второй с `assignee_role=Y`. Completion одной **НЕ** auto-cancel'ит вторую — embedder отвечает за reconciliation через `task.completed` hook (`tasks.cancel(other_id, reason="duplicated work covered")`). | v2.1 — native mixed assignment. |
| **Force-complete на DoA-spawned task** (`kind="authority.sign"`) | Admin force-complete = эквивалент `bypassed` event на underlying authority request (см. P2.7). Request status НЕ auto-approved (force-complete ≠ approval); audit получает `task_force_completed` event с admin id + reason. Если admin хочет именно approve — есть `/_admin/authority/.../bypass`. | — стабильно. |
| **Realtime multi-role dedup** (user с 3 ролями subscribes to 3 channels) | Client-side dedup (поддерживается seen-request-ids set локально). Server fan-out's correctly per channel — не дедупит. | Server-side per-user dedup — v2.x если performance pressure. |
| **Cancellation in-flight claimed tasks** (request терминал-decided когда task already claimed) | Soft cancel: claimer keep'ит lock короткое время + receives notification «underlying request resolved; please conclude or release». TTL короткий (5 мин), потом forced cancel. | — стабильно, можно donastraivать TTL. |
| **Bulk operations** (claim 50 tasks одним запросом) | НЕ в v2.0. Admin UI делает цикл по `/api/tasks/{id}/claim`. | v2.1 — `POST /api/tasks/batch/{claim,unclaim,complete}` если pressure. |
| **Cancelled task retention** | Нет auto-purge в v2.0. Embedder добавляет cron-job, если нужно. См. P3.2. | v2.1 — formal `_settings.tasks.retention_days`. |
| **External system task completion** (webhook callback, e.g. внешняя система подписала) | НЕ в v2.0. Embedder ходит через REST `POST /api/tasks/{id}/complete` с API token. | v2.1 — explicit webhook-callback flow с HMAC. |

## Связанные документы

- [04-identity.md](04-identity.md) — RBAC predicates
- [05-realtime.md](05-realtime.md) — `tasks:mine:*` channels
- [06-hooks.md](06-hooks.md) — `task.*` namespace
- [10-jobs.md](10-jobs.md) — `tasks_reaper`
- [11-frontend-sdk.md](11-frontend-sdk.md) — `rb.tasks.*`
- [12-admin-ui.md](12-admin-ui.md) — inbox + queue screens
- [13-cli.md](13-cli.md) — `railbase tasks`
- [20-notifications.md](20-notifications.md) — task push routing
- [23-testing.md](23-testing.md) — `MockTasks`
- [26-authority.md](26-authority.md) — DoA → tasks spawning
