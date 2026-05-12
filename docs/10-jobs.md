# 10 — Jobs: queue, cron, workflow

## Background jobs queue

PB добавил background jobs в 0.23+. Railbase делает это first-class.

### Implementation: Postgres-backed queue с `SKIP LOCKED`

`internal/jobs/queue/`. River-style design — нет external Redis, всё в той же Postgres БД, atomic claim через `SELECT ... FOR UPDATE SKIP LOCKED` (efficient row-level locking, available since PG 9.5).

```sql
CREATE TABLE _jobs (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  kind         TEXT NOT NULL,
  args         JSONB NOT NULL,
  priority     SMALLINT NOT NULL DEFAULT 5,
  status       TEXT NOT NULL CHECK (status IN ('pending','running','completed','failed','cancelled')),
  run_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  leased_until TIMESTAMPTZ,
  leased_by    TEXT,                    -- worker_id для debug
  attempts     SMALLINT NOT NULL DEFAULT 0,
  max_attempts SMALLINT NOT NULL DEFAULT 5,
  last_error   TEXT,
  result       JSONB,
  tenant_id    UUID,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  completed_at TIMESTAMPTZ
);

CREATE INDEX _jobs_pending_idx ON _jobs (priority DESC, run_at)
    WHERE status = 'pending';
CREATE INDEX _jobs_running_idx ON _jobs (leased_until)
    WHERE status = 'running';
```

Tenant-scoped queues: `tenant_id` обязательно для tenant-jobs; RLS policy ограничивает worker'ов на их tenant scope (или `app_admin` role для system jobs).

### API surface

#### Go

```go
import "github.com/railbase/railbase/pkg/railbase/jobs"

// Register handler
jobs.Register("send_welcome", func(ctx, args struct{ UserID string }) error {
    user := ...
    return $mailer.Send(...)
})

// Enqueue
jobs.Enqueue(ctx, "send_welcome", args, jobs.Options{
    Priority: 5,
    RunAt: time.Now().Add(5 * time.Minute),
    MaxAttempts: 3,
})

// Cron schedules
jobs.Cron("daily_digest", "0 9 * * *", "send_digest", nil)
```

#### JS hooks

```js
$jobs.enqueue("send_welcome", { userId: "..." }, {
  priority: 5,
  runAt: "+5m",
  maxAttempts: 3,
})

cronAdd("daily_digest", "0 9 * * *", () => {
  // executed by cron worker — same JSVM pool
  $app.dao().findRecordsByFilter("posts", "...").forEach(p => {
    $jobs.enqueue("send_post_digest", { postId: p.id })
  })
})
```

### Worker model

- N workers (default `GOMAXPROCS`), configurable
- **Atomic claim через `SELECT ... FOR UPDATE SKIP LOCKED`**:
  ```sql
  WITH claimed AS (
    SELECT id FROM _jobs
     WHERE status = 'pending' AND run_at <= now()
     ORDER BY priority DESC, run_at
     LIMIT 1
     FOR UPDATE SKIP LOCKED
  )
  UPDATE _jobs SET status = 'running', leased_until = now() + interval '5 min', leased_by = $1, attempts = attempts + 1
   WHERE id IN (SELECT id FROM claimed)
   RETURNING *;
  ```
  Никаких busy-spin; никаких race conditions; multiple workers efficiently parallel
- Lease duration default 5 min (configurable per job kind); auto-renew worker'ом каждые `lease/2`
- На worker crash — lease expires, recovery job помечает `status = 'pending'` и job возвращается в очередь
- Wake workers через `LISTEN railbase_jobs` channel + `pg_notify` после `INSERT INTO _jobs` — отсутствие polling latency, но fallback ticker раз в 1s на случай missed NOTIFY

### Retry & backoff

- Exponential backoff: `base_delay * 2^attempt` (default base 30s, max 1h)
- `max_attempts` reached → status=`failed`, eventbus `job.failed` event
- Manual retry через admin UI или CLI

### Cron jobs

- `robfig/cron/v3` parser
- Persisted в `_cron_schedules` table
- На startup workers pick up schedules
- Manual trigger через admin UI «Run now»
- Pause/resume per schedule

### Job result storage

Optional `result JSON` column для jobs returning data. Default retention 24h (configurable).

### Metrics

- `railbase_jobs_pending`
- `railbase_jobs_running`
- `railbase_jobs_failed_total{kind}`
- `railbase_jobs_duration_seconds{kind, outcome}`
- `railbase_jobs_lease_renewed_total`

### Built-in jobs

Из коробки:

- `_railbase.scheduled_backup` — cron'd backup (если configured)
- `_railbase.audit_seal` — append Ed25519 signature к audit hash chain (если sealing enabled)
- `_railbase.document_retention` — auto-archive expired documents
- `_railbase.thumbnail_generate` — lazy thumbnail generation
- `_railbase.text_extract` — PDF text extraction (если `--documents-extract-text`)
- `_railbase.send_email` — async email send (через mailer)
- `_railbase.export_async` — async export для больших датасетов
- `_railbase.cleanup_sessions` — expired sessions garbage collection
- `_railbase.cleanup_record_tokens` — expired tokens GC
- `_railbase.cleanup_logs` — log retention enforcement

