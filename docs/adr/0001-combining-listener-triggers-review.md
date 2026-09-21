# Review: ADR 0001 listener match mode (T1–T6)

- **Date:** 2026-09-21
- **Status:** Both bugs fixed and every missing test added on 2026-09-21; see
  [Review follow-up](0001-combining-listener-triggers.md#review-follow-up) in the ADR.
- **Subject:** Uncommitted T1–T6 work against [ADR 0001](0001-combining-listener-triggers.md)
- **Deploy:** T7 was not run at review time. It was deployed later the same day (ADR T7, plan §2.19).

T1–T6 match the ADR. Two bugs mislead the form. Neither lets an impossible listener save, and neither enables Save while a check is pending.

## Slice results

| Slice | Result | What was checked |
|---|---|---|
| Schema and API | Pass | v5 `trigger_mode`, `COALESCE` reads, `ORDER BY id`, 400 `errors` array, `POST /api/webhooks/validate` |
| Matcher and payload | Pass | One `matchingTriggers` decision, OR first-hit payload, AND requires every enabled trigger |
| Validation rules | Issue | R1–R6. Spurious R2 when every enabled trigger already failed R1 |
| Webhook UI | Issue | Selector, sentence, shared row markup. A stale validate response can repaint errors |

## Bugs to fix

### 1. Stale validate paints the previous answer

`whatsapp-webhook-ui/script.js`, `scheduleValidation` (around lines 778–784) and the sequence check (around 819–823).

An edit clears the debounce timer and does not bump `validation.seq`. The sequence only advances when `runValidation` starts or `cancelValidation` runs. A response that is already in flight still matches the current sequence, so `renderValidationErrors` draws that older payload onto the form the user is looking at. The next debounced check corrects it.

Save stays disabled for that window: `validationBlocksSave` is true while `timer !== null` or `inFlight` is set.

**Fix.** Increment `validation.seq` in `scheduleValidation` when a request is already in flight, before resetting the timer. The existing `seq !== this.validation.seq` return then drops the stale body and does not paint it. Leave `inFlight` true on that early return so Save stays blocked until the new check finishes.

### 2. Auto-download plus an R1 failure also reports a false R2

`whatsapp-bridge/internal/webhook/validation_rules.go`. R1 at lines 76–81; the R2 empty-live branch at lines 298–302.

R1 omits a media trigger that matches nothing (for example `media_type = sticker`) from `c.live`. If that was the only enabled trigger and auto-download is on, `checkAudioCanFire` sees an empty live set and adds R2: “this listener has no enabled trigger.” There is an enabled trigger. It already failed R1. The form shows a second, wrong reason. Save still fails.

**Fix.** Use the “no enabled trigger” R2 only when the config truly has no enabled triggers. If `c.live` is empty because every enabled trigger failed R1, return without a second error. Add a test: auto-download on, one enabled trigger `media_type = sticker`, expect R1 only.

## Judgment calls that should stay

The implementation log records these on purpose. They follow the ADR rule that a listener is rejected only when no message could make it fire.

| Call | What the code does | Why it stays |
|---|---|---|
| Keyword and audio | A keyword matches audio only when its pattern matches the empty string (`.*` or `^$`). `keyword = urgent` with `media = audio` is still rejected. | On audio, `ExtractTextContent` returns `""`. The validator uses the same `matchesString` as the matcher. |
| OR payload length | Under OR, `matchingTriggers` returns the first enabled hit, so `matched_triggers` has one entry. | The payload section requires one entry under OR. `trigger` stays the first match. |
| R6 and plain text | Keywords are allowed when the media intersection includes plain text or `poll`. | Decision R6 treats an empty media type as keyword-capable. `poll` is not the only case. |
| Enter within 300ms | Submit while the debounce timer is set is refused with “Still checking the triggers”. | T6.6 as written. The log already notes a friendlier wait-then-save, and left it. |

## What already matches the ADR

**Schema and API.** v5 adds `trigger_mode TEXT DEFAULT 'or'` without editing v1–v4. The same column is in `createTables`. Reads use `COALESCE(trigger_mode, 'or')`. Any other value is a 400. Create, update, and `POST /api/webhooks/validate` share one error shape. The validate route is a fixed path. Enable does not re-validate and still writes the stored mode. `EXPECT_SCHEMA` defaults to 5. `README.md` and `WEBHOOK_README.md` were updated.

**Matcher.** `matchingTriggers` is the one decision for match, payload, and auto-download attribution. Empty mode is OR. Disabled triggers are skipped. `all` does not add an AND requirement.

**UI that held.** The form has one `triggerRowHtml`, sends `trigger_mode`, keeps empty rows so `trigger_index` stays aligned, and leaves disabled triggers disabled. `sticker` stays in the datalist.

## Tests still missing

| Gap | What it would lock |
|---|---|
| Auto-download with only an R1-failing media trigger | Bug 2: R1 alone, no “no enabled trigger” R2 |
| Validate response arriving during the debounce window | Bug 1: stale errors are not painted |
| `POST /api/webhooks/{id}/enable` | `trigger_mode` is preserved and validation is not re-run |
| Keyword regex `.*` or `^$` with `media = audio` | The empty-string path is allowed on purpose |
| Open an existing listener vs a new form | Existing listeners validate on open; a new form does not show “name is required” |
