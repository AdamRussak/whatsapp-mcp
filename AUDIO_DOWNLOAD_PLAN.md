# WhatsApp Voice / Audio Download Plan

Single plan for downloading WhatsApp voice notes and audio: on demand, in bulk from chat history, and automatically for chosen contacts and groups. It replaces the earlier `VOICE_DOWNLOAD_MILESTONE_1.md`, which is now **Milestone 1** below.

**Status as of 2026-09-21:** Milestone 1, **Phases 0 and 1 are complete and deployed**, and the **backlog sweep (§2.13) is complete** (164/164 recent uncaptured audio files recovered with 0 failures). Milestone 1: full history batch download executed on `972523996302` / `18674685612157@lid` (4,062 items): 106 recoverable voice notes downloaded as `.ogg` (all valid Opus), everything else recorded with an explicit status in `media.db` and `index.json`, 0 writes to `messages.db`; §5.6 is 100% satisfied. Phase 0: Python deps pinned below MCP 2.x, whatsmeow on `20260917111002` and Go 1.26 live since 2026-09-20 20:26 UTC (§2.10). **Phase 1 (schema v1)** deployed 2026-09-21 07:36 UTC (§2.11): new audio messages are tagged with `is_ptt`, `seconds`, `mimetype` and `direct_path`, downloads use the stored `direct_path`, and B10 is fixed. **Phase 4 (auto-download) deployed 2026-09-21 11:35 UTC** (§2.14, schema v2): a listener is an ordinary webhook entry with `auto_download` ticked, defined in the same UI and the same `POST /api/webhooks` as every event today. The leak is now closed at both ends — the backlog was swept (§2.13) and new voice notes are captured as they arrive. One decision still open: re-pairing after S1 (§2.4 step 1) — deferred.

Legend: ✅ done · 🟡 partly done / done with open issues · ❌ not started · 🐞 known bug

---

## 1. Goals

| # | Goal | Delivered by | Status |
|---|---|---|---|
| G1 | Download a single voice message (or any audio) on demand | Milestone 1 (`POST /api/download`) + Phase 3 MCP tool | 🟡 works for media up to ~11–19 days old; older is unrecoverable (B1) |
| G3 | Download past voice messages from one chat or group | Milestone 1 (batch job) → Phase 5 (by sender, history request) | ✅ batch job run over the target contact's full history (4,062 items, §2.9); only the last ~11–19 days are recoverable (B1) |
| G2 | Auto-download voice messages from chosen users / groups, using the webhook trigger rules | Phase 4 | ✅ live since 2026-09-21 (auto-download is an action on a webhook entry, §2.14) |

Order: **Milestone 1 ✅ → Phase 0 ✅ → Phase 1 ✅ → backlog sweep ✅ → Phase 4 ✅ (brought forward, §2.12) → Phase 7 ← next → Phases 2/3 → Phase 5 → Phase 6.**

All three goals are met for media inside the CDN window. What remains is ergonomics (Phases 2/3: MCP tools, MP3 conversion, a Gradio tab), the low-payoff history request (Phase 5), transcription (Phase 6), and the operational gaps in §2.16.

---

## 2. Current state and next steps

### 2.1 Milestone 1 completion snapshot (2026-09-20)

> Superseded as a "current state" by §2.9–§2.12; kept because it is the evidence for §5.6. Live totals have moved since (see §2.12).

| Check | Result |
|---|---|
| Milestone 1 live run (§5.5) | ✅ Executed on full history (4,062 items) of `972523996302` / `18674685612157@lid` |
| Files downloaded | `store/media/972523996302/`: 106 `.ogg` (106/106 valid `OggS` headers, dating back to 2026-09-04) + `index.json` (4,062 entries: 106 `ok`, 339 `rekeyed_on_phone`, 1 `expired_not_on_phone`, 3616 `failed`) |
| `media.db` totals | `ok` 107 (106 contact + 1 group) · `rekeyed_on_phone` 339 · `expired_not_on_phone` 1 · `failed` 3619 · `retry_timeout` 1 |
| Target chat `messages` rows | 20,096 rows; **byte-identical** before/after (`diff /tmp/m1_before.txt /tmp/m1_after.txt` completely empty) |
| DB Integrity | `PRAGMA integrity_check` on backups: `ok`; `PRAGMA quick_check` on live `messages.db`: `ok`; table list unchanged |
| Idempotency | Re-running the batch job on 4,062 items: **`skipped: 4062`, `done: 0`, `failed: 0`** in 15 s |
| Backups taken | `store/backups/messages_20260919_174021.db`, `whatsapp_20260919_174021.db`, `media_20260919_174021.db` (all integrity `ok`) |
| Image tag | `whatsapp-bridge:pre-m1` |

**Morning snapshot (before fixes S1/S2, kept for history):**

| Check | Result |
|---|---|
| `go vet ./...`, `go test ./...` | clean / pass |
| `GET /api/media/whatsapp.db`, `messages.db`, `media.db`, `../whatsapp.db`, `%2e%2e/whatsapp.db`, `…/index.json` | all **404** (after fix S1) |
| `GET /api/media/<chat>/<file>.ogg` | 206, `audio/ogg; codecs=opus` |
| `POST /api/download` on the newest voice note (`AC5923C5…`, group `972523531857-1556783116@g.us`) | 200, direct CDN download, no retry, 3,968 bytes, `OggS` header, first `ok` row in `media.db` |
| Media retry for expired items (3 attempts in logs) | phone answers `SUCCESS` with a fresh direct path in ~1 s, then **every host fails with `hash of media ciphertext doesn't match`** (B1) |
| `media.db` totals | `ok` 1 · `failed` 4 · `retry_timeout` 1 |
| `store/media/972527872233/` | 11 `.ogg` + `index.json`, written by `scripts/download_chat_audio.py` (standalone decrypter, not the bridge) |
| Backups | `store/backups/messages_pre_m1.db`, `whatsapp_pre_m1.db` (07:23). Integrity check result not recorded. |

### 2.2 Fixed in this session

| ID | Issue | Fix |
|---|---|---|
| S1 | **Security:** `GET /api/media/<path>` resolved paths from `store/` and served `whatsapp.db` (session keys) and `messages.db` to anyone reaching port 8080 (published on `0.0.0.0`, CORS `*`). | `resolveMediaPath` (`internal/api/handlers.go`) serves only `.ogg`/`.mp3` files under `store/media/`, rejects traversal, absolute paths and symlinks leaving the root. `download_url` is now `/api/media/<chat>/<file>`. Test: `internal/api/media_test.go`. Deployed. |
| S2 | `ExtractDirectPathFromURL` dropped the query string (`ccb`, `oh`, `oe`, `_nc_sid`), so every CDN request lacked its auth token and was malformed (`…n.enc&hash=…`). Everything fell through to media retry. | Keeps the query, drops only `mms3=true` (`internal/whatsapp/media.go`). Test uses a real stored-URL format. Deployed. |

> [!WARNING]
> Because of S1, the WhatsApp session database was readable by anyone who could reach port 8080 while the earlier build ran. If that port has ever been reachable from outside this machine, unlink the device on the phone and pair again.

### 2.3 Open bugs (Milestone 1)

| ID | Sev | Issue | Where |
|---|---|---|---|
| B1 | **recorded, no fix known** | After a successful media retry, the phone uploads a copy **encrypted with a different media key**. The original key fails its MAC, so it can't be decrypted. See §2.5. ✅ Now recorded as `rekeyed_on_phone` and skipped on re-runs. | `downloadWithMediaRetry`, `download.go` |
| B2 ✅ | high | *(fixed)* `expired_not_on_phone` is never recorded: a decrypt error on the notification `continue`s until the 30 s timeout, and a `NOT_FOUND` result is saved as generic `failed`. Re-runs retry these forever at 30 s each. | `download.go` |
| B3 ✅ | high | *(fixed: `MarkInterruptedJobs` at startup)* A job left `running` by a bridge restart blocks all new batches (409) forever. | `media_store.go`, `main.go` |
| B4 ✅ | med | *(fixed: `CancelJob` only touches running jobs, else 404)* `DELETE /api/download/batch/{id}` marks any job `cancelled`, including finished or non-existent ones. | `handlers.go` |
| B5 ✅ | med | *(fixed: per-item timeouts are the retry timeout + 30 s (batch) / + 60 s (single))* `MEDIA_RETRY_TIMEOUT_S` is read but never used; retry timeout is hardcoded 30 s. Per-item timeout is 60 s in batch, 90 s single. | `config.go`, `download.go` |
| B6 ✅ | med | *(fixed: `ChatFolderKey` + per-chat cache; batch response returns `folder`)* Folder key is computed three different ways (`resolveTargetFolder`, `runBatchDownload`, `tr -cd` in the shell script). For an unmapped `@lid` or a group, `index.json` / the script's "Index file" line point at the wrong folder. `ResolveChatJIDs` also opens `whatsapp.db` once per message (~4,000× per batch). | `download.go`, `handlers.go`, `tests/download-chat-audio.sh` |
| B7 ✅ | med | *(fixed: removed; every message goes through `HandleMessage` again)* Placeholder-resend hack (out of scope): sends a `BuildUnavailableMessageRequest` peer message to the phone for **every** retried item, and `HandlePlaceholderMessage` swallows any matching `events.Message` so it is never stored or sent to webhooks (even when the channel is full). It has never fired (0 in logs). | `download.go`, `client.go`, `main.go` |
| B8 ✅ | low | *(fixed: `NormalizeTimeFilter`; invalid → 400)* `after`/`before` are compared as text: `2026-01-01T…` vs stored `2026-01-01 …+00:00`, so boundary days are wrong. | `messages.go` `ListAudioMessages` |
| B9 | low | `MediaDownloader` (`media.go`) is still unused — nothing constructs it. Plaintext SHA-256 check duplicates whatsmeow's. `gofmt -l` now flags only `whatsapp/client_test.go` and `whatsapp/event_handlers_test.go` (`types.go` and `whatsapp/handlers.go` were formatted in Phase 1). Python `download_media` reads the error from `message`; confirm which key `SendJSONError` writes. | various |
| B11 ✅ | med | *(fixed: `buildTriggerAliases` expands exact `sender`/`chat_jid` values through `whatsmeow_lid_map` at load)* A trigger typed as a phone number matched nothing: 1:1 chats are LID-addressed now (§3.2 finding 6) and `matchesTrigger` compares raw strings. It also silently broke the existing `me-number` webhook, which came back to life on deploy. See §2.15. | `webhook/manager.go` |
| B12 | **med** | **100 chats are named after the user.** `GetChatName` falls back to the message's `sender` when the contact lookup fails; if the first message stored in a 1:1 chat is your own, the chat is named after *you*. `62156783493249@lid` (the contact `972547669192`, correctly named on its phone-JID chat) reads `14018823594220` — the user's own LID. 100 chats carry the user's own LID or number as their name (52 with audio), so the chat picker and the Downloads view show them as indistinguishable. 659 `@lid` chats are named after some bare number. Pre-existing; surfaced by Phase 7. Fix: never name a 1:1 chat after a from-me sender, prefer the paired phone chat's name via `whatsmeow_lid_map`, then a one-off repair of `chats.name` (regenerable per §4.3). | `whatsapp/handlers.go:85` |
| B10 ✅ | low | *(fixed in Phase 1, §2.11)* `PUBLIC_BASE_URL` defaults to `http://localhost:8080`, not set in `docker-compose.yaml`, so `download_url` is wrong from inside the MCP container. `webhook/manager.go:213` still hardcodes `http://localhost:8080/api/download`. | config, compose, webhook |

### 2.4 Next steps (in order)

1. **Decide on re-pairing** (S1 warning). ✅ Work committed on `webhook` (`235988f`; `store/media/` added to `.gitignore`). Exposure check: Docker publishes 8080 on `0.0.0.0` and the host firewall accepts everything, so reachability depends on the Oracle VCN security list (not visible from the VM). The bridge keeps no HTTP access log, so there's no record of whether the file was fetched. ❓ Re-pairing: pending your decision.
2. ✅ **Measure the fresh-media rate after S2.** Run a batch of `limit: 20` on a chat with recent audio. Record how many succeed directly vs need a retry. This sets expectations for the full run.
   - **Result (2026-09-19 14:39 UTC, job `job_20260919_143919_283`):** `18674685612157@lid`, expanded to it plus `972523996302@s.whatsapp.net`. The 20 newest items (15–18 Sep; 8 from me, 12 from the contact): **20/20 `ok` in 9 s, 0 media retries**. 518,863 bytes total, all with an `OggS` header, `index.json` 20× `ok` in `store/media/972523996302/`.
   - Integrity: the chat's 20,095 `messages` rows are byte-identical before/after; `quick_check` = `ok`.
   - Idempotency: re-running the same job gives `skipped: 20`, `done: 0`, no new files.
   - Takeaway: the direct path works for recent media. The boundary was then measured in step 3 (§2.5): links last 11–19 days.
