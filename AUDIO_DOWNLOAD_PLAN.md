# WhatsApp Voice / Audio Download Plan

Single plan for downloading WhatsApp voice notes and audio: on demand, in bulk from chat history, and automatically for chosen contacts and groups. It replaces the earlier `VOICE_DOWNLOAD_MILESTONE_1.md`, which is now **Milestone 1** below.

**Status as of 2026-09-19:** Milestone 1 code is written, deployed and committed on branch `webhook`. Voice notes up to ~11–19 days old download directly. Older media can't be recovered: the phone re-uploads it with a new key it doesn't send back (B1, §2.5). Those items are now recorded as `rekeyed_on_phone` and skipped (§2.6). Phases 0 and 2–6 have not started.

Legend: ✅ done · 🟡 partly done / done with open issues · ❌ not started · 🐞 known bug

---

## 1. Goals

| # | Goal | Delivered by | Status |
|---|---|---|---|
| G1 | Download a single voice message (or any audio) on demand | Milestone 1 (`POST /api/download`) + Phase 3 MCP tool | 🟡 works for media up to ~11–19 days old; older is unrecoverable (B1) |
| G3 | Download past voice messages from one chat or group | Milestone 1 (batch job) → Phase 5 (by sender, history request) | 🟡 endpoint + job built; not yet run at scale |
| G2 | Auto-download voice messages from chosen users / groups, using the webhook trigger rules | Phase 4 | ❌ |

Order: **Milestone 1 → Phase 0 → Phase 1 + Phase 5 → Phases 2/3 → Phase 4 → Phase 6.** Nothing after Milestone 1 starts until its definition of done (§5.6) is met.

---

## 2. Current state and next steps

### 2.1 What was verified on 2026-09-19

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
| Before/after `messages` diff, `store-test` rehearsal | not done |

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
| B9 | low | `MediaDownloader` is unused. Plaintext SHA-256 check duplicates whatsmeow's. `gofmt -l` flags `config.go`, `download.go` (and pre-existing `types.go`, `handlers.go`, two tests). Python `download_media` reads the error from `message`; confirm which key `SendJSONError` writes. | various |
| B10 | low | `PUBLIC_BASE_URL` defaults to `http://localhost:8080`, not set in `docker-compose.yaml`, so `download_url` is wrong from inside the MCP container. `webhook/manager.go:213` still hardcodes `http://localhost:8080/api/download`. | config, compose, webhook |

### 2.4 Next steps (in order)

1. **Decide on re-pairing** (S1 warning). ✅ Work committed on `webhook` (`235988f`; `store/media/` added to `.gitignore`). Exposure check: Docker publishes 8080 on `0.0.0.0` and the host firewall accepts everything, so reachability depends on the Oracle VCN security list (not visible from the VM). The bridge keeps no HTTP access log, so there's no record of whether the file was fetched. ❓ Re-pairing: pending your decision.
2. ✅ **Measure the fresh-media rate after S2.** Run a batch of `limit: 20` on a chat with recent audio. Record how many succeed directly vs need a retry. This sets expectations for the full run.
   - **Result (2026-09-19 14:39 UTC, job `job_20260919_143919_283`):** `18674685612157@lid`, expanded to it plus `972523996302@s.whatsapp.net`. The 20 newest items (15–18 Sep; 8 from me, 12 from the contact): **20/20 `ok` in 9 s, 0 media retries**. 518,863 bytes total, all with an `OggS` header, `index.json` 20× `ok` in `store/media/972523996302/`.
   - Integrity: the chat's 20,095 `messages` rows are byte-identical before/after; `quick_check` = `ok`.
   - Idempotency: re-running the same job gives `skipped: 20`, `done: 0`, no new files.
   - Takeaway: the direct path works for media up to at least 4 days old. How far back links stay valid is still unknown. The next batch should reach further back (e.g. `before: 2026-09-10`, limit 20) to find where direct downloads start failing. That boundary is where B1 matters.
