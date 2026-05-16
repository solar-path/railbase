# 26 + 27 — Design review pass

> **Статус**: adversarial critique. Drafted 2026-05-16 после написания
> [26-authority.md](26-authority.md) + [27-tasks.md](27-tasks.md) тем же
> автором, тот же день. Цель — найти дыры до начала кода, когда исправление
> стоит правки docs, а не разбора production-инцидента.
>
> **Updated 2026-05-16 (same day)**: все 10 P1 findings + 6 P2 default'ов
> зафиксированы обратно в main spec. См. секцию «Закрытые findings»
> в конце документа. Review остаётся как traceable reference от
> finding → fix — каждая правка в spec помечена `(см. design-review §P1.N)`.
>
> **Second update — после ysollo comparison + rev2 spec rewrite**:
> docs/26 был переписан в hybrid schema+matrix-data model. Часть
> P1/P2 findings стали неактуальны (потому что underlying design
> assumption изменился). Маркировка:
> - 🔄 **Translated** — finding по-прежнему актуален, но точка
>   приложения сместилась (например, P1.1 action_key derivation
>   теперь касается matrix `key`, не AuthorityConfig).
> - ❌ **Obsoleted** — finding не применим в rev2 (например, P1.10
>   `requested_changes` recycle — recycle вообще удалён в rev2,
>   reject стал terminal).
> - ✅ **Carried forward** — finding по-прежнему точно применим
>   и зафиксирован в rev2 (например, P1.4 consume validation
>   через ProtectedFields).
>
> **Severity**:
> - **P1** — must-fix перед началом кода. Реальная семантическая дыра,
>   которая сломает либо security, либо state consistency, либо UX до
>   неюзабельности.
> - **P2** — should-clarify. Не блокер, но первый embedder упрётся.
> - **P3** — polish / nice-to-have / явно out-of-v2.0.

## P1 — must-fix перед кодом

### P1.1 — Action key derivation не специфицирован

[26.md](26-authority.md) пишет: `action_key TEXT NOT NULL, -- "articles.publish" derived from .On (collection + transition fingerprint)`. Но **как** именно derive — не сказано.

Кейс, который ломается: одна коллекция, несколько `.Authority()` с
разными `On.From` но одинаковым `On.To`:

```go
Articles.
    Authority(railschema.AuthorityConfig{
        On: railschema.AuthorityOn{Field: "status",
            From: []string{"draft"},     To: []string{"published"}},
        Requires: AuthoritySignatures{Min: 1, Roles: []string{"editor"}},
    }).
    Authority(railschema.AuthorityConfig{
        On: railschema.AuthorityOn{Field: "status",
            From: []string{"archived"},  To: []string{"published"}},  // republish
        Requires: AuthoritySignatures{Min: 2, DistinctRoles: []string{"editor","chief"}},
    })
```

Оба эмитят `articles.publish`? Тогда partial unique index
`(collection, record_id, action_key) WHERE status='pending'` блокирует
любой второй scenario, а requirement-matching при сейте использует
неправильное правило.