System jobs runners separately registered, не subject of user RBAC.

### Admin UI

См. [12-admin-ui.md](12-admin-ui.md#10-jobs--queue).

---

## Saga / workflow engine — plugin `railbase-workflow`

Не в core — слишком specific. v1.1+ plugin.

### Что делает

Lightweight saga orchestration, inspired by rail's BPMN engine ([flow.bpmn.ts](src/modules/admin/flow/server/flow.bpmn.ts)) но без BPMN authoring.

### API

```go
flow.Define("checkout",
    flow.Step("reserve_inventory", reserveInventory, releaseReserve),  // step + compensation
    flow.Step("charge_payment", chargePayment, refundPayment),
    flow.Step("create_order", createOrder, deleteOrder),
    flow.Step("send_confirmation", sendConfirmation, nil),  // no compensation
)

// Run
flow.Start(ctx, "checkout", input)
```

### Persistence

`_flow_runs` table: state machine с current step, input, intermediate results. На крах process — другой instance picks up, resumes from last committed step.

### JS API

```js
$flow.start("checkout", input)
$flow.status(runId)
$flow.cancel(runId)
```

### Compensation logic

При failure на step N → выполняется compensation для steps N-1, N-2, ..., 1. Saga pattern.

### Что НЕ делает

- BPMN authoring UI (rail имеет, Railbase не делает — слишком сложно)
- Long-running workflows (Temporal-class) — out of scope
- Visual workflow editor — может быть в v2 plugin

### Detailed spec

```go
import "github.com/railbase/railbase-workflow/flow"

flow.Define("checkout").
    Step("reserve_inventory", reserveItems, flow.Compensate(releaseItems)).
    Step("charge_payment", charge, flow.Compensate(refund)).
    Step("create_order", createOrder).
    Step("send_email", sendConfirmation, flow.OnError(flow.Continue))   // non-critical step

flow.Define("approval_required").
    Step("validate_input", validate).
    Wait("manual_approval", 7*24*time.Hour).                          // long-running wait
    Step("execute", performAction)

flow.Define("conditional").
    Step("classify", classify).
    Branch(flow.When("classification == 'high_risk'"),
        flow.Step("escalate", escalate),
    ).
    Branch(flow.Default(),
        flow.Step("auto_approve", autoApprove),
    )
```

### Features

- **Saga pattern**: forward steps + compensations
- **Persisted run state** в `_workflow_runs(id, definition, current_step, state, created, ...)`
- **Resume after crash**: idempotency через step keys; restart picks up at last committed step
- **Timeout per step** + retry policy (configurable)
- **Branching** через condition predicates (использует authority's evaluator engine)
- **Long-running waits**: `flow.Wait("manual_approval", 7*24h)` — паузит до signal или timeout
- **Parallel steps**: `flow.Parallel(step1, step2, step3)` — выполняются concurrently
- **Step dependencies**: DAG форма через `.After("step_name")`

### JS hooks

```js
$flow.start("checkout", input)                          // start run
$flow.signal(runId, "manual_approval", { decision: "approve" })   // resume waiting step
$flow.cancel(runId, { reason: "..." })                  // abort + run compensations
$flow.status(runId)                                      // current state

onWorkflowStarted("checkout", (e) => {...})
onWorkflowStepCompleted((e) => {...})
onWorkflowCompleted((e) => {...})
onWorkflowFailed((e) => {...})
```

### Admin UI (plugin extends core admin UI)

- **Runs list** с filtering (definition, status, time range)
- **Per-run timeline visualization**: steps как nodes с completion status, duration, errors
- **Manual retry/cancel/skip-step** для troubleshooting
- **Workflow definitions browser** (read-only — definitions в Go-коде)
- **Stuck runs alert** (waiting > expected time)

### Integration с Authority plugin

```go
flow.Define("payment_processing").
    Step("validate", validate).
    AuthorityGate("payments", "process").                // блокируется до approval
    Step("execute", executePayment)
```

При hit AuthorityGate:
- Workflow run pauses
- Authority request submitted
- На approve → workflow resumes на next step
- На reject → compensation chain runs

### Не делает

- BPMN authoring (см. главный план — это намеренно не делаем)
- Visual editor (только code-defined)
- Long-term scheduling > 30 days (use jobs queue для этого)
- Cross-tenant workflows (single tenant scope)

### Open questions

- **Workflow versioning**: старые runs продолжают на old definition? Recommend: yes, version pinned at start
- **Retry strategies**: exponential / linear / custom — default config?
- **Resource limits**: max concurrent runs per tenant?

См. plugin entry в [15-plugins.md](15-plugins.md#railbase-workflow).