3. ✅ **Diagnose B1**. Result in §2.5: diagnosed, no fix with the current protocol. Original plan kept for reference: fetch the raw blob from the fresh direct path without whatsmeow's hash check, then
   - verify its HMAC and decrypt it with the **original** `media_key` (same code as `scripts/download_chat_audio.py`);
   - if HMAC + plaintext SHA-256 pass → add this as the post-retry fallback (plaintext hash still enforced, so integrity holds);
   - if HMAC fails → the phone re-encrypted with a new key; the only route is the placeholder resend (keep B7's path but fix it), or accept these as unrecoverable.
   - Also try the upgraded whatsmeow (Phase 0) on a copy: its retry/media changes may matter here.
4. ✅ **Fix B2, B3, B5** (and B4, B7, the `index.json` overwrite; see §2.6) (make the job resumable and honest): map `ErrMediaNotAvailableOnPhone` / `NOT_FOUND` to `expired_not_on_phone`; on startup mark leftover `running` jobs `interrupted`; use `MEDIA_RETRY_TIMEOUT_S`.
5. ✅ **Fix B6, B8** (B4, B7 done in step 4). See §2.7: cancel only `running` jobs (404 otherwise); compute the folder key once per job in one function and return it in the batch response; gate placeholder resend behind a flag, don't swallow unmatched/duplicate events; normalise `after`/`before` to the stored format.
6. ✅ **Missing tests**: LID expansion, cache hit, retry waiter (fake `events.MediaRetry`), "no writes to `messages`". See §2.8.
7. **Execute §5.5**: baseline dump → `store-test` rehearsal (limit 20) → live run on `972523996302` → after-run diff. Tick §5.6.
8. Then **Phase 0** (pin `mcp[cli]<2` first — cheapest, prevents a broken rebuild).

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

1. **`/api/download` was missing** after the refactor in `fe89097` (upstream `0487e16` had it). ✅ Restored in Milestone 1. `webhook/manager.go` still advertises `http://localhost:8080/api/download` (B10).
2. **Decryption material is stored** for 9,845 of 9,847 audio rows (`url`, 32-byte `media_key`, `file_sha256`, `file_enc_sha256`, `file_length`). Not stored: `direct_path`, `mimetype`, `seconds`, the **`ptt` flag**. The direct path is recoverable from `url` **including its query string** (S2 — the original upstream extraction was wrong). `ExtractMediaInfo` names files from `time.Now()`, so replays get new names.
3. **No audio filter / batch** in the MCP layer. Batch now exists in the bridge; `list_messages` still has no `media_type` filter.
4. **CDN links expire** (~1–2 weeks in practice; the `oe=` value is not reliable either way). Recovery is whatsmeow's media retry: `SendMediaRetryReceipt` → `events.MediaRetry` with a fresh direct path. Needs the sender's phone online and still holding the file. Currently blocked by B1.
5. **Format:** voice notes are Opus in Ogg, mono, ~16 kbps. The MCP container has ffmpeg.
6. **One contact = two chat JIDs.** 1:1 chats moved to LID addressing; the phone-number chat stops around 2026-07-19 and the `@lid` chat continues. "All voice notes from X" must union both via `whatsmeow_lid_map` (19,241 rows). ✅ `ResolveChatJIDs`.
7. **Old rows can't tell voice notes from audio files** (no `ptt`). Milestone 1 downloads all audio; nearly all 1:1 audio is voice notes anyway.
8. **Stored filenames collide** (second resolution). Downloaded files are named by timestamp + direction + message ID; the `filename` column is never used or changed.
9. **About half of each chat is `from_me`.** Their media retry goes to your own phone.
10. **Webhook trigger model to reuse for G2:** `WebhookTrigger{TriggerType: chat_jid|sender|keyword|media_type|all, TriggerValue, MatchType: exact|contains|regex}`, matched by `Manager.matchesTrigger`.
11. **whatsmeow:** pinned `v0.0.0-20260718134955-fac667d55293` (Go 1.25). Target `v0.0.0-20260917111002-2e338d0ee73d` (Go 1.26) has retry/ack fixes and new protobufs. Relevant APIs: `DownloadMediaWithPath(ctx, directPath, encSHA256, sha256, mediaKey, mediaType, mmsType, allowNoHash)` (verifies the ciphertext hash when `encSHA256` is set — the cause of B1's error), `DownloadToFile`, `SendMediaRetryReceipt`, `DecryptMediaRetryNotification`, `BuildHistorySyncRequest`.

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
- ❌ `stop_grace_period: 30s` on the bridge in `docker-compose.yaml`; shutdown closes DBs after whatsmeow disconnects.
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
| 7 | `docker compose up -d --build` (use `--force-recreate` if the container isn't replaced) | healthy, expected migration version |
| 8 | steps 5–6 on live DB (read-only) | same |
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
| `STORE_DIR`, `DOWNLOAD_PACE_MS`, `MEDIA_RETRY_TIMEOUT_S`, `PUBLIC_BASE_URL`, `API_PORT` env | `internal/config/config.go` | 🟡 retry timeout unused (B5) |
| DSNs from `STORE_DIR` (messages + whatsapp DB) | `internal/database/store.go`, `internal/whatsapp/client.go` | ✅ |
| `media.db` store + job CRUD | `internal/database/media_store.go` (+ test) | 🟡 stuck-job cleanup missing (B3) |
| Read-only `GetMediaInfo`, `ListAudioMessages` (explicit columns) | `internal/database/messages.go` | 🟡 date filter (B8) |
| `ExtractDirectPathFromURL` | `internal/whatsapp/media.go` | ✅ fixed (S2) |
| `MediaDownloader` | `internal/whatsapp/media.go` | unused (B9) |
| `DownloadMedia`: cache check, atomic `.part`+fsync+rename, SHA-256 check, media-retry fallback | `internal/whatsapp/download.go` (+ test) | 🐞 B1, B2, B6, B7 |
| `events.MediaRetry` → waiter | `internal/whatsapp/client.go`, `main.go` | ✅ (placeholder waiter: B7) |
| `ResolveChatJIDs` (phone / `@s.whatsapp.net` / `@lid` / `@g.us`) | `internal/whatsapp/lid.go` | ✅ untested |
| `POST /api/download`, `POST /api/download/batch` (202 + job), `GET`/`DELETE /api/download/batch/{id}`, `GET /api/media/{chat}/{file}` | `internal/api/handlers.go`, `server.go` | 🟡 S1 fixed; B4 |
| Python: `_ro()` everywhere, `download_media` path mapping, `start_chat_audio_download`, `get_download_job` | `whatsapp-mcp-server/whatsapp.py` | ✅ |
| MCP tools `batch_download_chat_audio`, `get_audio_download_progress` | `whatsapp-mcp-server/gradio-main.py` | ✅ |
| Helper script (polls every 5 s) | `whatsapp-bridge/tests/download-chat-audio.sh` | 🟡 index path wrong for groups (B6) |
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
    R -- SUCCESS + new path --> D2[download new path<br/>B1: fails hash check today] --> W
    R -- not on phone / timeout --> E[record reason in media.db]
    W --> Z
```

Rules: rebuild `MessageInfo` from the row (`from_me` → own JID as sender). One retry round-trip per attempt, never a loop. Permanent failures (`expired_not_on_phone`, `incomplete_metadata`) are skipped on re-runs unless `retry_failed=true`.

### 5.4 Batch job

```json
{ "chat": "972527872233", "after": null, "before": null, "limit": 5000,
  "skip_cached": true, "retry_failed": false }
```
- `chat` is a phone or any JID; expanded to phone + LID JIDs and echoed as `resolved_jids`.
- Background job (`202 {job_id, status_url}`), sequential, one worker, `DOWNLOAD_PACE_MS` between items. One job at a time (409). Cancel with `DELETE`.
- `GET /api/download/batch/{job_id}` → `{status, total, done, failed, skipped, current_message_id, …}`. Per-item results go to `store/media/<key>/index.json` at the end. (Per-item `items` in the GET response: not implemented.)
- Resumable with `skip_cached=true`.

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

**Expected for `972523996302` + LID:** 4,062 audio rows. Recent months download directly; older items need one retry each (blocked by B1 today). Plan for a multi-hour first run and one or two re-runs on different days.

### 5.6 Definition of done

- [x] B1 documented as unrecoverable (§2.5) and recorded as `rekeyed_on_phone`; B2–B5 fixed
- [x] B6 fixed (and B8)
- [ ] Before/after `diff` on the chat's rows is empty (new live rows excepted)
- [ ] `quick_check` = `ok`, table list unchanged, `whatsapp.db` untouched except whatsmeow's own writes
- [ ] Every item ends with an explicit status (`ok`, `rekeyed_on_phone`, `expired_not_on_phone`, `incomplete_metadata`; no leftover `failed`/`retry_timeout` after a re-run). *(Was "≥ 95 % ok"; not reachable because of B1: only ~11–19 days of media can be downloaded.)*
- [x] Re-running the job downloads nothing new (verified on a 20-item batch, 2026-09-19)
- [x] Tests: direct-path extraction (real URL shape), path sanitiser, media path guard
- [x] Tests: LID expansion, cache hit, retry waiter, "no writes to `messages`" (runtime `query_only` + source guard)

---

## 6. Later phases

### Phase 0 — Dependencies ❌

**Do first (cheap, prevents breakage):** in `whatsapp-mcp-server/requirements.txt` (currently `mcp[cli]>=1.6.0`, unbounded) and `pyproject.toml`:
```text
httpx>=0.28.1
mcp[cli]>=1.28.1,<2
requests>=2.34.2
starlette>=0.37.2,<1.0
uvicorn>=0.51.0
gradio==5.34.2
gradio_client==1.10.3
```
MCP 2.x is a breaking rewrite (`FastMCP` → `MCPServer`); an uncached rebuild would pull it today. Gradio 6.x breaks `mcp_server=True`.

**Go / whatsmeow upgrade** (first change that touches `whatsapp.db`'s schema — its own backup and image tag; forward-only):

| Package | Current | Target |
|---|---|---|
| Go toolchain (`go.mod`, `Dockerfile.bridge` `golang:1.26-bookworm`) | 1.25.0 | 1.26.0 |
| `go.mau.fi/whatsmeow` | `20260718134955` | `20260917111002-2e338d0ee73d` |
| `go.mau.fi/util` | 0.9.12 | 0.10.1 |
| `github.com/mattn/go-sqlite3` | 1.14.48 | 1.14.52 |
| `google.golang.org/protobuf` | 1.36.11 | 1.36.12 |
| `golang.org/x/*` | 0.54/0.57 | 0.57/0.59 |
| FontAwesome CDN (webhook UI) | 6.0.0 | 6.7.2 |

```bash
cd whatsapp-bridge
go get go.mau.fi/whatsmeow@v0.0.0-20260917111002-2e338d0ee73d
go get github.com/mattn/go-sqlite3@v1.14.52 google.golang.org/protobuf@v1.36.12
go mod tidy && go test ./...
```
Rehearse on `store-test` first; may help with B1.

### Phase 1 — Live voice-note tagging (schema v1) ❌

Only needed for live `is_ptt` tagging and auto-download; history download does not need it.

- Migration v1 in new `internal/database/migrations.go`: `direct_path TEXT`, `mimetype TEXT`, `seconds INTEGER`, `is_ptt BOOLEAN`, `local_path TEXT`, `downloaded_at TIMESTAMP`.
- `ExtractMediaInfo` returns them (`AudioMessage.GetPTT()`, `GetSeconds()`, `GetDirectPath()`, `GetMimetype()`); new rows named `voice_<msgID>.ogg`. Keep `media_type='audio'`; expose virtual type `voice` = audio + `is_ptt`.
- `StoreMessage` → `ON CONFLICT … COALESCE` (§4.3) **before** anything writes `local_path`.
- `SetMediaLocalPath`, `SetMediaDirectPath` (after `SUCCESS` only). Decide whether `media.db` stays the source of truth for downloads or is mirrored into `messages`.
- Skip view-once audio.
- `PUBLIC_BASE_URL` in `webhook/manager.go` and `docker-compose.yaml`; add `message_id`, `chat_jid`, `is_ptt`, `seconds` to webhook payloads (B10).
- Tests: migration on an old-schema temp DB keeps rows byte-identical and is a no-op on re-run; replay keeps `local_path`/`content`/`filename`/`url`; setters affect exactly one row. `grep -rn "INSERT OR REPLACE INTO messages\|SELECT \*"` returns nothing.

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

### Phase 4 — Auto-download rules (G2) ❌

- Move `matchesTrigger`/`matchesString` to `internal/matcher`, shared by webhooks and auto-download; add `voice` media type.
- Schema v2: `auto_download_rules(id, name, enabled, media_types DEFAULT 'voice', created_at, updated_at)`, `auto_download_triggers(id, rule_id, trigger_type, trigger_value, match_type DEFAULT 'exact', enabled)`, `auto_download_logs(rule_id, message_id, chat_jid, status, error, duration_ms)`. Optional YAML seed next to `webhooks.yaml.example`.
- Config: `AUTO_DOWNLOAD_VOICE=none|rules|all` (default `rules`), `AUTO_DOWNLOAD_WORKERS=2`, `AUTO_DOWNLOAD_RETENTION_DAYS`.
- `internal/autodownload/manager.go`: match in `HandleMessage` after storing, enqueue to a buffered channel, worker pool calls `DownloadMedia` with a 2-min timeout. Optionally fire webhooks with `download_url` filled in.
- API `GET/POST /api/auto-download`, `PUT/DELETE /api/auto-download/{id}`, `GET /api/auto-download-logs`; "Auto-download" tab in `whatsapp-webhook-ui/`; MCP tools `list_/add_/remove_auto_download_rule`.
- Verify: rule for a test sender → their voice note appears within seconds with a success log row; another sender's does not.

### Phase 5 — Batch by sender, missing history 🟡

- ✅ Batch by chat (Milestone 1).
- ❌ Filter by `sender` across groups and by `voice` once `is_ptt` exists.
- ❌ `POST /api/history/request {chat_jid, count}` using `BuildHistorySyncRequest` when the DB lacks older messages; MCP tool `request_chat_history`. Old history media is expired, so this depends on a working media retry (B1).

### Phase 6 — Transcription (later) ❌

`transcribe_voice_message(message_id, chat_jid, language)` MCP tool with `faster-whisper` or an API backend; cache in a `transcripts` table; add `transcript` to `list_messages`; optional `transcribe: true` on auto-download rules.

---

## 7. Risks

- **DB integrity** — §4. The replay-overwrite hazard (§4.3) must be closed before Phase 1 writes download state into `messages`.
- **Exposure** — port 8080 is public and unauthenticated; S1 showed what that costs. Bind it to localhost or the Docker network, or add token auth.
- **Expired media is the norm** for anything older than ~1–2 weeks; report per item, never abort a batch.
- **MCP SDK v2** — unpinned today (Phase 0).
- **Rate limits** — one worker, 250 ms pace for history; ≤ 2 workers for auto-download.
- **Disk** — ~120 KB per minute of voice; add retention before `AUTO_DOWNLOAD_VOICE=all`.
- **View-once** audio — skip.
