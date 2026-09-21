# ADR 0001: OR / AND match mode for listener triggers, with validation

- **Status:** Implemented. T1–T6 built and reviewed, review fixes applied, and T7 deployed
  2026-09-21 21:21 UTC (schema v5). See [Implementation log](#implementation-log) and
  §2.19 of `AUDIO_DOWNLOAD_PLAN.md`.
- **Date:** 2026-09-21
- **Deciders:** Adam Russak
- **Scope:** `whatsapp-bridge` (webhook matching, validation, schema), `whatsapp-webhook-ui`,
  `messages.db`

## Context

A listener (a `webhook_configs` row) has one or more triggers — `all`, `chat_jid`,
`sender`, `keyword`, `media_type` — each with a match type of `exact`, `contains` or
`regex`. Today a listener fires when **any one** of its enabled triggers matches. There is
no way to require two conditions at once, so "audio from a specific chat" cannot be expressed
for a webhook delivery; the only workaround is filtering in n8n.

Today's behaviour also sets a trap. Someone who wants AND adds two triggers,
`media_type = audio` and `chat = A`, and gets their **union**: audio from every chat, plus
everything from A. Nothing in the UI says how triggers combine.

Adding AND creates a second hazard: combinations that can **never** match. `chat = A` AND
`chat = B` is always false, because a message belongs to one chat. A listener like that saves
without error and silently does nothing.

Facts about what a message can look like, checked against the code and the live database on
2026-09-21:

- **One chat, one sender, one media type per message.**
- **Media types are a closed set.** `ExtractMediaInfo` produces `audio`, `document`, `image`,
  `poll`, `poll_vote` and `video`, or nothing for plain text — 476,000 stored messages contain
  no other value.
- **Stickers are never recognised.** Yet the UI suggests `sticker`, so a
  `media_type = sticker` trigger can be saved today and never fires, in any mode.
- **Keyword triggers only see plain text and poll questions.** `ExtractTextContent` reads the
  message text or a poll's question and **never an image, video or document caption**. So a
  keyword can never match an audio, image, video, document or poll-vote message.
- **Auto-download only ever captures audio.** A listener with auto-download on whose
  triggers cannot match audio has an action that can never run.
- **LID expansion is per trigger** (`buildTriggerAliases`, B11). A contact's phone form and
  LID form are the same person: `chat = 972547669192` and `chat = 62156783493249@lid` name
  one chat.
- **No live listener combines triggers.** The 2026-09-21 check found 7, and all of them pass
  every rule below. The Capture voice notes listener was test-only and has since been removed,
  which leaves 6, each with a single trigger; a test fixture of those 6 passes in every mode.
- **The payload reports one trigger** (`WebhookPayload.Trigger`). An n8n workflow reads it.

## Decision

### 1. A match mode per listener: OR or AND

| Mode | Meaning | Default |
|---|---|---|
| **OR** | The listener fires when **any** enabled trigger matches. Today's behaviour, unchanged. | ✅ |
| **AND** | The listener fires when **every** enabled trigger matches. | |

AND means exactly what it says. The `all` trigger is always true, so it is neutral under
AND. Disabled triggers are ignored, and a listener with no enabled triggers matches nothing,
as today. Each trigger keeps its own match type and its LID expansion.

### 2. Validation: reject only what can provably never match

The principle: **a listener is rejected only when no possible message could make it fire.**
A combination that *might* match is always allowed. Some unsatisfiable combinations, such as
two mutually exclusive regexes, can slip through — that is the accepted cost of never
rejecting a listener that could work.

| # | Rule | Mode | Rejected example | Why it can never match |
|---|---|---|---|---|
| R1 | A media type must be one the bridge produces | both | `media_type = sticker`, `media_type = audoi` | Only the six types above exist. A `contains` or `regex` media trigger must match at least one of them |
| R2 | Auto-download needs a listener that can match audio | both | auto-download on, triggers `media_type = image` only | Auto-download only captures audio. An audio message has no text, so keyword triggers never match it either |
| R3 | One value per single-valued field | AND | `chat = A` AND `chat = B` | A message has one chat, one sender and one media type. Two `exact` values are allowed only when they are the same contact through the LID map |
| R4 | A pattern must agree with an exact value on the same field | AND | `chat = A@g.us` AND `chat contains "@lid"` | When a field has an `exact` trigger, every `contains` or `regex` trigger on that field must match that value or one of its LID forms |
| R5 | Media-type triggers must overlap | AND | `media_type = audio` AND `media_type regex ^(image\|video)$` | The media types satisfying every media trigger must not be empty. The set is finite, so this is decided exactly |
| R6 | Keywords need a message that has text | AND | `keyword = urgent` AND `media_type = audio` | Keywords only read plain text and poll questions, so the allowed media types must include plain text or `poll` |

**Allowed, because they can match:** `keyword = X` AND `keyword = Y`, since text can contain
both — something no OR-based design offered. Also `sender = Dana` AND `chat = family group`,
since Dana may be in the group, and any `contains` or `regex` pair not settled by R4 or R5.

Every message names its rule and says what to do instead. For R3 on chats:

> A message belongs to one chat, so *chat = A* and *chat = B* can never both match. For
> audio from either chat: set Match to OR with just the two chat triggers if this listener
> auto-downloads, since it only ever captures audio; otherwise create one listener per chat.

The rules need the LID map at save time for R3 and R4. The webhook manager already holds the
store directory for `buildTriggerAliases`, so validation uses the same resolution.

### 3. One validator, used live by the UI

- Create and update run the rules, and a rejected listener is a **400** listing every
  failure, each with its rule and the index of the trigger it concerns.
- **`POST /api/webhooks/validate`** runs the same rules without saving. The form calls it,
  debounced, as triggers change, shows each problem **next to the trigger it concerns**, and
  disables Save while any remain. Keeping one implementation on the server means the UI's
  feedback and the save can never disagree.
- The form's **Match: [ OR | AND ]** selector sits above the triggers, with a
  **plain-language sentence** below them, rebuilt on every change:
  - OR: *"Fires for messages where media type is audio, or chat is A."*
  - AND: *"Fires for messages where chat is A, and media type is audio."*

  The sentence exposes OR's union before a listener is saved — the trap from the context.

### Worked examples

| Triggers | OR | AND |
|---|---|---|
| `chat = A`, `media = audio` | everything in A, plus audio anywhere | audio in A |
| `chat = A`, `chat = B` | messages in A or B | **rejected (R3)** |
| `chat = 972547669192`, `chat = 62156783493249@lid` | that contact's chat | that contact's chat (same person, allowed) |
| `sender = Dana`, `chat = family` | anything from Dana, plus everything in family | Dana's messages in family |
| `keyword = invoice`, `keyword = overdue` | either word | **both words** |
| `keyword = urgent`, `media = audio` | urgent text, plus audio anywhere | **rejected (R6)** |
| `media = sticker` | **rejected (R1)** | **rejected (R1)** |
| `all`, `media = audio` | everything | audio |

### Schema

**v5**, additive per §4.2 of `AUDIO_DOWNLOAD_PLAN.md`:

```sql
ALTER TABLE webhook_configs ADD COLUMN trigger_mode TEXT DEFAULT 'or';
```

The same column goes into `createTables`. Every existing listener reads `or` and keeps its
meaning. In the API, `trigger_mode` is `"or"` or `"and"`; absent means `"or"`, and any other
value is a 400, never silently treated as OR.

### Payload

- `trigger` keeps its shape and reports the **first** matching trigger, so existing n8n
  workflows keep working.
- A new `matched_triggers` array lists every trigger that matched — one entry under OR, all
  of them under AND — together with the listener's `trigger_mode`.

### Unaffected

Auto-download, retention, delivery, logs, the chat picker and the Downloads view act after a
listener matches; only *whether* it matches changes.

## Consequences

**Positive**

- AND means AND: what the form shows is what the listener does.
- Impossible listeners are refused at save time with a reason and a fix, instead of saving
  and silently never firing.
- Keyword AND becomes possible — "invoice and overdue".
- It catches two problems that exist today in either mode: `sticker`, and auto-download on a
  listener that can never match audio.
- Backward compatible: default OR, additive schema, and the remaining live listeners pass the rules.

**Negative**

- **"Audio from chat A or chat B" is not one AND listener.** R3 rejects it. For
  auto-download, OR with chat triggers already does it, since auto-download only captures
  audio. For a webhook delivery it needs one listener per chat.
- **Not every impossible combination is caught.** Two `regex` triggers can be mutually
  exclusive in ways that are not decidable in general; they are allowed.
- **Validation depends on the LID map.** If `whatsapp.db` cannot be read at save time, R3 and
  R4 fall back to comparing values as typed, which could reject a phone/LID pair that is
  really one person. The error message says so.
- One more schema version (v5), with its deploy checklist.
- **A rollback to v4 turns AND listeners into OR listeners, silently.** The v4 binary reads
  `webhook_configs` fine, because its queries name their columns. But its matcher never
  looks at `trigger_mode`, so an AND listener fires on the union: more deliveries, and more
  audio captured if auto-download is on. The column survives, since the v4 `UPDATE` does not
  touch it, so moving forward to v5 again restores AND. The v4 validator also does not run
  R1–R6. See T7.

## Review

Checked against the bridge and `whatsapp-webhook-ui` on 2026-09-21, **before** the
implementation. The decision stands. These facts describe the code as it was then, and they
changed how it was built; the [Implementation log](#implementation-log) records the result:

- Schema is **v4**. v5 is the next entry in the `migrations` slice in
  `internal/database/migrations.go`. `createTables` in `store.go` must gain the same column.
  `scripts/smoke.sh` defaults `EXPECT_SCHEMA` to 4.
- Whether a listener matches and which trigger the payload names are **two loops**:
  `MatchesTriggers` and the first-match loop in `ProcessMessage` (`internal/webhook/manager.go`).
  AND has to be one function. Two copies will let the payload name a trigger the match
  decision did not require.
- `GetWebhookTriggers` has no `ORDER BY`, so "the first matching trigger" is not stable.
  Triggers need a stable order before `trigger` and `matched_triggers` mean anything.
- `ValidateWebhookConfig` returns one `error`. Create and update answer
  `{success:false, error:"..."}`. The new 400 adds an `errors` array and keeps `error` as
  the first message, so the existing toast still renders.
- LID expansion runs at **load** time (`buildTriggerAliases`). R3 and R4 run at **save**
  time and call `ResolveChatJIDs` with the manager's store directory. If `whatsapp.db`
  cannot be read, compare the typed strings and say so in the message.
- A keyword can match only plain text (`media_type` empty) and `poll`. `poll_vote` has no
  text. View-once audio is not `media_type = audio`; it extracts as empty.
- `POST /api/webhooks/{id}/enable` does not re-validate. Enable does not change triggers,
  so it stays outside these rules.
- The form has no match mode and no validate call, and `index.html` still offers `sticker`.
  Trigger-row markup is copied in three places (`index.html`, `resetForm`, `addTriggerRow`).
  `collectTriggers` drops a non-`all` row whose value is empty, which would shift
  `trigger_index` away from the row the user sees. UI tests are Node's test runner with
  jsdom, via `scripts/test-ui.sh`.
- The open questions are decided below. `sticker` stays in the suggestions, keywords do not
  grow to read captions, and the test-only Capture voice notes listener is already gone.

## Implementation

Tasks run in this order. A task is done when its checks pass. Do not start T3's rule tests
against a validator that T2's matcher would disagree with: R2 and R6 ask "could a message
fire this listener", which is the matcher's question.

### T1. Schema v5 — `trigger_mode` ✅

1. Append migration v5 in `migrations.go`: `addColumns` of
   `trigger_mode TEXT DEFAULT 'or'` on `webhook_configs`. Do not edit v1–v4.
2. Add the same column to `createTables` in `store.go`.
3. Add `TriggerMode` (`json:"trigger_mode"`) on `WebhookConfig`. Absent or empty reads and
   writes as `or`.
4. Thread the column through the webhook `SELECT`, `INSERT` and `UPDATE` in `webhooks.go`.
   Read with `COALESCE(trigger_mode, 'or')`.
5. `GetWebhookTriggers`: `ORDER BY id` so trigger order is stable.

**Checks.** A migration test from a pre-v5 `webhook_configs` (the shape in
`migrations_test.go`) leaves existing rows untouched and reads `or`. A fresh database is
already at v5. `SchemaVersion()` is 5. Bump the assertions that lock the version
(`migrations_test.go`, `health_test.go`, `scripts/smoke.sh` default).

### T2. Matcher — one function, two modes ✅

1. Replace the two match loops with one helper that returns the enabled triggers that
   matched, in trigger order. `MatchesTriggers` is true when that list is non-empty under
   OR, and when it contains every enabled trigger under AND.
2. Empty `trigger_mode` is OR. A listener with no enabled triggers matches nothing. `all`
   is always true, so under AND it does not add a requirement.
3. Disabled triggers are skipped in both modes. LID expansion stays as it is: exact
   `chat_jid` and `sender` only, at load time.
4. `ProcessMessage` uses that same list. `trigger` is the first entry. `matched_triggers`
   is the whole list. `trigger_mode` is copied onto the payload. See T5 for the fields.

**Checks.** One test per worked-example row, plus disabled triggers, an empty listener, and
`all` beside another trigger. The OR column of that table must match today's behaviour,
including "audio from anywhere, plus everything in A".

### T3. Validator — R1 to R6 ✅

Extend `ValidateWebhookConfig`. Keep the checks it already makes (name, URL, retention,
trigger type, match type, regex compiles). After those, apply R1–R6 to **enabled** triggers
only. Collect every failure; do not return on the first.

`trigger_mode` other than `or` or `and` is a failure of its own (not one of R1–R6), and is
never treated as OR.

Each rule failure is `{rule, trigger_index, message}`. `trigger_index` is the index in the
submitted `triggers` array, so a disabled row in the middle does not renumber the rows the
UI draws. `message` names the rule and says what to do instead, using the chat wording in
the decision for R3.

The satisfiability checks use this closed set, not a copy that can drift:

- Produced media types: `audio`, `document`, `image`, `poll`, `poll_vote`, `video`.
- Keyword-capable: empty media type (plain text) and `poll`. Nothing else, including
  `poll_vote` and view-once audio.

Per rule:

1. **R1 (both modes).** Every `media_type` trigger must accept at least one produced type.
   `exact` must be one of the six. `contains` and `regex` are tested against all six.
2. **R2 (both modes), only when auto-download is on.** Ask whether some `audio` message
   could fire the listener. Under OR, one enabled trigger that an audio message can
   satisfy is enough (`all`, any `chat_jid`, any `sender`, or a `media_type` that accepts
   `audio`). A `keyword` never can. Under AND, every enabled trigger must be satisfiable
   by one audio message, so a keyword fails R2, and so does a media trigger that rejects
   `audio`.
3. **R3 (AND).** On `chat_jid`, `sender` and `media_type`, two `exact` values may both
   match only when they are the same value, or — for chat and sender — the same contact
   through `ResolveChatJIDs`. Media types have no LID alias. If the LID map cannot be
   read, compare the typed strings and say that the phone/LID check was skipped.
4. **R4 (AND).** When a field has an `exact` trigger, every `contains` or `regex` on that
   field must match that value or one of its LID forms. Same fallback as R3.
5. **R5 (AND).** Intersect the produced types accepted by every `media_type` trigger. An
   empty intersection fails. The set is finite, so this is exact.
6. **R6 (AND).** If any `keyword` is enabled, the listener must still allow a
   keyword-capable message. No media trigger means plain text is still possible. Media
   triggers mean the R5 intersection must include `poll` (the only produced type that
   carries text).

A combination the rules cannot decide, including two regexes, is allowed.

**Checks.** One test per rule with the rejected example from the decision **and** a near
miss that must be accepted: two keywords under AND, `sender` AND a different field,
phone and LID of one contact under AND, `contains`/`regex` that does match the exact
value, a media regex that still leaves one type, `keyword` AND `media_type = poll`.
A fixture of the live listeners passes. The Capture voice notes listener was test-only and
has been removed, so it is not part of that fixture. `ValidateWebhookConfig` URL tests keep
passing.

### T4. API — save and `POST /api/webhooks/validate` ✅

1. On validation failure, create and update return **400**:

   ```json
   {"success": false, "error": "<first message>", "errors": [
     {"rule": "R3", "trigger_index": 1, "message": "<same message>"}
   ]}
   ```

   `error` stays a string so current clients keep working. Basic failures (name, URL,
   bad `trigger_mode`) use the same array; `rule` is empty and `trigger_index` is `-1`
   when the failure is not about one trigger.
2. Register `POST /api/webhooks/validate`. It decodes a config, runs
   `ValidateWebhookConfig`, and does not write. Success is
   `{"success": true, "errors": []}`. Failure is the same 400 as a save.
3. Route it as a fixed path, not under `/api/webhooks/{id}`.

**Checks.** The same body through validate and through create returns the same `errors`.
A valid body validates without inserting a row. An unknown `trigger_mode` is 400.

### T5. Payload ✅

On `WebhookPayload`:

- `trigger` — unchanged shape, first entry of the match list.
- `matched_triggers` — every entry of that list, each a `WebhookTriggerInfo`.
- `trigger_mode` — `"or"` or `"and"`.

One entry under OR, every enabled trigger under AND. Delivery and `webhook_logs` keep
logging the single `trigger`. An n8n workflow reads `trigger`, so its shape does not change.

**Checks.** A two-trigger AND match sends both in `matched_triggers` and the first in
`trigger`. An OR match that hits the second trigger still reports that trigger as
`trigger` and as the only `matched_triggers` entry.

### T6. UI — selector, sentence, live validation ✅

1. In the listener modal, add **Match** (`#triggerMode`, `or` default) above
   `#triggersContainer`, and `#triggerSummary` below the triggers.
2. Add `id="submitBtn"` to the submit button. Put a `.trigger-error` slot on the trigger
   row. The row markup is duplicated in `index.html`, `resetForm` and `addTriggerRow` —
   one helper should emit it, so the slot cannot drift.
3. Submit `trigger_mode` on create and update. `populateForm` restores it.
4. Rebuild the sentence on every change, from the visible rows:
   - OR: *"Fires for messages where media type is audio, or chat is A."*
   - AND: *"Fires for messages where chat is A, and media type is audio."*
5. Debounce (~300ms, same idea as the chat picker) a `POST /api/webhooks/validate` of the
   payload the save would send. Validate the **visible** rows. Do not drop empty rows
   before that call, or `trigger_index` will point at the wrong row.
6. Render each error on the trigger at `trigger_index`. A `trigger_index` of `-1` is a
   form-level message. Disable Save while any error remains or a validate request is in
   flight. `handleFormSubmit` refuses to save in that state too: tests submit the form
   directly, which does not care that the button is disabled.
7. On a 400 from create or update, render `errors` the same way.

Leave `sticker` in the datalist. R1 rejects it if someone picks it.

**Checks** (`whatsapp-webhook-ui/test`, run with `scripts/test-ui.sh`):

- The selector defaults to OR, and the payload includes `trigger_mode`.
- The sentence matches the two templates above.
- A rejected body puts the message on the named row and disables Save.
- A clean body enables Save.
- An error with `trigger_index: -1` is not painted on a trigger row.

### T7. Deploy ✅

This migration touches `store.go`, so it takes the §4.6 checklist in `AUDIO_DOWNLOAD_PLAN.md`.
The smoke check is `EXPECT_SCHEMA=5`. The log line that proves the deploy is
`messages.db schema version 5`. Step 6b (`./scripts/test-ui.sh`) runs because T6 changes
the form. Step 9 is the rollback drill on a copy: a v4 binary must still read
`webhook_configs` after the column exists, which is what the constant default is for.

Specifics for this deploy, on top of the checklist:

- **Commit first.** Deploy a commit, not a working tree, so the image matches a revision.
- **Tag the running image** as `whatsapp-bridge:pre-trigger-mode` before building, as earlier
  deploys did (`pre-retention`, `pre-b12`, …). That is the rollback target and the image for
  step 9.
- **Step 4** rehearses v5 with `cmd/migrate` on a fresh `store-test` copy. Pass: only
  `schema_migrations` gains a row, the 6 listener rows are byte-identical apart from the new
  column, and every listener reads `trigger_mode = 'or'`.
- **Bridge and UI go out together** (one `up -d --build --force-recreate`). A v5 form in
  front of a v4 bridge would lose AND without a word: the v4 bridge ignores `trigger_mode`
  and saves the listener as OR. The form survives the missing validate endpoint (see log
  point 10), but not that.
- **Step 8b** `smoke.sh` now also checks that every listener carries a valid `trigger_mode`
  and that the served UI has `triggerMode` and `triggerSummary`.
- **After the smoke run**, check it live once: open an existing listener (no errors
  expected), and try an AND listener with two chats in the form. Expect R3 on the second row
  and Save disabled, without saving anything.
- **Rollback:** switch back to `pre-trigger-mode`. No data changes are needed. If any AND
  listener exists by then, it acts as OR until v5 returns (see Consequences), so list them
  first: `SELECT id, name FROM webhook_configs WHERE trigger_mode = 'and'`.

**Result (2026-09-21).** Deployed at 21:21 UTC; the full record is §2.19 of
`AUDIO_DOWNLOAD_PLAN.md`. What differed from the plan above:

- **Deployed before committing,** by decision, so the result could be checked first. The
  commit followed, and it includes the cache-buster fix below.
- **Everything passed:**
  - The rehearsal left every old column byte-identical, and all 6 listeners read `or`.
  - The backups `*_20260921_212113.db` pass `integrity_check`.
  - The log prints `messages.db schema version 5`.
  - The live rows are unchanged and `smoke.sh` is 27/27.
  - On real data: R3 fires for two chats, a phone number and its LID pass, and all 6 stored listeners pass.
- **Step 9 was run by query, not by booting the old image.** The bridge serves its API only
  after connecting to WhatsApp, so booting v4 on a copy would open a second session from a
  copy of `whatsapp.db` and put the live one at risk. The v4 SQL run against the v5 copy
  confirms the Consequences entry: v4 reads the table, inserts `or`, and leaves `and` as it is.
- **Missed and fixed:** `index.html` still requested `script.js?v=9`, and nginx caches
  scripts for an hour. A cached browser would run the old script against the new page, whose
  trigger list starts empty. Bumped to `script.js?v=10` and `styles.css?v=10` (the stylesheet
  had no version before), then redeployed the UI alone; smoke passed 27/27 again. §4.6 step 6b
  of the plan now requires the bump.

## Implementation log

Implemented 2026-09-21, T1–T6 in the order above, then corrected after
[the review](https://github.com/AdamRussak/whatsapp-mcp/blob/0784307/docs/adr/0001-combining-listener-triggers-review.md) (removed after its fixes landed; kept in history) (see [Review follow-up](#review-follow-up)).
`go test ./...` and `scripts/test-ui.sh` (42 tests) pass. The ADR was re-checked against the
code afterwards; that pass added the rollback consequence and the T7 specifics above. Deployed
the same day (T7).

### What was built

| Task | Where | Notes |
|---|---|---|
| T1 | `migrations.go` (v5), `store.go`, `webhooks.go`, `types.go` | `WebhookConfig.TriggerMode` plus `Mode()`, which reads empty as `or`; stored as `or` when unset. `GetWebhookTriggers` orders by `id`. `smoke.sh` defaults to `EXPECT_SCHEMA=5`, checks every listener has a valid `trigger_mode`, and looks for `triggerMode`/`triggerSummary` in the served UI. `health_test.go` compares against `SchemaVersion()` rather than a literal, so it needed no bump; the new migration tests assert 5 directly. |
| T2 | `webhook/manager.go` | `matchingTriggers` is the one decision: under OR it returns the first enabled match, under AND every enabled trigger or nothing. `MatchesTriggers`, `ProcessMessage` (via `matchConfigs`) and auto-download attribution (`firstMatchingTrigger`) all use it; the second loop in `ProcessMessage` is gone. |
| T3 | `webhook/validation.go`, new `webhook/validation_rules.go` | `ValidateWebhookConfig` still returns `error`, now a `ValidationErrors` slice whose `Error()` is the first message. The closed set lives next to `ExtractMediaInfo` as `whatsapp.ProducedMediaTypes` and `whatsapp.CarriesText`, with a test that fails if `ExtractMediaInfo` returns a type the set does not list. `whatsapp.LIDMapReadable` tells R3/R4 whether `ResolveChatJIDs` really consulted the map, since that function falls back silently. |
| T4 | `api/handlers.go`, `api/server.go` | `sendValidationErrors` writes the 400. `/api/webhooks/validate` is registered as a fixed path, which the mux prefers over the `/api/webhooks/` prefix. |
| T5 | `types.go`, `manager.go` | `matched_triggers` and `trigger_mode` on the payload. Delivery and `webhook_logs` still log the single first trigger. |
| T6 | `index.html`, `script.js`, `styles.css` | `triggerRowHtml()` is the only copy of the row markup; `index.html` ships an empty `#triggersContainer` and the script adds the first row. `buildWebhookPayload()` is shared by save and validate. Form-level problems go in a new `#formErrors` block above the buttons. |

Tests added: `webhook/match_mode_test.go` (every worked-example row in both modes, disabled
triggers, empty listener, `all`, and T5 payloads checked against a real HTTP receiver),
`webhook/validation_rules_test.go` (39 rule cases, message wording, LID fallback, index
counting, the live-listener fixture), `api/webhooks_validate_test.go`, T1 tests in
`migrations_test.go` and `webhooks_test.go`, and `whatsapp-webhook-ui/test/triggers.test.js`.

### Where the build differs from the text above

1. **An enabled trigger other than `all` needs a value.** This is a new basic check, not one
   of R1–R6. T6.5 has the form send empty rows so `trigger_index` stays aligned, so the
   bridge has to judge them; `contains ""` would otherwise match every message. The message is
   shown on the empty row. A disabled empty trigger is left alone.
2. **R1, R2, R5 and R6 ask the matcher, which makes them a little more permissive than the
   wording in T3.** Whether a trigger accepts a media type is decided by the same
   `matchesString` the matcher uses, over the six produced types **plus plain text**. So a
   media regex that matches only the empty string passes R1, because it does match plain
   text. A keyword on a message without text sees `""`, so a keyword regex like `.*` or `^$`
   *can* match audio and passes R2 and R6, where T3 says "a keyword never can". This follows
   the principle in §2: reject only what can never match.
3. **Rules are not reported twice.** A trigger that fails R1 is left out of R2–R6, since
   every later rule would only restate that it matches nothing. R5 stays silent when R3
   already found two different exact media types. R6 is skipped when R5 failed. Under AND, R2
   does not flag a trigger another rule already flagged. Every trigger with a problem still
   gets at least one error.
4. **Where each error points.** R3 flags the *later* of two conflicting triggers, R4 the
   pattern, R5 the media trigger that empties the intersection, and R6 each keyword. R2 under
   OR, and R2 with no enabled triggers, is form-level (`-1`), because no single trigger is at
   fault.
5. **R2 with no enabled triggers fails.** Under AND the "every trigger matches audio" test
   passes vacuously, so it is checked explicitly. It counts enabled triggers, not the ones
   left after R1, so a lone `sticker` trigger gets R1 only (review bug 2).
6. **Syntax errors stop the rules.** If any trigger fails a basic check (unknown type or
   match type, bad regex, missing value), R1–R6 do not run until it is fixed. An invalid
   `trigger_mode` still lets R1 run, but not the rules that depend on the mode.
7. **The form keeps a disabled trigger disabled.** Before this change, `collectTriggers`
   sent `enabled: true` for every row, so editing a listener quietly re-enabled its disabled
   triggers. Rows now carry the stored flag, are dimmed, and are left out of the sentence.
8. **AND listener cards start with "All of:".** OR cards are unchanged.
9. **The form checks an existing listener as soon as it opens**, so one the rules now reject
   says why before it is edited. A new, empty form waits for the first edit, so it does not
   open showing "name is required".
10. **A failing validate call does not lock the form.** Anything other than 200 or 400 (an
    older bridge without the endpoint, a 5xx, a network error) counts as no problems; the
    save is still validated by the bridge.
11. **An edit makes an in-flight check stale.** `scheduleValidation` advances `seq` when a
    check is on the wire, so its answer is dropped rather than painted over the newer form,
    and `inFlight` stays set so Save waits for the new check (review bug 1).
12. **Removing a row clears the drawn errors at once**, because the rows below it have moved
    up and their `trigger_index` no longer lines up, until the next answer arrives.
13. **Sentence details beyond the two templates.** Under OR, `all` gives "Fires for every
    message."; under AND it adds nothing and is left out. Keywords read as *the text is
    "x"* / *the text contains "x"*, and regexes as *matches …*. Rows without a value are
    skipped.

### Comments for review

- **Enter within 300ms of an edit is refused** with "Still checking the triggers". That is
  T6.6 as written, but it can surprise someone who types a name and presses Enter at once.
  Waiting for the in-flight check and then saving would be friendlier and still never save
  unchecked. The review kept it as specified; it is a candidate follow-up.
- **Point 2 above is a judgment call, and the review kept it.** If the stricter reading
  ("a keyword never matches audio") is ever preferred, change the `keyword` case of
  `canFireOn` in `validation_rules.go`.
- **The live-listener fixture uses placeholder numbers.** Each live listener has one trigger,
  so the values do not affect any rule, and real phone numbers and group IDs stay out of the
  tests.
- **`/enable` keeps `trigger_mode`.** It reads the listener with `GetWebhookConfig` and writes
  it back with `UpdateWebhookConfig`, both of which now carry the column. As decided, it does
  not re-validate (locked by `TestEnableKeepsModeAndDoesNotRevalidate`).
- **Docs:** `WEBHOOK_README.md` covers `trigger_mode`, the 400 shape with a table of rules,
  `POST /api/webhooks/validate`, and the new payload fields. `README.md` lists the endpoint.

### Review follow-up

[The review](https://github.com/AdamRussak/whatsapp-mcp/blob/0784307/docs/adr/0001-combining-listener-triggers-review.md) passed the schema, API, matcher
and payload slices, and found two bugs. Neither let an impossible listener save. Both are
fixed:

| Bug | Fix | Locked by |
|---|---|---|
| 1. A validate answer overtaken by an edit was painted over the newer form until the next check landed | `scheduleValidation` advances `validation.seq` while a check is in flight; the stale answer returns early and leaves `inFlight` set | `a validate answer overtaken by an edit is not painted` (`triggers.test.js`). Checked to fail with the fix removed |
| 2. Auto-download with only an R1-failing media trigger also reported R2 "no enabled trigger" | `checkAudioCanFire` takes the enabled count; with enabled triggers but an empty `live` set it returns, since R1 already says why | `TestR1FailureAloneDoesNotAlsoReportR2`, in both modes, plus the true no-enabled-trigger case |

The review's missing tests are added:

| Gap | Test |
|---|---|
| R1-only media trigger with auto-download | `TestR1FailureAloneDoesNotAlsoReportR2` |
| Answer arriving after a newer edit | `a validate answer overtaken by an edit is not painted`. The jsdom harness now awaits a route that returns a promise, so a test can hold a response back |
| `POST /api/webhooks/{id}/enable` | `TestEnableKeepsModeAndDoesNotRevalidate`: a listener stored under AND with two chats (an R3 failure) enables with a 200 and keeps `and` |
| Keyword `.*` / `^$` with `media = audio` | `TestKeywordMatchingEmptyTextIsAllowedWithAudio`, which also checks that `urgent` with audio is still R6 |
| Existing listener vs new form on open | `an existing listener is checked on open; a new form is not` |

The review's four judgment calls stay as they are: the keyword empty-string path, one
`matched_triggers` entry under OR, plain text counting for R6, and refusing Enter within
300ms.

### Next

Nothing is required. Optional follow-ups, none of them blocking:

- **Make Enter wait for the running check, then save,** instead of refusing (see Comments
  for review).
- **Let keywords read captions** (deferred in open question 3). R6 would need revisiting.
- **A real rollback drill** needs a way to serve the API without connecting to WhatsApp
  (plan G-e).

## Alternatives considered

- **AND across trigger types** (same-type triggers OR'd, different types AND'd). It keeps
  "(A or B) and audio" in one listener, but "AND" would not mean what it says: two chat rows
  under AND would silently mean either chat, and keyword AND would stay impossible. Literal
  AND with validation keeps the selector honest.
- **Global AND without validation.** It lets impossible listeners save and silently do
  nothing — the hazard this ADR exists to prevent.
- **An AND / OR choice between each pair of triggers.** "A or B and C" is ambiguous without
  precedence rules or parentheses, and reordering rows would change the meaning.
- **Explicit trigger groups, or a boolean expression language.** More expressive, but they
  need a grouping model or a parser and an editor. Anything they add is already available by
  using more than one listener.

## Decisions on the open questions

1. **An n8n workflow reads `trigger`.** It stays the first match. `matched_triggers` is
   added beside it.
2. **`sticker` stays in the suggestions.** R1 rejects it on save.
3. **Captions are out of scope.** Keywords keep seeing only plain text and poll questions,
   and R6 is written for that. Reading captions is a later change.
4. **The Capture voice notes listener is gone.** It was for testing only, so there is
   nothing to convert to chat triggers.