**Решение**: action_key = `<collection>.<field>.<from-list-hash>.<to-list-hash>` или explicit `Name` поле в `AuthorityConfig`, обязательное при наличии второго `.Authority()` на коллекции (validator pass'ит single-call коллекции, требует Name при ≥2). Sample: `articles.publish` vs `articles.republish`.

**Где править**: [26-authority.md](26-authority.md) §Schema DSL + §Data model.

### P1.2 — Множественные `.Authority()` с overlapping `On.To` не валидируются

Связано с P1.1. Если две конфиги перекрываются по `To`:

```go
Authority(AuthorityConfig{On: AuthorityOn{To: []string{"published"}}})
Authority(AuthorityConfig{On: AuthorityOn{To: []string{"published","archived"}}})
```

Какая применяется на `status → published`? Spec говорит «несколько `.Authority()` с разными `On.Field`/`On.To`» — это намёк, не enforcement.

**Решение**: registry-time validator падает с явным message «Authority configurations on collection X have overlapping target states {…}; either merge into one config with Conditional rules, or split into disjoint `On.From` regions». Эмитируем чёткую validation error при `Register()`, не runtime panic.

**Где править**: новый `internal/schema/builder/authority_validate.go` + тест.

### P1.3 — Conditional `When` против какого state evaluate'ся

```go
Conditional: []AuthorityRule{
    {When: "is_premium = true", Requires: ...},
}
```

Какое значение `is_premium` — pre-write (row state on disk), post-write (после применения diff), или из самого diff? Critical для materiality: non-premium статья **становится** premium в одной операции + publish — нужен ли legal-approve?

**Default, который надо зафиксировать**: evaluate против **post-write state** (т.е. `merge(current_row, requested_diff)`). Это «материальность результата», не «материальность текущего состояния». Альтернатива (pre-write) даёт лазейку: «опубликуй сначала, потом сделай premium» обходит conditional.

**Где править**: [26-authority.md](26-authority.md) §AuthorityConfig поля — добавить explicit «`When` evaluated against post-merge state».

### P1.4 — Consume validation отсутствует — approve A, write B

Lifecycle gap. Editor approves request с `requested_diff = {status: "published"}` в T+0. В T+30s handler приходит, проверяет approved-request есть, помечает consumed, и пишет **что угодно**: `{status: "published", body: "<exfiltrated PII>"}`. Approval санкционировал смену status, но handler написал больше.

**Решение**: `Authority.Gate()` при consume проверяет, что **actual write field-by-field** matches `requested_diff` для полей, которые были в diff'е. Поля вне diff — свободно меняются (это не материальный actor). Mismatch → 409 «approved diff does not match attempted write» + audit event.

Это превращает DoA из "session-bound capability" в "claim-bound capability". Approval не выдаёт permission «делать что хочешь сейчас» — он выдаёт permission «применить именно этот diff».

**Где править**: [26-authority.md](26-authority.md) §Lifecycle + §Data model (добавить, что requested_diff — нормативное содержимое санкции, не справочное).

### P1.5 — DELETE не покрыт

Spec — про field transitions. Но newsroom-кейс «опубликованная статья содержит ошибку — нужно убрать» — это **DELETE** (или soft-delete), и его тоже хочется gate'ить approval'ом. То же для accounting: delete posted JE → требует chief.

Текущая `AuthorityConfig` строится вокруг `On.Field` — нет способа сказать «при DELETE».

**Решение**: добавить `On.Op` discriminator: `Op: "delete"` (или `"insert"`, `"update"`, `"delete"`). Default — `"update"` плюс field-transition match. Когда `Op="delete"`, поля From/To/Field игнорируются.

```go
Authority(AuthorityConfig{
    On: AuthorityOn{Op: "delete"},
    Requires: AuthoritySignatures{Min: 1, Roles: []string{"chief"}},
})
```

Soft-delete коллекции — gate'ить флаг `deleted_at IS NULL → NOT NULL` (через field-transition trick), или новый `Op="archive"` явный. Решение: `Op="delete"` покрывает обе формы — handler знает соответствующее физ. поведение коллекции.

**Где править**: [26-authority.md](26-authority.md) §Schema DSL + §AuthorityConfig поля + §«Не входит в v2.0» убрать «delete approvals» если там было.

### P1.6 — INSERT vs transition — implicit, надо сделать explicit

Spec говорит «transition» — но что про создание row сразу в gated-state? Embedder делает `POST /articles {status: "published"}`. У строки нет `From` (она не существовала). По духу spec'а — это **должно** требовать санкции (создание published-article без approval), но `On.From` empty list = «any» интерпретируется как «любой существующий», а не «включая несуществование».

**Решение**: explicit — `On.IncludeInsert bool` default `false`. Когда true — INSERT с `status ∈ To` тоже triggered DoA. Альтернатива: добавить `Op="insert"` (P1.5).

**Где править**: [26-authority.md](26-authority.md) §AuthorityConfig поля.

### P1.7 — Hook reject после DoA approval — state drift

Pipeline: RBAC → DoA gate → BeforeUpdate hook → DB write → AfterUpdate hook → DoA mark consumed.

Hook может `Reject()` в before-update. Тогда: DoA уже approved + consumed-attempt начался, но DB write не произошёл, hook отбил. Что с request status'ом?
- Если оставляем `consumed` — врёт audit chain (записал «consumed», write не было).
- Если откатываем в `approved` — request снова «live», следующий attempt пройдёт без re-approval.
- Если переводим в `expired` — теряется approval, ре-submit нужен.

**Решение**: `consumed` помечается **after successful write**, не до. Если hook отбил — request остаётся `approved`, эмитим audit event `consume_attempted_rejected_by_hook` с указанием hook name. Approval не теряется (это легитимный flow: «approved + написать пытались, но hook закрыл по data validation»).

Bonus: это превращает hook'и в **defense-in-depth** post-DoA, без подрыва санкции.

**Где править**: [26-authority.md](26-authority.md) §Lifecycle (явно описать что consume = success-only).

### P1.8 — requested_diff staleness между approve и consume

Approver подписывает в T+0 (`requested_diff = {status: "published"}`, row title="foo"). В T+10 кто-то редактирует title→"bar" (RBAC разрешает, DoA не gated этот diff). В T+30 requester жмёт publish — handler видит approved-request, mark'ит consumed, пишет `status: "published"`. Эффект: статья опубликована с title="bar", а approver одобрял title="foo".

**Решение — два variant'а**:

A. **Strict** — `requested_diff` snapshot'ит **полное состояние row** на момент создания. Consume проверяет, что non-diff поля row тоже не изменились с тех пор. Если изменились — 409 «row drifted since approval; resubmit». Tight, но дорого (большой snapshot, частые refresh).

B. **Loose** — `requested_diff` сериализует только сами меняемые поля + явный `protected_fields []string` в `AuthorityConfig` («материальные поля» — title для published-article, amount для JE). Consume проверяет, что protected fields не drifted.

**Default**: B, потому что A провоцирует частые false-positives при collaboration. Embedder декларирует, что для него «материально»:

```go
Authority(AuthorityConfig{
    On: AuthorityOn{Field:"status", To:[]string{"published"}},
    ProtectedFields: []string{"title", "body", "is_premium"},
    ...
})
```

Изменения title/body/is_premium **между approve и consume** инвалидируют санкцию.

**Где править**: [26-authority.md](26-authority.md) §AuthorityConfig поля (новое `ProtectedFields`) + §Lifecycle.

### P1.9 — Bulk operations + DoA — undocumented

Batch PATCH (v1.4.13) меняет N rows одним запросом. Что, если 30 из 100 — под `.Authority()`?

**Решение**: atomic-fail-on-mixed. Любой row, требующий санкции — весь batch отбит 409 с списком record_ids + suggested per-row authority request creation. Альтернатива (per-row partial commit) подрывает atomicity, которую batch обещает.

```json
{
  "error": {
    "code": "authority_required",
    "message": "batch contains 30 records requiring approval",
    "details": {
      "blocked_records": [{"id": "...", "action_key": "articles.publish"}, ...],
      "auto_approved_records_skipped": true
    }
  }
}
```

Embedder либо submit'ит N authority requests сначала, либо разбивает batch.

**Где править**: [26-authority.md](26-authority.md) §API surface + новый раздел §Batch interaction.

### P1.10 — `requested_changes` decision + UNIQUE blocking re-sign

`_authority_signatures` имеет `UNIQUE (request_id, signer_id)`. Editor подписывает с `decision='requested_changes'` (нужны правки). Requester правит. Тот же editor хочет подписать снова — теперь `approved`. UNIQUE падает.

**Решения**:
- A. Разрешить multiple signatures с UNIQUE on `(request_id, signer_id, decision)`. Last-wins при подсчёте кворума? Тогда `requested_changes` остаётся в audit но не блокирует.
- B. Mutable `decision` на signature row — UPDATE при re-sign.
- C. Сбросить весь request в `pending`-fresh при `requested_changes`: удалить все signatures, ре-spawn tasks, requester ре-submit'ит после правок.

**Default**: C — cleanest. `requested_changes` логически = «отзываю approval, давай по новой». Преимущества:
- Audit chain пишет explicit event `request_recycled` с reason.
- Никаких «sticky old signatures» эффектов.
- Tasks ре-спавнятся для всего pool'а — editor, который не успел подписать в первый раунд, получает второй шанс.

Минус: история сигнатур теряется в `_authority_signatures` (мигрирует в `_authority_audit` как event).

**Где править**: [26-authority.md](26-authority.md) §Lifecycle (state machine) + §Data model (изменить UNIQUE или добавить cascade).

---

## P2 — should-clarify

### P2.1 — Cross-tenant approval default

Open question в [26.md §«Открытые вопросы»](26-authority.md). Лучше зафиксировать default сейчас, не оставлять подвешенным до v2.0 work:

**Default**: signer должен быть в том же tenant, что и request. Site-scope (`tenant_id IS NULL`) actors могут подписывать только site-scope requests. Cross-tenant approval (site legal → child publication) — **v2.1 feature**, добавляется явным `AuthorityConfig.AllowSiteScopeSigners bool`.

### P2.2 — Conditional `When` против Translatable() fields

Если row имеет `.Translatable()` поля (i18n core), `When: "title = 'urgent'"` — против какого locale? Канонический (default-locale) source-of-truth или против каждого live перевода?

**Default**: канонический. Translatable() field хранит source-text в основной колонке, переводы в `_translations` table — DoA evaluate'т source-text. Зафиксировать прямо.

### P2.3 — Authority на AuthCollection

`.Authority()` на AuthCollection (например, на user-account для `delete` — «удалить юзера требует chief»). Legitimate use case (GDPR right-to-erasure с approval).

**Default**: разрешено. Тот же gate, никаких special-case'ов. Auth-injected колонки (email/password_hash) тоже доступны для `ProtectedFields`. Это естественно — DoA не знает про специфику auth, видит только row.

### P2.4 — Notification storm — qualified pool of 50 signers

Authority request → 50 push notifications одной командой editor'ов? Реально для большого newsroom.

**Default**: notifications integration в [20-notifications.md](20-notifications.md) применяет user-level **preferences** (digest vs immediate vs none). Inbox-only delivery (без push) — пользователь может отключить. Также: **deduplicated digest** per user — 5 pending authority requests в день = одно дайджест-письмо «5 approvals waiting» с deeplink на queue, а не 5 отдельных push'ей.

### P2.5 — Approved-but-unconsumed forever

Approver подписал, requester забыл нажать publish. Request status='approved' лежит вечно. Через год кто-нибудь жмёт — публикуется вчерашняя по содержанию статья по сегодняшним подписям. Stale.

**Default**: `expires_at` применяется и к `pending`, и к `approved`. Если request approved + expired — переводится в `expired`, эмитим event. Reaper job уже на месте; просто расширяем его условие. `TTL` в `AuthorityConfig` интерпретируется как «время от создания до consume, не до approve».

Альтернатива (`approved_TTL` отдельный от `pending_TTL`) — overkill для v2.0.

### P2.6 — Audit cross-link на data audit log

`.Audit()` пишет в `_audit_log_*`. DoA-consumed write — это **аудитируемая запись**. Можно ли из audit-row найти связанный authority request?

**Решение**: добавить nullable `authority_request_id UUID` колонку в `_audit_log_site` / `_audit_log_tenant`. Handler set'ит при consume. Полностью optional — недо-`.Authority()` коллекции просто NULL.

UI affordance: «Show audit trail for record» отображает inline «✓ approved by editor + chief» вместе с before/after diff. Это превращает audit и DoA в **один связный timeline** без отдельной навигации.

### P2.7 — Force-complete admin action на authority.sign task

`/api/_admin/tasks/{id}/force-complete` на task с `kind="authority.sign"` — что происходит с подлежащим authority request?

**Решение**: force-complete на DoA-spawned task = эквивалент `bypassed` event на request. Admin фактически принудительно «прощает» отсутствие подписи. Request status НЕ автоматически approved (force-complete одной task ≠ approval) — но request audit получает explicit `task_force_completed` event с admin id + reason. Если admin хочет именно approve — есть `/_admin/authority/.../bypass`.

Это keeps task system общим (force-complete работает одинаково для всех kinds), DoA остаётся отдельным концептуальным слоем.

### P2.8 — Task description i18n default

Открытый вопрос из [27.md](27-tasks.md).

**Default**: title/description в `_tasks` — **ICU template** (plaintext с `{var}` placeholders) + `payload`-driven. Render at **GET time** через requestLocale. Преимущества:
- Один title-template переживает language switching.
- Embedder не дублирует строки в `_tasks` per language.
- Совместимо с auto-translate pipeline (translate template once, render N raз).

Template lookup: kind-keyed registry (`internal/tasks/templates.go`) с зарегистрированными по `kind` ICU patterns. Embedder регистрирует свой через `app.Tasks().RegisterTemplate(kind, locale, template)`.

### P2.9 — Mixed assignment v2.0 vs v2.1 ambiguity

[27.md](27-tasks.md) пишет «Mixed assignment (user + role fallback) — v2.1+. Базовый case покрывается двумя независимыми tasks». «Два tasks для одной работы» оставляет ambiguity: complete one — что с другой? Spec не говорит.

**Default v2.0**: если embedder хочет «assigned to user X, but anyone with role Y can also pick up» — explicitly два task'а с одинаковым `target_record_id` и kind, но разными assignment'ами. Completion одной **НЕ** auto-cancel'ит вторую. Embedder отвечает за reconciliation через hook на `task.completed` (можно вызвать `tasks.cancel(other_id, reason="duplicated work covered")`).

Это less magic чем mixed-assignment, и v2.1 переход к built-in fallback — additive.

### P2.10 — `requested_changes` flow integration с tasks

P1.10 решён через recycle pattern (новый round, новые tasks). Но что с уже claimed tasks от первого раунда?

**Решение**: при recycle — все open/claimed spawned tasks ре-set'ятся в `open` (unclaim auto-applied), претендуют на claim повторно. Текущий claimer теряет lock, получает notification «request changed by author; please review again». Это явный UX, не silent loss.

---

## P3 — polish / out-of-v2.0

### P3.1 — Real-time multi-role channel dedup

User имеет 3 ролей → subscribes к 3 channels → один request fan-out'ится на все 3 → client side dedup нужен. Не блокер, очевидное решение (client maintains seen-request-ids set).

### P3.2 — Cancelled task retention

Open question. Сейчас `_tasks.Audit()` пишет diff. Cancelled task сидит в БД вечно.

**Default**: нет авто-purge в v2.0. Embedder добавляет cron-job если нужно (`DELETE FROM _tasks WHERE status='cancelled' AND completed_at < now() - interval '90 days'`). v2.1 — формальный retention setting в `_settings`.

### P3.3 — Multi-signature ordering (sequential chain)

Open question из [26.md](26-authority.md). «Editor сначала, потом chief» — рабочий newsroom flow, но **не блокер для v2.0**. Покрывается через два sequential `.Authority()` calls на разных transitions (`draft → in_review` требует editor, `in_review → published` требует chief). Спецификация явная, embedder моделирует sequence через state.

Native sequential chain (один request, sequence ordering) — v2.1.

### P3.4 — Delegation (Alice→Bob на неделю)

Open question. Сложная фича (temporal role delegation), требует отдельную таблицу `_role_delegations`, RBAC integration. **v2.1+** explicit.

### P3.5 — Approval-on-behalf (PA подписывает за chief)

Open question. v2.0: **запрещён** (UNIQUE constraint не позволяет PA подписать «как chief» без chief'овых credentials). Embedder может моделировать через explicit "delegation" с audit trail, но это P3.4 territory.

### P3.6 — Raw SQL via JS hook `$app.dao().query(...)` обходит audit

[26.md Decision #7](26-authority.md) упомянул raw `app.Pool()`. JS hook'и через `$app.dao().query("UPDATE articles SET status='published'")` — то же самое, обходят `.Audit()` writer. Это **существующий до DoA gap** (документировать в [06-hooks.md](06-hooks.md) как known-opening), не новая дыра. Mitigation: deprecation warning при detection raw UPDATE в hook context, plus admin-UI banner.

---

## Сводка: что должно попасть в spec до начала кода

**Жёсткий минимум (P1)** — 10 правок в [26-authority.md](26-authority.md) + [27-tasks.md](27-tasks.md):

1. Action key derivation rule (P1.1) — добавить в §Schema DSL.
2. Multi-Authority overlap validator (P1.2) — добавить требование в §Schema DSL.
3. Conditional `When` evaluates against post-merge state (P1.3) — §AuthorityConfig поля.
4. `ProtectedFields` + consume validation (P1.4 + P1.8 объединены) — §AuthorityConfig поля + §Lifecycle.
5. `On.Op` discriminator (insert/update/delete) (P1.5 + P1.6) — §AuthorityConfig поля.
6. Hook reject = consume not marked, request stays approved (P1.7) — §Lifecycle.
7. Bulk-operation atomic-fail (P1.9) — новый §Batch interaction.
8. `requested_changes` triggers recycle (P1.10) — §Lifecycle + §Data model.
9. Tasks recycle on request recycle (P2.10) — §Tasks integration.
10. `approved_at_expires` semantics (P2.5) — §Lifecycle.

**Sponge fixes (P2)** — добавить default-решения для 4 open questions:
- P2.1 cross-tenant strict-default
- P2.2 canonical-locale `When`
- P2.3 AuthCollection authority allowed
- P2.8 ICU task templates rendered at GET

После этих правок spec становится implementable без accumulated ambiguity, и v2.0 коду нечего интерпретировать.

**Готов внести правки следующим тиком** — мерж findings обратно в основные доки, потом spec замораживается под label «v2.0 ready for slicing».

## Findings status — после rev1 merge + rev2 переписки

| ID | Status | Заметка |
|---|---|---|
| **P1.1** action_key derivation | 🔄 Translated | В rev2 — matrix `key` уникальный per (tenant, version). Schema-side `.Authority({Matrix})` ссылается по key, нет необходимости в auto-derivation. |
| **P1.2** multi-Authority overlap validator | 🔄 Translated | В rev2 — multi-Authority на одной коллекции легитимный (separate gate points, разные matrices). Overlap check теперь на **matrix selection** уровне (две approved matrices с overlapping amount ranges → admin validation error). |
| **P1.3** Conditional `When` post-merge state | 🔄 Translated | В rev2 — `condition_expr` на matrix optional. Если используется — evaluated против post-merge state. Open question перенесён в [26 §Открытые вопросы](26-authority.md). |
| **P1.4 + P1.8** Consume validation + drift | ✅ Carried | `ProtectedFields` сохранён в rev2 (на schema `.Authority({ProtectedFields})`). Consume validation field-by-field. |
| **P1.5 + P1.6** `On.Op` discriminator | ✅ Carried | `On.Op: "update" / "insert" / "delete"` сохранён в rev2. |
| **P1.7** Hook reject post-DoA | ✅ Carried | Workflow stays in `running` state, не помечается consumed, retry OK. Same model в rev2. |
| **P1.9** Bulk operation contract | 🔄 Translated | Atomic-fail-on-mixed сохраняется. `/preflight` endpoint **удалён** — стандартный queue filter покрывает use case (из ysollo). |
| **P1.10** `requested_changes` recycle | ❌ Obsoleted | Recycle path полностью удалён в rev2 — reject теперь terminal, для revisions initiator создаёт fresh workflow. Из ysollo. Закрывает UNIQUE-constraint + stale-signatures complexity. |
| **P2.10** Tasks recycle | ❌ Obsoleted | Same — нет recycle. |
| **P2.5** Approved-but-unconsumed expiry | 🔄 Translated | В rev2 — `_doa_workflows.expires_at` применяется к running workflow целиком. Approved-but-unconsumed как промежуточное state не существует (workflow's terminal state — completed после consume). |
| **P2.1** Cross-tenant strict default | ✅ Carried | Matrix scoped к tenant (или site-scope). Delegations tenant-scope. Cross-tenant matrix sharing — v2.1. |
| **P2.2** Translatable() canonical locale | ✅ Carried | `condition_expr` evaluation против canonical source-text. |
| **P2.3** Authority на AuthCollection | ✅ Carried | Разрешено в rev2 без special-case'ов. |
| **P2.7** Force-complete on DoA task | ✅ Carried | Force-complete = bypassed audit event. Логика unchanged в rev2. |
| **P2.8** Task descriptions i18n | ✅ Carried | ICU templates rendered at GET через RegisterTemplate. Unchanged. |
| **P2.9** Mixed assignment | ✅ Carried | НЕ в v2.0; embedder spawns двух tasks + reconciles. Unchanged. |

**Новые findings, появившиеся из ysollo comparison** (закрыты в rev2):

| Concept | Status в rev1 | Реализация в rev2 |
|---|---|---|
| Matrix-as-data vs schema-as-code | Schema-as-code (rev1) | Hybrid (rev2): schema declares gate points, matrix data declares rules |
| Approver types | `Roles []string` only | `role / user / position / department_head` |
| Per-level approval modes | Global `Min` | `any / all / threshold` per level |
| Per-level escalation | Global TTL | `escalation_hours` per level + final-escalation handler |
| Materiality | Filter-expression в schema | First-class `min_amount / max_amount / currency` на matrix |
| Multi-level workflow | Flat N-of-M | Level traversal с per-level mode |
| Delegation | Deferred v2.1+ | First-class в v2.0 (`_doa_delegations`) |
| Reassign workflow action | Не существовал | First-class action |
| Matrix versioning | Не существовало | First-class lifecycle: draft → approved → archived/revoked |
| Workflow SLA tracking | Не существовал | `lead_time`, `slaCountdown`, `timeAtStep` в admin UI |
| Approval memo per decision | Notes только | Explicit `memo` field на `_doa_workflow_decisions` |

**Остающееся открытым** (требуют решения до 6.1.x кода — оба в
[26 §Открытые вопросы](26-authority.md)):

1. **`OnRecycle` action** — авто-делать ли что-то с record state
   при `requested_changes`? Сейчас default: nothing, embedder
   reagiruet через `authority.recycled` hook.
2. **Bulk preflight scope** — должен ли покрывать DELETE/INSERT
   batch ы, не только PATCH? Default: yes, единообразная response.

**P3 (polish / out-of-v2.0)** остаются в этом docs как reference
для будущих slice'ов. Не блокируют v2.0 ship.

## Связанные

- [26-authority.md](26-authority.md) — основной spec
- [27-tasks.md](27-tasks.md) — tasks subsystem spec
- [plan.md §6](../plan.md) — v2.0 task breakdown