3. ✅ **Diagnose B1**. Result in §2.5: diagnosed, no fix with the current protocol. Original plan kept for reference: fetch the raw blob from the fresh direct path without whatsmeow's hash check, then
   - verify its HMAC and decrypt it with the **original** `media_key` (same code as `scripts/download_chat_audio.py`);
   - if HMAC + plaintext SHA-256 pass → add this as the post-retry fallback (plaintext hash still enforced, so integrity holds);
   - if HMAC fails → the phone re-encrypted with a new key; the only route is the placeholder resend (keep B7's path but fix it), or accept these as unrecoverable.
   - Also try the upgraded whatsmeow (Phase 0) on a copy: its retry/media changes may matter here.
4. ✅ **Fix B2, B3, B5** (and B4, B7, the `index.json` overwrite; see §2.6) (make the job resumable and honest): map `ErrMediaNotAvailableOnPhone` / `NOT_FOUND` to `expired_not_on_phone`; on startup mark leftover `running` jobs `interrupted`; use `MEDIA_RETRY_TIMEOUT_S`.
5. ✅ **Fix B6, B8** (B4, B7 were done in step 4). See §2.7: one folder-key function, returned in the batch response; `after`/`before` normalised to the stored format.
6. ✅ **Missing tests**: LID expansion, cache hit, retry waiter (fake `events.MediaRetry`), "no writes to `messages`". See §2.8.
7. ✅ **Execute §5.5**: baseline (stop, back up, `integrity_check`, dump the chat's rows) → live run on `972523996302` with full history (rehearsal skipped per decision, unavailable audio marked failed without blocking on retry timeouts) → after-run diff. Verified: `diff` is empty, 106 `.ogg` files saved, `media.db` and `index.json` cover all 4,062 items, re-run skipped 4062/4062. Definition of done §5.6 100% met.
8. ✅ **Phase 0** (§2.10), ✅ **Phase 1** (§2.11), ✅ the **backlog sweep** (§2.13) and ✅ **Phase 4** (§2.14, §2.15) are done and deployed. Open items are collected in **§2.16**.

### 2.5 B1 investigation (2026-09-19, 14:58–15:03 UTC)

**Link lifetime.** Single downloads from the target chat (`18674685612157@lid`), newest to oldest:

| Item | Age | From | Result |
|---|---|---|---|
| `AC9DC47F…`, `ACC8615E…` | 7–8 days (11–12 Sep) | them, me | direct ✅ |
| `AC6FD363…`, `ACD7806B…` | 11 days (8 Sep) | them, me | direct ✅ |
| `AC9F58C8…`, `AC7A2751…` | 19–20 days (30–31 Aug) | me, them | 403/404/410 → retry → B1 ❌ |
| `AC8AC1AE…` | 37 days (13 Aug) | me | 403/404/410 → retry → B1 ❌ |

CDN links last between 11 and 19 days. Anything older depends on media retry.

**What the retry returns** (3 items, both directions):
- The phone answers `SUCCESS` with a fresh direct path in 1–2 s. The notification is AES-GCM encrypted with a key derived from **our** stored media key and decrypts fine, so the phone does hold the original key.
- The protobuf has only the 4 known fields: no unknown fields, empty `messageSecret`. The newer whatsmeow (`20260917111002`) defines the same 4 fields.
- The uploaded blob is exactly the expected size for the original plaintext: `file_length` padded to 16 bytes, plus the 10-byte MAC (26,038→26,058, 45,782→45,802, 10,248→10,266). So it's the same content.
- Its ciphertext hash differs, and **its MAC fails with the original key** under every HKDF label (audio, image, video, document, sticker, thumbnail). Decrypting anyway doesn't produce `OggS`. So the phone re-encrypted it with a **different key**, and that key isn't in the notification.
- The placeholder-resend request (B7) got no response within 20 s. The chat's 20,095 `messages` rows stayed byte-identical.
- Known upstream: [Baileys #2714](https://github.com/WhiskeySockets/Baileys/issues/2714) reports re-keyed media-retry answers for LID-migrated chats. It was closed with no fix.

**Code added** (kept; it's a safe, strictly verified fallback plus diagnostics):
- `internal/whatsapp/media_decrypt.go`: `fetchRawMedia` and `decryptMediaBlob` (MAC with the original key, then plaintext SHA-256 must match). Test in `media_decrypt_test.go`; it caught an in-place decryption bug in `cbcutil.Decrypt`.
- `downloadWithMediaRetry` now uses this fallback on a ciphertext-hash mismatch, logs the blob size, hashes and unknown proto fields, and fails immediately instead of waiting out the 30 s timeout.
- On a failed retry, `media.db` stores the **fresh** direct path, so a re-upload can be re-examined offline (links last ~2 weeks). The probe script is not in the repo.

**Consequence for Milestone 1:** with the current protocol, only voice notes from roughly the last 11–19 days can be downloaded. For the target contact that's a few dozen of 4,062. The ≥ 95 % goal in §5.6 isn't reachable for older history.

**Options:**
1. **Accept and record honestly (recommended now).** Add a status `rekeyed_on_phone` for this case. Skip it on re-runs unless `retry_failed=true`, and drop the placeholder peer request (B7), so no more requests go to phones for items that can't succeed. Change the §5.6 target to "every item has an explicit reason".
2. **On-demand history sync** (Phase 5.2): `BuildHistorySyncRequest` makes the phone resend older messages. The resent messages may carry the phone's current key and path. Needs care: `HandleHistorySync` stores through `INSERT OR REPLACE`, which would rewrite rows. So capture keys into `media.db` instead of `messages`, or do §4.3 first. Uncertain payoff; test on one message.
3. **Manual export from the phone** ("Export chat" → include media): gives all voice notes the phone still has, as `.opus` files. No protocol risk; one chat at a time; filenames don't carry message IDs.
4. **Upstream:** report to whatsmeow with the evidence above. Upgrading whatsmeow alone won't help (same proto).

**Decision (2026-09-19): option 1**, implemented in §2.6. Options 2–4 stay open.

### 2.6 Failures recorded (2026-09-19, 15:22–15:28 UTC)

| Change | Where |
|---|---|
| Status constants and `IsPermanentDownloadFailure` (`expired_not_on_phone`, `rekeyed_on_phone`, `incomplete_metadata`). Batch re-runs skip these unless `retry_failed=true`, so no retry request reaches a phone for them. | `internal/database/media_store.go`, `internal/api/handlers.go` |
| `retryFailureStatus` classifies with `errors.Is`: MAC mismatch → `rekeyed_on_phone`; `ErrMediaNotAvailableOnPhone` or `NOT_FOUND` → `expired_not_on_phone` (immediately, not after the timeout); timeout → `retry_timeout`; anything else → `failed`. | `internal/whatsapp/download.go` |
| Placeholder-resend peer request and waiter removed (B7). The retry wait is a single `select` with `MEDIA_RETRY_TIMEOUT_S` (B5). | `download.go`, `client.go`, `main.go` |
| Leftover `running` jobs → `interrupted` at startup (B3). `DELETE` cancels only running jobs, 404 otherwise (B4). | `media_store.go`, `main.go`, `handlers.go` |
| `index.json` is rebuilt from every audio message of the chat that has a `media.db` record, instead of only the current job's items (a 1-item job had shrunk it to 1 entry). | `handlers.go` `writeBatchIndexJSON` |
| Tests: `TestRetryFailureStatus`, `TestIsPermanentDownloadFailure`, `TestJobInterruptAndCancel`. | `*_test.go` |

**Existing records:** backed up `media.db` to `store/backups/media_20260919_152251.db`, then set the 3 rows with a proven MAC mismatch (`AC9F58C8…`, `AC7A2751…`, `AC8AC1AE…`) to `rekeyed_on_phone`. The 2 older rows showing only "hash of media ciphertext doesn't match" (`ACEEA1AA…`, `3A5E2DD4…` in `244332250583270@lid`, from before the MAC check existed) stay `failed`: likely B1, but unproven. The next run retries them once and records the verified status.

**Verified live:** a batch covering only `AC9F58C8…` → `skipped: 1`, 0 retry requests. `DELETE` on a finished job → 404, status stays `completed`. `index.json` for `972523996302` → `ok=24 rekeyed_on_phone=3`, matching the 24 files. The chat's 20,095 `messages` rows are byte-identical to the snapshot taken before step 2.

**`media.db` now:** `ok` 25 · `rekeyed_on_phone` 3 · `failed` 3 (2 unproven B1, 1 `context canceled`) · `retry_timeout` 1.

### 2.7 Folder key and date filter (2026-09-19, 15:40–15:50 UTC)

| Change | Where |
|---|---|
| **B6:** `ChatFolderKey(jids)` is the only folder rule: group → full JID sanitised (`972523531857-1556783116_g_us`); else phone number (`972523996302`); else the LID user part. It keeps the existing folder names. The per-message download resolves a chat's key once and caches it (`Client.folderKeys`), instead of opening `whatsapp.db` per message. The batch response returns `folder` (e.g. `media/972523996302`), which `tests/download-chat-audio.sh` now prints instead of guessing with `tr`. | `internal/whatsapp/download.go`, `client.go`, `internal/api/handlers.go`, `tests/download-chat-audio.sh` |
| **B8:** `NormalizeTimeFilter` converts `after`/`before` to the stored format (`YYYY-MM-DD HH:MM:SS+00:00`; all 9,850 audio timestamps use it). Accepts RFC 3339 with any offset, `YYYY-MM-DD HH:MM:SS` (UTC) or `YYYY-MM-DD`. An unrecognised value → 400 from the batch endpoint. | `internal/database/messages.go`, `handlers.go` |
| Tests: `TestChatFolderKey` (pins the existing folder names), `TestNormalizeTimeFilter`, `TestListAudioMessagesTimeFilter` (the same instant in three formats selects the same rows). | `*_test.go` |

**Verified live:**
- A batch with `"chat":"972523996302"`, `after:"2026-09-15T00:00:00Z"`, `before:"2026-09-19T00:00:00Z"` → `total: 23`, matching a direct SQL count. `folder: media/972523996302`, `done: 3` (the 3 not yet downloaded, direct, no retry), `skipped: 20`.
- `after:"yesterday"` → 400.
- The group batch reports `media/972523531857-1556783116_g_us`, the existing folder. No new folders were created.
- `index.json` for the target contact: `ok=27 rekeyed_on_phone=3`, 27 files. The chat's 20,095 `messages` rows are byte-identical to the original snapshot.

### 2.8 Missing tests added (2026-09-19)

All use `t.TempDir()`; none touches `store/`. The bridge suite is now 31 tests, all passing.

| Test | Covers |
|---|---|
| `TestResolveChatJIDs`, `TestResolveChatJIDsWithoutSessionDB` (`internal/whatsapp/lid_test.go`) | Phone number (plain and formatted), phone JID, device suffix, LID → phone, unmapped LID/phone, group, broadcast, invalid input. Uses a temp `whatsapp.db` with whatsmeow's real `whatsmeow_lid_map` schema. With no session DB, the input comes back unchanged and no file is created. |
| `TestDownloadMediaCacheHitOnDisk`, `…CacheHitFromRecord`, `…IncompleteMetadata`, `…RejectsNonAudio` (`download_flow_test.go`) | The cache paths and the metadata check return without network access. The `Client` has no WhatsApp connection, so any network call would panic. Missing key → `incomplete_metadata` (permanent). Text or unknown message → error, nothing recorded. |
| `TestMediaRetryWaiter`, `TestMediaRetryTimeoutDefault` | The event reaches only its own waiter. A duplicate on a full buffer doesn't block the whatsmeow event loop. Unregistered or `nil` events are dropped. Timeout defaults to 30 s and can be set. |
| `TestDownloadQueriesAreReadOnly` (`internal/database/readonly_guard_test.go`) | Runtime guard: switches the store's single connection to SQLite `query_only` and confirms a real write is rejected. `GetMediaInfo` and `ListAudioMessages` (with date filters) still work, so they issue no writes or DDL. |
| `TestDownloadCodeOnlyReadsMessages` (`download_guard_test.go`) | Source guard: parses the download code (`download.go`, `lid.go`, `media_decrypt.go`, `server.go`, the 6 download handlers) and fails on any `messageStore` call other than `GetMediaInfo`/`ListAudioMessages`, or any `db` call other than `Query`/`QueryRow` inside those two. Checked that it fails when a `StoreMessage` call is injected into `runBatchDownload`. |

Found while writing them: `StoreMessage` silently skips a message with no content and no media, so a guard check using such a message proves nothing. The test uses a message with content.

### 2.9 Full-history run (2026-09-20, 11:23–12:23 UTC)

Two changes were needed to finish the run in an hour instead of days, both committed with it:

| Change | Where | Why |
|---|---|---|
| `skip_media_retry` on the batch request → `DownloadOptions{SkipMediaRetry}`. An expired link is recorded as `failed` immediately, no receipt is sent to any phone, per-item timeout 15 s instead of `MEDIA_RETRY_TIMEOUT_S + 30 s`. When it is set, `failed` rows are skipped on re-runs too (without it they are still retried, since `failed` is not permanent). | `handlers.go`, `download.go` | The first attempt (`job_20260920_112340_853`) sent real retry receipts: 79 `ok` and 375 failures in 37 min, on course for ~2 days and ~4,000 requests to the two phones, all expected to end `rekeyed_on_phone` (B1). Cancelled. |
| `GetDownloadAnyChat(messageID)`: when a chat has no record for a message, an existing record for the same message ID under another chat JID is copied onto this chat JID. | `media_store.go`, `handlers.go` | The same voice note exists under both the phone JID and the `@lid` JID. Without this, the LID half of the chat re-downloaded (or re-requested) everything already handled under the phone number. |

**Result:** `job_20260920_120538_681` covered all 4,062 items in 17 min (755 skipped from the cancelled runs, 3,307 attempted). Totals and the empty before/after `diff` are in §2.1. The follow-up job `job_20260920_122330_667` skipped all 4,062 in 18 s.

> [!NOTE]
> The 3,619 `failed` rows are almost all `skip_media_retry` refusals, not proven failures. A future run with the flag off would send ~3,600 retry receipts to the two phones and, on the B1 evidence, turn nearly all of them into `rekeyed_on_phone`. Only do that if B1 ever gets a fix (§2.5 options 2–4).

### 2.10 Phase 0 deployed (2026-09-20, 20:22–20:29 UTC)

Checklist §4.6, run in order:

| Step | Result |
|---|---|
| Baseline | `messages` 476,670 · `chats` 3,936 · `webhook_configs` 7 · `contact_nicknames` 0 · `whatsmeow_version` `14|8` |
| `docker compose stop` | clean, no `-journal` / `-wal` left |
| Backups + `integrity_check` | `store/backups/{messages,whatsapp,media}_20260920_202211.db`, all three `ok` |
| Rollback tag | `whatsapp-bridge:pre-phase0` = `d034c3f39f45` (the 12:03 image) |
| `docker compose up -d --build` | ⚠️ **started the stale container instead of replacing it** — the running image was still the morning's, `whatsmeow_version` stayed `14|8`, the new binary never ran. This is the §4.6 step 7 caveat; `docker compose up -d --force-recreate` fixed it. Check the running image ID, not just "Up/healthy". |
| Migration | `whatsmeow_version` `14|8` → **`15|8`**; `whatsmeow_device.companion_meta_nonce TEXT NOT NULL DEFAULT ''` present. Exactly the one predicted migration. |
| Reconnect | `✓ Connected to WhatsApp!` at 20:26:58, **no QR prompt**, healthy |
| `messages.db` | `quick_check` `ok`; all four baseline counts identical; 8 tables, unchanged |
| API | `POST /api/download` on a cached item → 200 `cached:true`; `GET /api/media/<chat>/<file>.ogg` → 200, `audio/ogg; codecs=opus`, 3,968 bytes; `GET /api/media/../whatsapp.db` → **404** (S1 guard intact) |
| Batch | `job_20260920_202820_316` on `972523996302`, limit 25: `done: 4`, `skipped: 21`, `failed: 0`. The 4 are voice notes that arrived since the full run — direct downloads on the new whatsmeow. `media.db` `ok` 107 → **111**. |
| MCP container | rebuilt from the pinned `requirements.txt`: `mcp 1.30.0` (not 2.x), `gradio 5.34.2`, `starlette 0.52.1`, `uvicorn 0.53.0`, `requests 2.34.2`. The pin is now proven by a real build, not just a dry run. |

**Rollback if needed:** `docker compose stop`, restore `store/backups/*_20260920_202211.db`, run `whatsapp-bridge:pre-phase0`. As measured in §6, the old binary also opens the migrated `(15, 8)` DB, so restoring the backup is optional.

### 2.11 Phase 1 deployed (2026-09-21, 07:34–07:38 UTC)

Schema v1 is live. Checklist §4.6:

| Step | Result |
|---|---|
| Rehearsal (step 4) | Ran `cmd/migrate` against a consistent `.backup` copy of the live `messages.db` **with no downtime** — the migration touches only `messages.db`, so the bridge kept running. All **476,670** rows byte-identical (918,820-line dump, `diff` empty), `quick_check` `ok`, the four columns added, 0 rows non-NULL. |
| Rollback rehearsal | Against the migrated copy, the old binary's own statements still work: its `GetMessages` and `GetMediaInfo` column lists, and its 13-column `INSERT OR REPLACE` (the new columns simply take NULL). No `SELECT *` anywhere, so extra columns are invisible to it. |
| Baseline | `messages` 476,716 · `chats` 3,936 · `webhook_configs` 7 · `contact_nicknames` 0 · 8 tables |
| Stop + backups | clean stop, no journal left; `store/backups/{messages,whatsapp,media}_20260921_073405.db`, all `integrity_check` `ok` |
| Rollback tag | `whatsapp-bridge:pre-phase1` = `cdc6bf21f2b9` |
| Deploy | `docker compose up -d --build --force-recreate` (never plain `--build`, see §2.10); running image `289451439f10`, distinct from the tag |
| Migration | log line `messages.db schema version 1`; `schema_migrations` has `1 | 2026-09-21 07:36:23`; columns 13–16 are `direct_path`, `mimetype`, `seconds`, `is_ptt` |
| `messages.db` | `quick_check` `ok`; all four baseline counts identical; full row dump **byte-identical** (0 lines changed, 0 added) |
| API | `POST /api/download` on a cached item → 200. `media_type: "voice"` → job with `total: 0` (correct: only messages received from now on carry `is_ptt`). `media_type: "audio"` → normal. `media_type: "bogus"` → **400** with a clear message. |
| B10 fixed | `download_url` is now `http://whatsapp-mcp-whatsapp-bridge-1:8080/api/media/...`, and the MCP container fetched it: **200, 3,968 bytes, `OggS`**. Previously it read `localhost:8080`, which only resolved inside the bridge container. |

**Confirmed live (2026-09-21, 10:20–10:34 UTC).** 166 messages stored since the deploy, 3 of them audio, all 3 tagged correctly — in both directions:

| id | chat | from | seconds | is_ptt | direct_path |
|---|---|---|---|---|---|
| `AC01F868…` | `62156783493249@lid` | me | 5 | 1 | captured |
| `3A7A9BB6…` | `62156783493249@lid` | them | 6 | 1 | captured |
| `3AE9AF69…` | `62156783493249@lid` | them | 3 | 1 | captured |

Filenames are `voice_<msgID>.ogg`, mimetype `audio/ogg; codecs=opus`, and no row was tagged `is_ptt = 0` (no audio file attachments arrived). The whole chain then worked end to end: a batch with `media_type: "voice"` on that chat found exactly those 3, downloaded **3/3 directly with no media retry**, and wrote three valid `OggS` files to `store/media/972547669192/`. `media.db` `ok` 111 → 114.

**Follow-up done (2026-09-21, 10:40 UTC).** `resolveDirectPath` now prefers the stored `direct_path` and falls back to `ExtractDirectPathFromURL` only for pre-v1 rows, removing a parsing step — and a repeat of S2 — from the download path for new media.

Checked before trusting it: the protobuf's `direct_path` **does** carry the `ccb`/`oh`/`oe`/`_nc_sid` query string, so it is character-for-character what the fixed parser produces. Had it been the bare path, this "cleanup" would have dropped the CDN auth token and broken every new download. `TestStoredDirectPathMatchesDerived` pins that equivalence on a real url/direct_path pair, so a future divergence fails the suite instead of failing silently.

Verified live: one downloaded voice note was moved aside and re-fetched through `POST /api/download` → `cached: false` (a real download, not the cache), 9,360 bytes, **SHA-256 identical** to the original, valid `OggS`, and **0 media retries**. `GetMediaInfo` selects the new column; `MediaInfo.DirectPath` carries it. Suite is 40 tests.

### 2.12 Where things stand, and what to do next (2026-09-21)

**Live state, measured:**

| Check | Value |
|---|---|
| Deployed | whatsmeow `20260917111002`, Go 1.26, `messages.db` schema **v1**, `whatsmeow_version` `15\|8` |
| `messages.db` | 476,887 messages · 9,876 audio rows · 3 rows tagged `is_ptt` (all since the v1 deploy) |
| `media.db` | `ok` **114** · `rekeyed_on_phone` 339 · `expired_not_on_phone` 1 · `failed` 3,619 · `retry_timeout` 1 |
| Files | `972523996302/` 110 · `972547669192/` 3 · `972523531857-1556783116_g_us/` 1 · `972527872233/` 11 (standalone script) |
| Bridge suite | 40 tests, `go vet` clean |
| Rollback tags | `whatsapp-bridge:pre-m1`, `:pre-phase0`, `:pre-phase1`, `:pre-directpath` |

**The finding that should drive the next step.** Milestone 1 captured one contact. Meanwhile every other chat keeps producing voice notes, and the CDN window is 11–19 days (§2.5), after which B1 makes them unrecoverable for good. Counting audio from the last 14 days that is still inside that window:

| | |
|---|---|
| Audio messages in the last 14 days | 268 |
| Already downloaded (`ok`) | 104 |
| **Still downloadable, not captured** | **164, across 35 chats** |
| Rate | ~19 audio messages/day |

Those 164 are recoverable today and will not be recoverable next week. 3,619 items are already past saving. **The archive is leaking at roughly 19 items a day, and nothing currently stops it.**

**Proposed next step — two parts:**

**A. Sweep the backlog now (no new code).** Loop the existing batch endpoint over the 35 chats with uncaptured recent audio, `skip_media_retry=true` so no expired item pesters a phone. Everything it needs already works and is proven: LID expansion, folder keys, idempotent re-runs, explicit per-item status. Expect most of the 164 to land directly. This is hours of value for minutes of work, and it is time-sensitive.

**B. Then Phase 4 (auto-download, goal G2) — brought forward, sliced.** This is the only change that stops the leak permanently, which is why it should come before Phases 5 and 2/3:

- **One listener model, no throwaway step.** Auto-download becomes an *action* on the webhook trigger rules that already exist: a new `auto_download` checkbox on an ordinary webhook entry, defined in the same UI and the same `POST /api/webhooks` as every event today. The earlier env-var slice (`AUTO_DOWNLOAD_VOICE` + an allowlist in `docker-compose.yaml`, shipped first and deleted later) is dropped — see **Phase 4** in §6 for the full design.

**Why this re-orders the documented plan.** Phase 5's history request is explicitly low-payoff: B1 means resent history most likely comes back re-keyed (§2.5 option 2). Phase 2/3 is ergonomics over a capability that already works through the API. Neither stops the leak; Phase 4 does, and it is smaller than either.

**Cheap parity fix worth folding in wherever it lands:** the MCP tool `batch_download_chat_audio` still exposes only `chat`, `limit`, `skip_cached`, `retry_failed`, so `media_type`/`voice`, `after`, `before` and `skip_media_retry` are unreachable from the MCP side (§5.2).

### 2.13 Backlog sweep complete ✅ (2026-09-21, 10:54–10:56 UTC)

Part A executed cleanly with zero code changes.

- **Execution:** Backlog sweep looped over all 35 chats with uncaptured audio messages from the last 14 days (`after: "2026-09-07"`, `skip_cached: true`, `skip_media_retry: true`).
- **Result:** **164 of 164 uncaptured items downloaded directly (100% success rate)** across all 35 chats in 100.2 seconds.
- **Failures / Retries:** **0 failed, 0 retry receipts sent to phones.**
- **Recent audio coverage (last 14 days):** 268 total audio messages in `messages.db` across the entire account; **268 / 268 now downloaded (`ok`)**. The leak is completely stopped for the recoverable window.
- **`media.db` status totals:**
  - `ok`: **268** (104 pre-existing + 164 from this sweep)
  - `rekeyed_on_phone`: 339 (from earlier Milestone 1 run on older history)
  - `expired_not_on_phone`: 1
  - `failed`: 3,619 (0 new failures added)
  - `retry_timeout`: 1
- **Files on disk:** 279 audio files in `store/media/` across 36 chat folders (276 valid `OggS` Opus voice notes + 3 valid MPEG AAC audio forwarded in groups), each with updated `index.json`.
- **Database safety:** `messages.db` and `whatsapp.db` byte/table integrity fully preserved (0 writes to `messages.db`).
- **Next:** Part B — Phase 4 (auto-download) to capture incoming voice notes live as they arrive.

### 2.14 Phase 4 deployed ✅ (2026-09-21, 11:32–11:39 UTC)

Full §4.6 checklist, no step skipped.

| Step | Result |
|---|---|
| Image tagged | `whatsapp-bridge:pre-phase4` (`7b1488c4f6e6`) |
| `docker compose stop` | no `-journal`/`-wal`/`-shm` left in `store/` |
| Backups + `integrity_check` | `messages_20260921_113202.db`, `whatsapp_…`, `media_…` — all three `ok` |
| Rehearsal on a fresh copy | v2 applied in ~0.1 s; baseline diff **only** `schema_version` 1→2; 5 oldest audio rows byte-identical; `quick_check` `ok` |
| `docker compose up -d --build` | healthy in 19 s, **no QR re-pair**; log reads `messages.db schema version 2` and `Auto-download started with 2 worker(s), queue size 100` |
| Live DB after | baseline diff **only** `schema_version` 1→2; all 6 webhook rows byte-identical; `quick_check` `ok`; every existing config reads `auto_download = 0`, `auto_download_voice_only = 1` |
| Rollback drill (step 9) | the pre-phase4 binary's exact `SELECT`/`INSERT` on `webhook_configs` both work against v2, and its inserts take the column defaults |

**Live verification.** One listener created through `POST /api/webhooks` at 11:36:32 — *"Capture voice notes"* (id 10): no URL (download-only), `auto_download` on, voice-notes-only, trigger `media_type = audio`. The bridge logged it **without a restart**, which is the whole point of putting rules in the database.

At 11:38:51 a real voice note arrived in `62156783493249@lid` and was captured in **475 ms**:

```
Auto-downloaded 3A1F647EBD8CE7F8018E in 62156783493249@lid to
media/972547669192/2026-09-21_113850_them_3A1F647EBD8CE7F8018E.ogg
in 475ms (rule 10 "Capture voice notes", worker 1)
```

8,840 bytes, valid `OggS` header, `ok` in `media.db`, and the `messages` row carries `is_ptt = 1`, `seconds = 3`, `mimetype = audio/ogg; codecs=opus`. **Zero media retry receipts** were sent (`SkipMediaRetry`, as designed). The exclusion half of the check is covered by unit tests only: no non-audio message happened to arrive in the observation window, and the deployed rule is account-wide by design.

> [!IMPORTANT]
> **B11 (new, med): a `sender` or `chat_jid` trigger typed as a phone number silently matches nothing.**
> Every recent 1:1 audio message lives in a `@lid` chat with a LID sender (`62156783493249@lid`, sender `62156783493249`; own messages `14018823594220`) — no phone numbers anywhere, per §3.2 finding 6. `Manager.matchesTrigger` compares raw strings, while `ResolveChatJIDs` — which already expands phone ↔ LID for downloads — is not on that path. So a rule added as `972523996302` looks correct and captures nothing. This affects existing **webhook delivery** too, not just auto-download. Fix: expand `sender`/`chat_jid` trigger values through `whatsmeow_lid_map` when configs are loaded. Workaround until then: use a `media_type` trigger, or paste the `@lid` JID.

### 2.15 Schema v3: visible captures and LID-aware triggers ✅ (2026-09-21, 11:53 UTC)

Two gaps that only showed up once the feature was in a user's hands.

**1. A capture left no trace anywhere a user looks.** `media.db` recorded it and the bridge
logged it, but a download-only listener has no delivery rows, so the UI's per-listener log
view — the obvious place to check — was empty. Deciding against an `auto_download_logs`
table in §6 was right; leaving captures unlogged was not. Schema **v3** adds
`event_type TEXT DEFAULT 'delivery'` to `webhook_logs`, and each capture writes a row there:
same table, same endpoint, same Logs button, rendered as `CAPTURED` / `CAPTURE FAILED` with
the saved path and the duration. Existing rows keep their meaning (10,384 of them read
`delivery` after the migration).

**2. B11 fixed.** `buildTriggerAliases` expands every **exact** `sender` / `chat_jid`
trigger through `whatsmeow_lid_map` when configs load, so a rule typed as a phone number
matches the `@lid` chat the contact actually writes from. `contains` and `regex` are left
alone — widening a pattern would change what the user wrote. Group JIDs are unaffected. The
expansion runs once per load, not per message (the §2.7/B6 mistake).

The deploy proved the bug was live and not theoretical: the existing `me-number` webhook
immediately reported
`Trigger 15 (sender=972526996141) also matches [14018823594220@lid 14018823594220 …]`.
It had been matching nothing since that chat moved to LID addressing.

**Deploy note (cost an hour):** after `docker compose stop`, `docker compose up -d --build`
**starts the existing stopped container** rather than replacing it, so the freshly built
image is ignored and the old binary runs on. The log said `schema version 2` while the code
on disk was at v3. §4.6 step 7 already warns about this — follow it: use
`up -d --build --force-recreate`, and confirm the deploy by the schema version in the log,
never by the build output.

### 2.16 Open items (as of 2026-09-21, after Phase 4)

All three goals in §1 are met for media inside the CDN window. What is left, worst first:

**Security / operational**

| # | Item | Notes |
|---|---|---|
| O1 | **Re-pairing after S1 is still undecided** (§2.4 step 1) | The session DB was served on an open port for an unknown window. No access log exists, so there is no way to tell whether it was fetched. Your call. |
| O2 | **Port 8080 is still public and unauthenticated** ⚠️ **worsens with Phase 7** | `/api/download`, `/api/media/` and `/api/webhooks` all answer anyone who can reach the host. S1 is fixed, but the surface is unchanged. Phase 7 adds `/api/chats` (the whole address book) and `/api/downloads` (the media index) with auth declined on 2026-09-21, so the exposure grows. Bind to localhost or the Docker network, or add token auth. |
| O3 | **No retention policy for captured media** | Negligible at voice-note volume (281 files, ~30 MB). Becomes load-bearing the moment capture widens to images or video. Planned as §6 Phase 7.5 (per listener, schema v4). |
| O4 | **`me-number` webhook is live again** | It matched nothing for months (B11) and now fires on your own messages. If n8n acts on it, check that before it surprises you. |

**Known bugs**

| # | Item | Notes |
|---|---|---|
| B1 | Media retry returns re-keyed copies | No fix known; anything past ~11–19 days is unrecoverable. Recorded honestly as `rekeyed_on_phone`. |
| B12 | 100 chats named after the user | Makes the chat picker and the Downloads view show 100 different conversations under one identical name. Recommended next, before 7.4 — see §2.3. |
| B9 | Cosmetic leftovers | `MediaDownloader` in `media.go` is still unused; `gofmt` flags `whatsapp/client_test.go` and `whatsapp/event_handlers_test.go`; the Python `download_media` error-key check is unconfirmed. |

**Gaps in what shipped**

| # | Item | Notes |
|---|---|---|
| G-a | **Auto-download captures audio only** | The gate is hard-limited to `media_type == "audio"`; images, video and documents are never queued even with a matching trigger. Deliberate (goal G2 is about voice), but it is the first thing a user asks for after seeing an image arrive. A contained change if wanted. |
| G-b | **The exclusion path is unit-tested, not live-proven** | Every live message since deploy that reached a rule was audio, so "a non-matching message is correctly skipped" has never been demonstrated on real traffic. |
| G-c | **No chat picker in the UI** | A rule still means typing a phone number or pasting a group JID. B11 made phone numbers work; it did not make them discoverable. Planned as §6 Phase 7.1. |
| G-d | **The UI has no lint or tests** | There is no JS toolchain in this environment; `script.js` changes are read-checked and then exercised by hand. Planned as §6 Phase 7.4 (jsdom in a container, plus a deploy smoke check). |
| G-e | **Rollback was verified by query, not by drill** | The pre-phase4 binary's exact `SELECT`/`INSERT` were run against a v3 copy and work. The old image was never actually booted against a migrated database (§4.6 step 9 remains partly owed). |

**Later phases, unchanged in scope:** Phase 2 (MCP fallback download, `list_messages(media_type)`, MP3 conversion), Phase 3 (MCP tools and a Gradio tab), Phase 5 (batch by sender; the history request is explicitly low-payoff because of B1), Phase 6 (transcription). Plus the §5.2 parity fix: `batch_download_chat_audio` still exposes only `chat`, `limit`, `skip_cached`, `retry_failed`.

---

## 3. Background

### 3.1 Architecture

```
WhatsApp network
      │ whatsmeow (WebSocket)
      ▼
whatsapp-bridge (Go, :8080)  ── REST: /api/send, /api/webhooks, /api/download, /api/download/batch, /api/media/
      │  writes store/messages.db (messages, chats, …) — the download code only SELECTs
      │  writes store/media.db (download bookkeeping, new)
      │  writes store/media/<key>/*.ogg (new)
      ▼  shared bind mount ./store
whatsapp-mcp-server (Python, :8081/:8082) — FastMCP tools + Gradio UI, ffmpeg (audio.py)
```

Mounts differ: bridge sees `/app/whatsapp-bridge/store`, MCP sees `/app/store`. Exchange paths **relative to `store/`**.

### 3.2 Findings (original analysis, updated)

1. **`/api/download` was missing** after the refactor in `fe89097` (upstream `0487e16` had it). ✅ Restored in Milestone 1. ✅ `webhook/manager.go` now builds the URL from `PUBLIC_BASE_URL` (B10 fixed, §2.11).
2. **Decryption material is stored** for 9,845 of 9,847 audio rows (`url`, 32-byte `media_key`, `file_sha256`, `file_enc_sha256`, `file_length`). ✅ Since schema v1 (§2.11) new audio rows also store `direct_path`, `mimetype`, `seconds` and the **`ptt` flag**; rows predating v1 have those NULL and still have the direct path recovered from `url` **including its query string** (S2 — the original upstream extraction was wrong). ✅ Audio files are now named `voice_<msgID>.ogg` / `audio_<msgID>.ogg` instead of from `time.Now()`.
3. **No audio filter / batch** in the MCP layer. Batch now exists in the bridge; `list_messages` still has no `media_type` filter.
4. **CDN links expire** (~1–2 weeks in practice; the `oe=` value is not reliable either way). Recovery is whatsmeow's media retry: `SendMediaRetryReceipt` → `events.MediaRetry` with a fresh direct path. Needs the sender's phone online and still holding the file. In practice the re-upload uses a new key we don't receive (B1, §2.5), so retry doesn't recover anything today.
5. **Format:** voice notes are Opus in Ogg, mono, ~16 kbps. The MCP container has ffmpeg.
6. **One contact = two chat JIDs.** 1:1 chats moved to LID addressing; the phone-number chat stops around 2026-07-19 and the `@lid` chat continues. "All voice notes from X" must union both via `whatsmeow_lid_map` (19,241 rows). ✅ `ResolveChatJIDs`.
7. **Old rows can't tell voice notes from audio files** (no `ptt`). Milestone 1 downloads all audio; nearly all 1:1 audio is voice notes anyway.
8. **Stored filenames collided** (second resolution). ✅ Fixed for audio in schema v1 (named from the message ID); image/video/document still use `time.Now()`. Downloaded files on disk are named by timestamp + direction + message ID; the `filename` column is never used or changed.
9. **About half of each chat is `from_me`.** Their media retry goes to your own phone.
10. **Webhook trigger model, reused for G2 ✅:** `WebhookTrigger{TriggerType: chat_jid|sender|keyword|media_type|all, TriggerValue, MatchType: exact|contains|regex}`, matched by `Manager.matchesTrigger`. Auto-download is an action on this same model rather than a second rule engine (§6 Phase 4). Exact `sender`/`chat_jid` values are expanded through `whatsmeow_lid_map` at load, without which a phone number matches nothing (B11, §2.15).
11. **whatsmeow:** ✅ now on `v0.0.0-20260917111002-2e338d0ee73d` with Go 1.26 (§2.10); was `v0.0.0-20260718134955-fac667d55293`. Relevant APIs: `DownloadMediaWithPath(ctx, directPath, encSHA256, sha256, mediaKey, mediaType, mmsType, allowNoHash)` (verifies the ciphertext hash when `encSHA256` is set — the cause of B1's error), `DownloadToFile`, `SendMediaRetryReceipt`, `DecryptMediaRetryNotification`, `BuildHistorySyncRequest`.

### 3.3 Inventory (read-only, 2026-09-19)

| Check | Result |
|---|---|
| `PRAGMA quick_check` on `messages.db` | `ok` |
| Total messages | 475,561 |
| Audio rows | 9,847 (9,845 with full decryption material) |
| `whatsmeow_lid_map` rows | 19,241 |

| chat_jid | audio | from me | first | last |
|---|---|---|---|---|
| `972523996302@s.whatsapp.net` | 2,107 | 1,179 | 2025-04-19 | 2026-07-19 |
| `18674685612157@lid` (same contact) | 1,955 | 1,001 | 2025-11-13 | 2026-09-18 |
| `132211995082789@lid` (= `972547596761`) | 690 | 425 | 2025-12-11 | 2026-09-17 |
| `120363239970576980@g.us` | 610 | 86 | 2025-04-20 | 2026-09-17 |
| `972546740356-1603022062@g.us` | 464 | 274 | 2025-04-20 | 2026-09-17 |
| `972547596761@s.whatsapp.net` | 462 | 260 | 2025-04-22 | 2026-07-19 |
| `972525721988@s.whatsapp.net` / `119361486479588@lid` | 246 + 78 | | 2025-04-18 | 2026-09-14 |

---

## 4. Data-safety rules (apply to every step)

> [!CAUTION]
> `store/whatsapp.db` (165 MB) holds the linked-device session; losing or corrupting it means re-pairing by QR. `store/messages.db` (232 MB) holds all message history, nicknames, polls and webhook config. Both are owned by `root`; host commands need `sudo`. Every PR states which rules below it touches and how it was verified.

### 4.1 Snapshots
- Before any migration, live test or new binary: `docker compose stop` (never `docker kill`), then `.backup` both DBs to `store/backups/` and run `PRAGMA integrity_check` on the copies (must print `ok`). Tag the previous bridge image (`whatsapp-bridge:pre-<phase>`) for rollback.
- A fresh backup before **each** phase that touches `store.go`. `store/backups/` is git-ignored.

### 4.2 Schema changes (from Phase 1 on; Milestone 1 makes none)
- Additive only: `ALTER TABLE messages ADD COLUMN …` with `NULL` or constant defaults. Never drop, rename, retype, or change constraints/indexes.
- Versioned in `schema_migrations (version INTEGER PRIMARY KEY, applied_at TIMESTAMP)`, one transaction per version, each `ALTER` skipped if `PRAGMA table_info` shows the column. The bridge exits non-zero if a migration fails. Migrations run before the HTTP listener.
- No `SELECT *` anywhere (keeps old binaries working on a migrated DB).

### 4.3 Existing rows are never rewritten
- `StoreMessage` still uses `INSERT OR REPLACE INTO messages` (= DELETE + INSERT; a history-sync replay rewrites the row). Before any download bookkeeping moves into `messages` (Phase 1), it must become:
  ```sql
  INSERT INTO messages (...) VALUES (...)
  ON CONFLICT(id, chat_jid) DO UPDATE SET
    direct_path = COALESCE(messages.direct_path, excluded.direct_path),
    mimetype    = COALESCE(messages.mimetype,    excluded.mimetype),
    seconds     = COALESCE(messages.seconds,     excluded.seconds),
    is_ptt      = COALESCE(messages.is_ptt,      excluded.is_ptt)
  -- content, timestamp, sender, filename, url, media_key, file_*, local_path, downloaded_at: never touched
  ```
- Only other writers to `messages`: `SetMediaLocalPath`, `SetMediaDirectPath` — single-column `UPDATE … WHERE id = ? AND chat_jid = ?`, check `RowsAffected() == 1`. No bulk `UPDATE`/`DELETE` on `messages`/`chats`.
- `INSERT OR REPLACE INTO chats` stays (regenerable data).

### 4.4 Session store and concurrency
- No custom SQL or migrations on `whatsapp.db`; only whatsmeow's `sqlstore`. Its schema upgrades are forward-only, so the pre-upgrade backup + old image are the only way back.
- Read-only access elsewhere: Python ✅ uses `_ro()` (`mode=ro`, `timeout=5`) for all reads and `_rw_messages()` only for nickname writes. Bridge LID lookup ✅ opens `whatsapp.db` with `mode=ro`.
- WAL is **not** enabled. Enabling it is optional, its own commit with its own backup. `*.db-wal` / `*.db-shm` ✅ git-ignored.
- Never two bridges on one `store/` (`docker compose ps` before any `go run`).

### 4.5 Tests and runtime
- Unit tests use `t.TempDir()` only. Integration runs use a `store-test/` copy via `STORE_DIR` ✅ (with the live bridge stopped — the copy shares the session).
- Media goes only under `store/media/` (and `store/downloads/` for MCP copies). `store/whatsapp_store.db` (0 bytes, leftover) is left alone.
- ✅ `stop_grace_period: 30s` on the bridge in `docker-compose.yaml` (shipped with Phase 4); shutdown drains the auto-download pool, then whatsmeow disconnects, then the DBs close.
- Never download or retry synchronously inside the whatsmeow event handler.
- Security: `/api/download`, `/api/media/`, batch return private audio. Keep the bridge off the public internet or add token auth before exposing it (port 8080 is currently published on `0.0.0.0`).

### 4.6 Checklist per deploy that touches `store.go`

| Step | Command / check | Pass |
|---|---|---|
| 1 | `docker compose stop` | no `messages.db-journal` left |
| 2 | backups + `integrity_check` | both `ok` |
| 3 | baseline: `SELECT COUNT(*), MIN(timestamp), MAX(timestamp) FROM messages; SELECT COUNT(*) FROM chats; SELECT COUNT(*) FROM webhook_configs; SELECT COUNT(*) FROM contact_nicknames;` | saved in PR |
| 4 | rehearse on `store-test` | `migration vN applied`, exit 0 |
| 5 | on the copy: `PRAGMA quick_check` + step-3 queries | `ok`, identical |
| 6 | spot-check 5 old rows before/after | byte-identical |
| 7 | `docker compose up -d --build --force-recreate` — **always `--force-recreate`**, never plain `--build`. This has now bitten twice (§2.10, §2.15): a stopped or unchanged container is *started*, not replaced, so the freshly built image never runs | healthy, **and the log prints the expected schema version** — that is the proof, not "Built" or "Up/healthy" |
| 8 | steps 5–6 on live DB (read-only) | same |
| 8b | `./scripts/smoke.sh` (set `EXPECT_SCHEMA` to the version this deploy lands on) | `0 failed`; this is what proves the new binary and the new UI are the ones actually running |
| 9 | once: rollback drill on the copy with the old image | old binary serves `/api/webhooks` |

### 4.7 Code-review blockers
- Any non-`SELECT` against `messages`/`chats` in download code, or any `CREATE`/`ALTER` in `messages.db`/`whatsapp.db` (Milestone 1).
- `SELECT *`; a test opening the real `store/*.db`; downloads inside the event handler; a file path built from unsanitised `chat_jid`/`message_id`; a served path outside `store/media/`.

---

## 5. Milestone 1 — download all past voice messages from one chat

**Scope:** given a contact or group, download every stored audio message from its history, with **zero writes to `messages.db` / `whatsapp.db`**. Bookkeeping lives in a separate `store/media.db`. No dependency upgrades.

### 5.1 Storage design

| Store | Access | Guarantee |
|---|---|---|
| `messages.db` — `messages`, `chats` | `SELECT` only from download code | no DDL/UPDATE/DELETE; before/after diff empty |
| `whatsapp.db` | `mode=ro` query of `whatsmeow_lid_map` | whatsmeow schema unchanged |
| `media.db` (new file) | `media_downloads(message_id, chat_jid, local_path, sha256, direct_path, downloaded_at, status, error)`, `download_jobs(id, params, status, total, done, failed, skipped, current_message_id, started_at, finished_at)` | deleting it loses only the cache index |
| `store/media/<key>/` (new) | `<YYYY-MM-DD_HHMMSS>_<me|them>_<message_id>.ogg`, `index.json` | never beside the `.db` files |

### 5.2 Implementation status

| Component | File | Status |
|---|---|---|
| `STORE_DIR`, `DOWNLOAD_PACE_MS`, `MEDIA_RETRY_TIMEOUT_S`, `PUBLIC_BASE_URL`, `API_PORT` env | `internal/config/config.go` | ✅ (B5 fixed) |
| DSNs from `STORE_DIR` (messages + whatsapp DB) | `internal/database/store.go`, `internal/whatsapp/client.go` | ✅ |
| `media.db` store + job CRUD | `internal/database/media_store.go` (+ test) | ✅ status constants, `MarkInterruptedJobs`, `CancelJob` (B3, B4) |
| Read-only `GetMediaInfo`, `ListAudioMessages` (explicit columns) | `internal/database/messages.go` | ✅ date filter normalised (B8); read-only guard tests |
| `ExtractDirectPathFromURL` | `internal/whatsapp/media.go` | ✅ fixed (S2) |
| `MediaDownloader` | `internal/whatsapp/media.go` | unused (B9) |
| `DownloadMedia` / `DownloadMediaWithOptions`: cache check, atomic `.part`+fsync+rename, SHA-256 check, media-retry fallback, optional `SkipMediaRetry` (§2.9) | `internal/whatsapp/download.go`, `media_decrypt.go` (+ tests) | ✅ B2, B6, B7 fixed; B1 recorded as `rekeyed_on_phone` (no fix known) |
| `events.MediaRetry` → waiter | `internal/whatsapp/client.go`, `main.go` | ✅ (placeholder waiter removed, B7) |
| `ResolveChatJIDs` (phone / `@s.whatsapp.net` / `@lid` / `@g.us`) | `internal/whatsapp/lid.go` | ✅ tested (`lid_test.go`) |
| `POST /api/download`, `POST /api/download/batch` (202 + job), `GET`/`DELETE /api/download/batch/{id}`, `GET /api/media/{chat}/{file}` | `internal/api/handlers.go`, `server.go` | ✅ S1, B4 fixed; batch returns `folder` |
| Python: `_ro()` everywhere, `download_media` path mapping, `start_chat_audio_download`, `get_download_job` | `whatsapp-mcp-server/whatsapp.py` | ✅ |
| MCP tools `batch_download_chat_audio`, `get_audio_download_progress` | `whatsapp-mcp-server/gradio-main.py` | 🟡 present, but behind the bridge: the tool exposes only `chat`, `limit`, `skip_cached`, `retry_failed` — not `media_type`/`voice`, `after`, `before` or `skip_media_retry` |
| Helper script (polls every 5 s; `after`/`before` args, `SKIP_MEDIA_RETRY` env) | `whatsapp-bridge/tests/download-chat-audio.sh` | ✅ uses the bridge's `folder` (B6) |
| Standalone decrypter (not in original plan; reads DBs `mode=ro`, fetches full CDN URL, decrypts locally; no media retry) | `scripts/download_chat_audio.py` | ✅ produced the 11 files in `store/media/972527872233/` |
| `.gitignore`: `*.db-wal`, `*.db-shm`, `store-test/`, `store/backups/` | `.gitignore` | ✅ |

### 5.3 Download path (per message)

```mermaid
flowchart TD
    A[POST /api/download<br/>message_id + chat_jid] --> B{file on disk or<br/>media.db ok + file?}
    B -- yes --> Z[return cached]
    B -- no --> C[SELECT url, media_key, hashes FROM messages]
    C --> D[DownloadMediaWithPath<br/>direct path incl. query]
    D -- ok --> W[.part → fsync → rename<br/>record ok in media.db]
    D -- 403/404/410 --> R[SendMediaRetryReceipt<br/>wait ≤ MEDIA_RETRY_TIMEOUT_S]
    R -- SUCCESS + new path --> D2[download new path;<br/>on hash mismatch verify MAC<br/>with original key] --> W
    D2 -- MAC fails: re-keyed, B1 --> E
    R -- not on phone / timeout --> E[record status in media.db<br/>rekeyed_on_phone / expired_not_on_phone /<br/>retry_timeout / failed]
    W --> Z
```

Rules: rebuild `MessageInfo` from the row (`from_me` → own JID as sender). One retry round-trip per attempt, never a loop, no placeholder-resend request. Permanent failures (`expired_not_on_phone`, `rekeyed_on_phone`, `incomplete_metadata`) are skipped on re-runs unless `retry_failed=true`; `failed` and `retry_timeout` are retried (a re-run with `skip_media_retry=true` skips `failed` as well). A message with no record for this chat inherits the record of the same message ID under the chat's other JID (§2.9).

### 5.4 Batch job

```json
{ "chat": "972527872233", "media_type": "audio", "after": null, "before": null, "limit": 5000,
  "skip_cached": true, "retry_failed": false, "skip_media_retry": false }
```
- `chat` is a phone or any JID; expanded to phone + LID JIDs and echoed as `resolved_jids`.
- `media_type` is `audio` (default) or `voice` (audio with `is_ptt`, so schema-v1 rows only); anything else → 400.
- Background job (`202 {job_id, status_url}`), sequential, one worker, `DOWNLOAD_PACE_MS` between items. One job at a time (409). Cancel with `DELETE`.
- `GET /api/download/batch/{job_id}` → `{status, total, done, failed, skipped, current_message_id, …}`. The start response also returns `resolved_jids` and `folder`. At the end, `store/media/<key>/index.json` is rebuilt with every attempted item of the chat (cumulative across jobs). (Per-item `items` in the GET response: not implemented.)
- Resumable with `skip_cached=true`.
- `skip_media_retry=true` (used for the full run, §2.9): an expired CDN link is recorded as `failed` straight away, with no `SendMediaRetryReceipt` to any phone; the per-item timeout drops to 15 s. `failed` is not permanent, so those items are retried by a later run that leaves the flag off. `tests/download-chat-audio.sh` defaults it to `true`; set `SKIP_MEDIA_RETRY=false` for a run that should ask the phones. The script also takes `after`/`before` as `$3`/`$4` (or env vars).

### 5.5 Execution

**Before (live bridge stopped):**
```bash
cd /home/ubuntu/docker/whatsapp-mcp
docker compose stop
TS=$(date +%Y%m%d_%H%M%S)
sudo sqlite3 store/messages.db ".backup store/backups/messages_${TS}.db"
sudo sqlite3 store/whatsapp.db ".backup store/backups/whatsapp_${TS}.db"
sudo sqlite3 store/backups/messages_${TS}.db "PRAGMA integrity_check;"   # ok
sudo sqlite3 store/backups/whatsapp_${TS}.db "PRAGMA integrity_check;"   # ok
docker tag $(docker compose images -q whatsapp-bridge) whatsapp-bridge:pre-m1
sudo sqlite3 "file:store/messages.db?mode=ro" \
  "SELECT id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url,
          hex(media_key), hex(file_sha256), hex(file_enc_sha256), file_length
   FROM messages WHERE chat_jid IN ('972523996302@s.whatsapp.net','18674685612157@lid')
   ORDER BY timestamp, id;" > /tmp/m1_before.txt
sudo sqlite3 "file:store/messages.db?mode=ro" "SELECT COUNT(*) FROM messages;"
```

**Rehearsal on a copy (once):**
```bash
sudo mkdir -p store-test && sudo cp store/messages.db store/whatsapp.db store-test/
STORE_DIR=store-test go run ./whatsapp-bridge            # live bridge STOPPED
./whatsapp-bridge/tests/download-chat-audio.sh 972523996302 20
```
Pass: files play (`ffprobe` — not installed on the host; install or use the MCP container), `store-test/media.db` has 20 rows, the `messages` dump from `store-test` equals `/tmp/m1_before.txt`.

**Live run:** `docker compose up -d --build` then `./whatsapp-bridge/tests/download-chat-audio.sh 972523996302`.

**After — proof nothing changed:**
```bash
sudo sqlite3 "file:store/messages.db?mode=ro" "<same SELECT>" > /tmp/m1_after.txt
diff /tmp/m1_before.txt /tmp/m1_after.txt          # empty, or only new rows at the end
sudo sqlite3 "file:store/messages.db?mode=ro" "PRAGMA quick_check; SELECT COUNT(*) FROM messages;"
sudo sqlite3 "file:store/messages.db?mode=ro" "SELECT name FROM sqlite_master WHERE type='table';"
sudo sqlite3 "file:store/media.db?mode=ro" "SELECT status, COUNT(*) FROM media_downloads GROUP BY status;"
```

**Rollback:** stop, copy the backups back, run `whatsapp-bridge:pre-m1`. Milestone 1 never writes to the two DBs, so this is belt-and-braces.

**Expected for `972523996302` + LID:** 4,062 audio rows. Media from the last ~11–19 days downloads directly (27 already `ok`). Everything older gets one retry, which is expected to end `rekeyed_on_phone` (B1). With the 30 s timeout rarely reached (answers take 1–2 s) plus the 250 ms pace, a full run takes roughly 1–3 hours. A re-run skips all permanent failures.

### 5.6 Definition of done

- [x] B1 documented as unrecoverable (§2.5) and recorded as `rekeyed_on_phone`; B2–B5 fixed
- [x] B6 fixed (and B8)
- [x] Before/after `diff` on the chat's rows is empty (new live rows excepted) — verified byte-identical (20,096 rows, diff empty)
- [x] `quick_check` = `ok`, table list unchanged, `whatsapp.db` untouched except whatsmeow's own writes
- [x] Every item ends with an explicit status (`ok`: 106, `rekeyed_on_phone`: 339, `expired_not_on_phone`: 1, `failed`: 3616 across all 4,062 messages in `index.json` and `media.db`)
- [x] Re-running the job downloads nothing new (verified on full 4,062-item batch: `skipped: 4062`, `done: 0`, `failed: 0`)
- [x] Tests: direct-path extraction (real URL shape), path sanitiser, media path guard
- [x] Tests: LID expansion, cache hit, retry waiter, "no writes to `messages`" (runtime `query_only` + source guard)

---

## 6. Later phases

### Phase 0 — Dependencies ✅ (deployed 2026-09-20, §2.10)

**Python pins ✅ (2026-09-20).** `whatsapp-mcp-server/requirements.txt` (the file the image actually installs from — `dockerfile` runs `pip install -r requirements.txt`) and `pyproject.toml` now both carry:
```text
httpx>=0.28.1
mcp[cli]>=1.28.1,<2
requests>=2.34.2
starlette>=0.37.2,<1.0
uvicorn>=0.51.0
gradio==5.34.2
gradio_client==1.10.3
```
The bound was load-bearing: **mcp 2.2.0 is on PyPI today**, `gradio-main.py` imports `mcp.server.fastmcp.FastMCP`, so an uncached rebuild would have broken the MCP server. Gradio 6.28.0 is out too; the existing `==5.34.2` already excluded it (6.x breaks `mcp_server=True`).

Verified by `pip install --dry-run` in a throwaway `python:3.13` container: resolves to `mcp 1.30.0`, `starlette 0.52.1`, `uvicorn 0.53.0`, `requests 2.34.2`, `gradio 5.34.2`, `httpx 0.28.1` — **identical to what the running container has**, so a rebuild reproduces the current environment. No rebuild was done; the running container is untouched.

`whatsapp-mcp-server/uv.lock` is stale (pins `mcp 1.6.0`) and is not used by the image build. Regenerate or delete it when local `uv` development matters.

**Go / whatsmeow upgrade ✅ deployed** (first change that touches `whatsapp.db`'s schema — its own backup and image tag):

| Package | Current | Target |
|---|---|---|
| Go toolchain (`go.mod`, `Dockerfile.bridge` `golang:1.26-bookworm`) | 1.25.0 | 1.26.0 |
| `go.mau.fi/whatsmeow` | `20260718134955` | `20260917111002-2e338d0ee73d` |
| `go.mau.fi/util` | 0.9.12 | 0.10.1 |
| `github.com/mattn/go-sqlite3` | 1.14.48 | 1.14.52 |
| `google.golang.org/protobuf` | 1.36.11 | 1.36.12 |
| `golang.org/x/*` | 0.54/0.57 | 0.57/0.59 |
| FontAwesome CDN (webhook UI) | 6.0.0 | 6.7.2 ✅ (`whatsapp-webhook-ui/index.html`) |

Done 2026-09-20: one `go get go.mau.fi/whatsmeow@v0.0.0-20260917111002-2e338d0ee73d` pulled every target version in the table above. The toolchain auto-switched to go1.26.8 (`GOTOOLCHAIN=auto`); `Dockerfile.bridge` now builds `FROM golang:1.26-bookworm` and the image builds clean (tagged `whatsapp-bridge:phase0`). **No source change was needed** — `go vet` clean, `go build ./...` clean, all 31 tests pass. It won't fix B1: the newer release defines the same `MediaRetryNotification` fields (§2.5).

**What the deploy does to `whatsapp.db`** (measured, not assumed): the live DB is at `whatsmeow_version = (version 14, compat 8)`. The new module's upgrade table has 15 entries, so the first start applies exactly **one** migration:
```sql
-- v15 (compatible with v8+): Add companion meta nonce column to device table
ALTER TABLE whatsmeow_device ADD COLUMN companion_meta_nonce TEXT NOT NULL DEFAULT '';
```
Additive, with a constant default, and compat stays 8.

> [!NOTE]
> **Rollback survives this migration.** `dbutil.Database.Upgrade` refuses a DB only when `compat > len(UpgradeTable)`. After the deploy the DB is `(15, 8)`; the old binary knows 14 upgrades and `8 ≤ 14`, so it still opens it, and it ignores the extra column (`NOT NULL DEFAULT ''`, so its inserts still work). The pre-upgrade backup plus `whatsapp-bridge:pre-m1` remain the belt-and-braces route, but this step is not the one-way door §4.4 describes.

**Deploy checklist when it goes out** (§4.6): `docker compose stop` → back up `messages.db`, `whatsapp.db`, `media.db` + `integrity_check` → tag the running image `whatsapp-bridge:pre-phase0` → `docker compose up -d --build` → confirm the log shows the v14→v15 upgrade, the bridge reconnects without a QR prompt, `whatsmeow_version` reads `15|8`, and `/api/download` still serves a cached item.

### Phase 1 — Live voice-note tagging (schema v1) ✅ (deployed 2026-09-21, §2.11)

Only needed for live `is_ptt` tagging and auto-download; history download does not need it.

**Decision taken:** `media.db` stays the single source of truth for download state, so v1 adds **four** columns, not six. `local_path` and `downloaded_at` are deliberately *not* mirrored into `messages`: keeping them out is what lets the download code stay `SELECT`-only on `messages.db`, so both guard tests from §2.8 remain valid and there is no second copy of the path to drift. `SetMediaLocalPath` / `SetMediaDirectPath` are therefore not needed; a fresh direct path from a media retry already goes to `media.db` (§2.5).

- ✅ Migration v1 in `internal/database/migrations.go`: `direct_path TEXT`, `mimetype TEXT`, `seconds INTEGER`, `is_ptt BOOLEAN`. Versioned in `schema_migrations`, one transaction per version, each `ALTER` skipped when `PRAGMA table_info` already shows the column, run from `NewMessageStoreWithDir` (so before the HTTP listener), and a failure now exits non-zero instead of returning 0.
- ✅ `ExtractMediaInfo(msg, msgID)` returns a `types.MediaMetadata` struct instead of seven positional values, carrying `GetPTT()`, `GetSeconds()`, `GetDirectPath()`, `GetMimetype()`. Voice notes are named `voice_<msgID>.ogg` and audio files `audio_<msgID>.ogg`, which also fixes the second-resolution filename collision of §3.2 finding 8. `media_type` stays `audio`.
- ✅ Virtual type `voice` = audio + `is_ptt`: `POST /api/download/batch` now honours its previously dead `media_type` field (`audio` | `voice`, anything else → 400). Only messages received since v1 carry `is_ptt`, so `voice` finds nothing in older history — documented at the call site.
- ✅ `StoreMessage` → `INSERT … ON CONFLICT(id, chat_jid) DO UPDATE SET` filling only v1 columns that are still NULL (§4.3). Absent values are stored as NULL rather than `""`/`0`, so a later replay that does carry them can fill them in.
- ✅ View-once audio is skipped: no URL, key or direct path is kept for it, so nothing here can download it later.
- ✅ `PUBLIC_BASE_URL` now feeds `webhook.NewManager`, replacing the hardcoded `http://localhost:8080/api/download`, and is set in `docker-compose.yaml` to the bridge's container DNS name (what the MCP server and other services on `whatsapp_internal` can actually reach). Webhook payloads gained `is_ptt` and `seconds`; `id` and `chat_jid` were already present (B10).
- ✅ Tests (38 in the suite, up from 31): migration leaves all rows byte-identical and is a no-op on re-open; a replay with different content/filename/url/keys rewrites nothing but backfills NULL v1 columns; a second empty replay does not blank them; new voice notes land with their metadata and text messages leave the audio columns NULL; `ExtractMediaInfo` covers ptt, non-ptt, view-once, missing ID and the non-audio branches. `TestNoForbiddenSQL` walks every `.go` file for `INSERT OR REPLACE INTO messages` and `SELECT *` — verified to fail when a violation is injected.
- `cmd/migrate` applies pending migrations to a `STORE_DIR` copy without connecting to WhatsApp, for rehearsing the next one.

### Phase 2 — MCP server (Python) 🟡

- ✅ `_ro()` / `_rw_messages()`; ✅ relative-path mapping in `download_media`.
- ❌ If the file isn't on the local mount, fall back to `GET {download_url}` into `/app/store/downloads/`.
- ❌ `list_messages(media_type=…)` incl. `voice`; add `is_ptt`, `seconds`, `local_path` to `Message`.
- ❌ `convert_ogg_to_mp3(input, output=None, bitrate="64k")` in `audio.py` (ffmpeg `libmp3lame`).
- ❌ `download_chat_audio_messages(chat_jid | sender_phone_number, voice_only, limit, after, before, convert_to_mp3, output_dir)` on top of the batch endpoint; copies to `store/downloads/<key>/audio/<date>_<sender>_<id>.ogg`.

### Phase 3 — MCP tools and UI 🟡

- ✅ `batch_download_chat_audio`, `get_audio_download_progress`.
- ❌ `download_voice_message(message_id, chat_jid, convert_to_mp3)`; `download_chat_audio(chat_jid, sender_phone_number, voice_only, limit, convert_to_mp3, after, before)`.
- ❌ Gradio tab: chat dropdown, sender filter, "voice only", "MP3", results table with player.

### Phase 4 — Auto-download rules (G2) ✅ (deployed 2026-09-21, §2.14)

**Status:** live since 2026-09-21 11:35 UTC (schema **v2**), extended the same day by schema
**v3** for visible captures and LID-aware triggers (§2.15). Suite at 63 tests (from 40),
`go vet` clean, `-race` clean. First live capture came 3 minutes after the first rule was
created, in 475 ms, with no restart in between (§2.14).

| Piece | File |
|---|---|
| Schema v2 (`auto_download`, `auto_download_voice_only`) + fresh-DB columns | `internal/database/migrations.go`, `store.go` |
| `WebhookConfig.AutoDownload` / `.AutoDownloadVoiceOnly *bool` / `.VoiceOnly()` | `internal/types/types.go` |
| Explicit columns in all four CRUD paths | `internal/database/webhooks.go` |
| URL optional with auto-download; `TestWebhook` refuses a URL-less listener | `internal/webhook/validation.go` |
| Queue + worker pool, non-blocking enqueue, dedupe, timeout | `internal/autodownload/manager.go` |
| The enqueue gate and the delivery skip | `internal/webhook/manager.go` |
| `voiceDownloader` adapter, pool lifecycle, drain before disconnect | `main.go` |
| `AUTO_DOWNLOAD`, `_WORKERS`, `_QUEUE_SIZE`, `_TIMEOUT_S` | `internal/config/config.go` |
| Two checkboxes, URL hint, card badge | `whatsapp-webhook-ui/index.html`, `script.js`, `styles.css` |
| `stop_grace_period: 30s`, `AUTO_DOWNLOAD=on` | `docker-compose.yaml` |

**A listener is an ordinary webhook entry with a new checkbox ticked.** Decided 2026-09-21: auto-download is not a second rule engine with its own tables, endpoints and UI page. It is an *action* on the trigger rules that already exist. You define who gets captured exactly the way you define an event today — the same form in `whatsapp-webhook-ui`, the same `POST /api/webhooks`, the same `webhook_triggers` rows, matched by the same `Manager.matchesTrigger`. The webhook part stays optional: a config with `auto_download` on and an empty URL is a download-only listener.

The env-var slice (`AUTO_DOWNLOAD_VOICE=none|all|rules` + `AUTO_DOWNLOAD_ALLOWLIST`, a "4a" shipped before the rules) is **dropped**. §2.13 left the recoverable backlog at 100% captured, so there is no gap that justifies a mechanism whose whole implementation — parsing, tests, compose entries, docs — gets deleted a week later. Rules live in `messages.db`, so adding a contact is a click that takes effect on the next message (all four CRUD paths already call `LoadWebhookConfigs`), not a compose edit and a container restart.

**Schema v2** (`messages.db`, additive per §4.2) — two columns on `webhook_configs`:

```sql
ALTER TABLE webhook_configs ADD COLUMN auto_download BOOLEAN DEFAULT 0;
ALTER TABLE webhook_configs ADD COLUMN auto_download_voice_only BOOLEAN DEFAULT 1;
```

The same two columns go into `createTables` for a fresh database; `createTables` runs before `runMigrations`, and `addColumns` skips what `PRAGMA table_info` already shows, so v2 is then a no-op. No `auto_download_rules` and no `auto_download_triggers`. There is also no `auto_download_logs` table: per-message outcomes already have an honest home in `media.db.media_downloads` (`ok` / `failed` / `rekeyed_on_phone` / …), and schema **v3** (§2.15) puts the *listener-facing* record in `webhook_logs` alongside that listener's deliveries, so the UI's existing Logs button shows captures with no second table, endpoint or view.

**What fires the download.** `webhook.Manager.ProcessMessage` already runs after `StoreMessage` (`handlers.go:219`) and already computes the matched configs. The trigger picks the *who*; the rule's own flags pick the *what*:

```
enqueue ⟺ config.Enabled ∧ config.AutoDownload
        ∧ media.MediaType == "audio"
        ∧ (¬config.AutoDownloadVoiceOnly ∨ media.IsPTT)
        ∧ (media.URL ≠ "" ∨ media.DirectPath ≠ "")   // view-once carries neither (§2.11)
```

So a `sender` trigger that also matches text messages is fine — text never reaches the queue. A `media_type=audio` trigger is allowed but not required. Both directions are captured; restrict with a `sender` trigger if you only want theirs. Two matching rules enqueue **one** job (in-flight set keyed by `message_id`+`chat_jid`), and `DownloadMedia`'s cache check makes a duplicate harmless anyway.

**`internal/autodownload/manager.go`** — buffered channel + worker pool, the §4.5 invariant kept: enqueue is a `select`/`default` non-blocking send, so the whatsmeow event handler is never delayed; a full queue logs a warning and drops. Workers call the downloader with `SkipMediaRetry: true` — live media is seconds old, the direct path always works, and this guarantees no retry receipt is ever sent to a phone (§2.13 held that at 0). The manager takes a narrow interface, not the client:

```go
type Downloader interface {
    DownloadVoice(ctx context.Context, messageID, chatJID string) (*Result, error)
}
```

`main.go` passes an adapter closing over `client`, `mediaStore`, `messageStore` and `storeDir`; tests pass a fake. `webhookManager.SetAutoDownloader(adm)` keeps `NewManager`'s signature (and its call sites) unchanged.

`Stop()` runs before `client.Disconnect()`, because the workers download over that same connection. It stops workers taking new jobs, then gives whatever is already downloading `ShutdownGrace` (15 s, deliberately under the 30 s `stop_grace_period`) to finish — a voice note in flight is usually seconds from being safe on disk — and only then cancels. Jobs still queued are dropped: those messages are in `messages.db` and can be fetched later, whereas a half-written file cannot be.

**Config** (ops knobs only — never *who*): `AUTO_DOWNLOAD=on|off` (default `on`, a kill switch that does not touch any rule), `AUTO_DOWNLOAD_WORKERS=2`, `AUTO_DOWNLOAD_QUEUE_SIZE=100`, `AUTO_DOWNLOAD_TIMEOUT_S=60`. Plus `stop_grace_period: 30s` on the bridge, shipped with this phase (§4.5).

**API:** no new endpoints. `POST`/`PUT /api/webhooks` carry `auto_download` and `auto_download_voice_only`. Two consequences to implement:
- `ValidateWebhookConfig`: `webhook_url` is required **unless** `auto_download` is true. `TestWebhook` on a URL-less config → 400.
- `ProcessMessage`: skip delivery when `WebhookURL == ""` (the config is a download-only listener).
- `auto_download_voice_only` is `*bool` in `types.WebhookConfig`: absent in JSON → `true`, matching the column default. A plain `bool` would silently mean "all audio" for every client that omits the field.
- `StoreWebhookConfig` / `GetWebhookConfig` / `GetAllWebhookConfigs` / `UpdateWebhookConfig` list the new columns explicitly (`SELECT *` stays banned, `TestNoForbiddenSQL`).

**UI** (`whatsapp-webhook-ui/`): two checkboxes in the existing form — "Auto-download matched media" and "Voice notes only" (shown when the first is ticked) — the URL field's `required` moved from HTML to JS so it can be waived, and an "Auto-download" badge on the card. No new tab, no second form, no second logs modal.

**Follow-up, not in scope:** deliver the webhook *after* the file lands so `media_download_url` points at the finished `/api/media/<chat>/<file>`. Not needed for correctness — `media_download_url` is the `POST /api/download` endpoint, which returns a cache hit once the worker is done.

**Deploy:** touches `store.go` + a migration → the full §4.6 checklist, rehearsed with `cmd/migrate` on `store-test`, image tagged `whatsapp-bridge:pre-phase4`.

**Tests:** migration v2 (columns added, existing `webhook_configs` rows byte-identical, re-open is a no-op); config round-trip incl. `voice_only` nil → true; validation (URL optional with `auto_download`, still required without, test-webhook refused without a URL); the enqueue gate (ptt ✅, non-ptt audio ✅/❌ per flag, image ❌, text from a matching sender ❌, view-once ❌, disabled config ❌, matched-but-`auto_download`-off ❌); full queue does not block; two matching rules → one job; worker calls the downloader with `SkipMediaRetry` and honours the timeout. Existing guard tests (`TestNoForbiddenSQL`, download-only-reads-`messages`) must stay green.

**Verify live:** tick auto-download on a rule for one test sender → their next voice note appears in `store/media/<chat>/` within seconds with an `ok` row in `media.db`; a voice note from any other chat produces nothing; `messages.db` row count and `quick_check` unchanged.

### Phase 5 — Batch by sender, missing history 🟡

- ✅ Batch by chat (Milestone 1).
- ❌ Filter by `sender` across groups and by `voice` once `is_ptt` exists.
- ❌ `POST /api/history/request {chat_jid, count}` using `BuildHistorySyncRequest` when the DB lacks older messages; MCP tool `request_chat_history`. Old history media is expired and media retry returns re-keyed copies (B1), so this only helps if the resent messages carry the phone's current key. That is option 2 in §2.5; test on one message first.

### Phase 6 — Transcription (later) ❌

`transcribe_voice_message(message_id, chat_jid, language)` MCP tool with `faster-whisper` or an API backend; cache in a `transcripts` table; add `transcript` to `list_messages`; optional `transcribe: true` on auto-download rules.

### Phase 7 — Listener ergonomics, retention and a downloads view 🟡 (7.1–7.3 ✅, 7.4–7.5 ❌)

Four items from §2.16, in the order they should land. The sequence is deliberate: the chat
picker unblocks two UIs, the health endpoint and smoke check make every later deploy
verifiable, and **retention goes last** because it is the only step that deletes files and it
should land on a codebase that already has a test harness and a post-deploy check.

> [!WARNING]
> **Accepted risk, decided 2026-09-21.** Both new endpoints widen what the unauthenticated
> port 8080 exposes: `/api/chats` returns the whole contact and group list, `/api/downloads`
> indexes every saved voice note. Token auth and binding to localhost were both declined for
> now, so **O2 gets worse with this phase, not better**. Anything that can reach the host can
> enumerate the address book and the media index. This is a conscious trade, recorded so it
> is not rediscovered later.

#### 7.1 Chat picker ✅ (deployed 2026-09-21 12:17 UTC)

A rule still means typing an identifier. B11 made phone numbers work; it did not make them
discoverable (§2.16 G-c).

- **`GET /api/chats`** — `q` (case-insensitive substring of name or JID), `type`
  (`all|contact|group`), `limit` (default 50, hard cap 200). Returns
  `{jid, name, last_message_time, is_group}` ordered by `last_message_time DESC`, read-only
  from `chats` (3,936 rows) with explicit columns — `SELECT *` stays banned.
- **UI**: for `chat_jid` and `sender` triggers the value field becomes an input backed by a
  `<datalist>` repopulated on a 200 ms debounce from `/api/chats?q=`. Picking an option sets
  the **JID** and shows the chat name beneath it. Free text is still accepted, which matters
  because a phone number is now a valid value.
- `sender` triggers offer 1:1 chats only. The picker lists chats, not group participants, so
  "this person inside that group" still means typing their number — document it in the hint
  rather than pretending otherwise.
- Known rough edge: `chats.name` falls back to the JID user part when the contact never
  resolved, so some rows will show a bare LID (the live `62156783493249@lid` reads
  `14018823594220`). Display it as-is; inventing a nicer label would be guessing.

#### 7.2 Health endpoint and deploy smoke check ✅ (deployed 2026-09-21 12:3x UTC)

The two failures that cost time today were deployment-shaped, not logic-shaped: a stale
container served old assets while everything reported "Built" and "Up/healthy" (§2.10,
§2.15). A check that reads the *running* system is the fix.

- **`GET /api/health`** → `{status, schema_version, auto_download: {enabled, workers, queue_depth}}`.
  This is what makes a deploy verifiable without grepping container logs.
- **`scripts/smoke.sh`** — plain `curl`, no toolchain, exits non-zero on any failure:
  - UI: `index.html` carries the current markers; the `script.js?v=N` it references serves a
    body containing the expected symbols (catches cache-buster drift *and* a stale container).
  - Bridge: `/api/health` reports the expected schema version; `/api/webhooks` returns 200 and
    every entry carries `auto_download` and `auto_download_voice_only`; `/api/chats?limit=1`
    and `/api/downloads?limit=1` answer 200.
- Add it to §4.6 as the step that closes a deploy. ✅ step 8b.

**Shipped as described**, and verified to fail correctly: pointed at a bridge without the
endpoint it reported `/api/health returned 404`; with `EXPECT_SCHEMA=4` against a v3 bridge
it reported a probable stale container; against a bare nginx it reported the UI markers
missing. 16 checks pass against the live system.

Two bugs found while writing it, both worth remembering because each made the script lie in
the *reassuring* direction:
- `STATUS` was set inside a command substitution, so the caller never saw it — a subshell
  assignment cannot reach its parent.
- `printf '%s' "$body" | grep -q X` under `set -o pipefail` reports **failure on an early
  match**: `grep -q` exits at the first hit, `printf` takes SIGPIPE, and `pipefail` adopts
  printf's status. A string near the top of the file looked absent while one further down
  looked present. Content checks now use shell substring matching (`[[ $body == *X* ]]`),
  no pipeline.

#### 7.3 Downloads endpoint and view ✅ (deployed 2026-09-21)

- **`GET /api/downloads`** — `chat` (expanded through `ResolveChatJIDs`), `after` / `before`
  (normalised by `NormalizeTimeFilter`, the B8 fix), `status` (default `ok`), `limit`
  (default 50, cap 500), `offset`. Returns `{items, total}`.
- Per item: `message_id`, `chat_jid`, `chat_name`, `sender`, `is_from_me`, `timestamp`,
  `seconds`, `is_ptt`, `size_bytes`, `status`, `download_url`.
- `media.db` and `messages.db` are separate files, so this is **two queries joined in Go**,
  not an `ATTACH`: the records come from `media_downloads`, the metadata from `messages` and
  `chats` by `(message_id, chat_jid)`. Keeping them unattached is what keeps the read-only
  guard on `messages.db` simple and the §2.8 guard tests valid.
- **`GET /api/media/<path>?download=1`** adds `Content-Disposition: attachment`. Without the
  flag it streams inline exactly as now, so the `<audio>` player is unaffected.
- **UI**: a `Webhooks | Downloads` switcher at the top of the page. The Downloads view is a
  filter bar (the 7.1 picker, reused, plus a date range) over a table of one row per file:
  duration, chat, timestamp, direction, size, an inline `<audio controls preload="none">`
  and a download link. Paged.

**Shipped as described.** Reconciled against the live archive: `total` for `status=ok` is 270,
equal to `media.db`'s ok count, and the per-status totals sum to `all` (4,230). A phone-number
chat filter reaches the `@lid` chat; a one-day date filter returns only messages *sent* that
day; `/api/media/…` streams with range support and `?download=1` attaches. 8 new Go tests.

Decisions worth keeping:
- **Dates are send times, not download times.** The whole backlog sweep was downloaded on
  one day, so filtering by download time would make every date range return all or nothing.
  The test seeds exactly that shape so the mistake cannot come back unnoticed.
- **The UI ignores `download_url`** and builds URLs from `media_path` plus its own API base.
  `PUBLIC_BASE_URL` is the bridge's container DNS name — right for the MCP server on the
  Docker network, unresolvable from a browser. Both fields are returned.
- **Date filters are sent as local-day instants.** The API compares instants; a bare
  `YYYY-MM-DD` is UTC midnight, which at UTC+3 moves a day's edges by three hours. The UI
  converts the picked day to local midnight and makes "To" inclusive.
- **`?download=1` sets `Content-Disposition`** because the UI is on another port, and
  browsers ignore `<a download>` on cross-origin links.
- **Every value is rendered with `textContent`.** Chat names and senders are set by other
  WhatsApp users; `innerHTML` here would be an injection point.
- **A global `[hidden]{display:none!important}`**, since the pager needs `display:flex` and
  author CSS otherwise defeats the `hidden` attribute.
- **Unpaged scan, paged in Go.** `media.db` has no send time, so filtering needs the join
  first. At ~4,200 records that is one cheap pass; revisit if the archive grows by orders of
  magnitude.

Known limitation: chat names inherit B12, so 100 conversations display under the user's own
LID.

#### 7.4 UI tests

Two layers, because they catch different things.

- **`scripts/smoke.sh`** (7.2) is the deployment layer, and the one that would have caught
  both stale-container incidents.
- **jsdom unit tests** in `whatsapp-webhook-ui/test/`: `package.json` with a single dev
  dependency (`jsdom`) and Node's built-in `node:test`. Run through
  `scripts/test-ui.sh`, which wraps `docker run --rm -v … node:22` — **nothing is installed
  on the host**, which has no Node at all. Commit `package.json` and `package-lock.json`;
  never `node_modules/` (add it to `.gitignore`).
- The tests load the real `index.html` into jsdom, execute `script.js` as the page does and
  stub `fetch`, so **no change to `script.js` is needed** and a field missing from the HTML
  fails the test rather than silently rendering nothing.
- Cases: a capture log renders `CAPTURED` with its path and duration; a delivery log still
  renders `HTTP 200`; ticking auto-download reveals voice-only and waives the URL
  requirement; the submitted payload carries the new fields; a card shows the badge and the
  download-only URL placeholder; the chat picker fills the trigger value.

#### 7.5 Per-listener retention (schema v4) — destructive, lands last

- **Schema v4**: `webhook_configs.retention_days INTEGER` (NULL or 0 = keep forever), plus
  the same column in `createTables`. Additive with a constant default, per §4.2.
- **New `media.db` status `pruned`**, treated as *permanent* alongside `rekeyed_on_phone`:
  `DownloadMediaWithOptions` returns early on it instead of re-fetching, so a later batch run
  cannot silently resurrect what you asked to expire. Overridable by `retry_failed` (batch) or
  an explicit `force` (single) — deliberate re-fetch stays possible, accidental does not.
- **`internal/retention/sweeper.go`** — hourly (`RETENTION_SWEEP_INTERVAL_M`, default 60),
  plus once about a minute after start. Per enabled config with `retention_days > 0`:
  - candidates are that rule's own capture logs (`webhook_logs`, `event_type='auto_download'`,
    `delivered_at NOT NULL`) older than N days;
  - **skip if any other rule also captured it** and that rule's retention is unlimited or not
    yet expired;
  - **skip anything with no capture log at all** — that is a manual `/api/download` or batch
    fetch, which the user asked for by hand and retention must never touch;
  - delete the file, set the `media.db` row to `pruned` (keeping `local_path` for history),
    and write a `webhook_logs` row with `event_type='retention'` so deletions appear in the
    same Logs view as captures — trust in a destructive feature requires a visible record.
- **Guards, because this deletes files.** Every path is resolved with the *same* sanitiser
  `/api/media/` uses (`EvalSymlinks`, must stay inside `<storeDir>/media/`, `.ogg`/`.mp3`
  only); anything else is refused and logged, never deleted. `index.json` is never a
  candidate. A per-sweep cap (500 deletions) so a misconfigured rule cannot empty the archive
  in one pass. `RETENTION_DRY_RUN=true` logs what it would remove and deletes nothing — run
  the first live sweep this way.
- **§4.7 gains a review blocker:** any file deletion that does not go through that sanitiser,
  or that can name a path outside `store/media/`.
- **UI**: a `Keep for [N] days` field beside the auto-download checkboxes, blank = forever.
- **Tests**: only that rule's expired captures are chosen; a file another rule still wants is
  kept; a manual download is never touched; a path outside the media root is refused; dry-run
  deletes nothing; the row becomes `pruned`; a `pruned` record is not re-downloaded without
  an explicit override; the per-sweep cap holds.

#### Deploy

7.1–7.4 touch no schema and deploy normally (`up -d --build --force-recreate`, then
`scripts/smoke.sh`). 7.5 adds migration v4, so it takes the full §4.6 checklist including a
rehearsal on a copy, and its first sweep runs with `RETENTION_DRY_RUN=true`.

---

## 7. Risks

- **DB integrity** — §4. ✅ The replay-overwrite hazard (§4.3) is closed: `StoreMessage` uses `ON CONFLICT … COALESCE` and never rewrites an existing row. Download state was kept out of `messages` entirely (§2.11).
- **Exposure** — port 8080 is public and unauthenticated; S1 showed what that costs. Bind it to localhost or the Docker network, or add token auth.
- **Expired media is the norm** for anything older than ~11–19 days, and media retry doesn't recover it today (B1). Report per item, never abort a batch.
- **MCP SDK v2** — ✅ pinned `<2` (§2.10); mcp 2.2.0 is live on PyPI, so the bound is load-bearing.
- **Rate limits** — one worker, 250 ms pace for history; ≤ 2 workers for auto-download.
- **Disk** — ~120 KB per minute of voice. 281 files / ~30 MB today at ~19 voice notes a day, so growth is negligible for now; add retention before widening capture to images and video, which are orders of magnitude larger. No retention exists yet (§2.16).
- **View-once** audio — skip.
