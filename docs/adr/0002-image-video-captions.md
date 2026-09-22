# ADR 0002: Image and video captions, and the metadata a later download needs

- **Status:** Accepted. T1–T4 implemented on branch `image-video-captions`. Not deployed,
  not committed.
- **Date:** 2026-09-22
- **Deciders:** Adam Russak
- **Scope:** `whatsapp-bridge` (receive path, history sync, listener validation). `messages.db`
  columns that already exist. Webhook `content` and MCP search, which already read that
  column.
- **Supersedes, once implemented:** the caption sentence in
  [ADR 0001](0001-combining-listener-triggers.md) (open question 3, and the keyword bullet
  in Context). Until this ADR is implemented, 0001 still describes the code.

## Context

Sending a JPEG already works. On 2026-09-22 a JPEG posted to `POST /api/send` was delivered
to a group. This ADR is about the other direction: what the bridge keeps when an image or a
video arrives, and what it does with the caption.

`ExtractMediaInfo` recognises `image` and `video` and stores the CDN URL, media key, both
hashes and the file length. It does not read the caption, the mimetype, the direct path,
the duration, the dimensions or the JPEG thumbnail. The filename is
`image_<second>.jpg` or `video_<second>.mp4`, so two messages in the same second share a
name.

`ExtractTextContent` reads three things, in order: `conversation`, extended text, then a
poll question. It never calls `GetCaption()`. History sync does not use that function. It
has its own copy that reads only `conversation` and extended text
(`internal/whatsapp/handlers.go`).

The two paths also differ in what they are handed. On the live path whatsmeow has already
unwrapped the message (`events.Message.UnwrapRaw`): device-sent, ephemeral, view-once (v1,
v2, v2 extension), document-with-caption and edited wrappers are removed before
`HandleMessage` runs, so a view-once photo arrives as an `ImageMessage` with `viewOnce`
set and a captioned document arrives as a `DocumentMessage` with its caption. History sync
hands over the raw `WebMessageInfo.Message`, which nothing unwraps. A wrapped history
message (view-once, a disappearing-messages chat, a document with caption) yields no media
type and no text today, and is dropped by the "no content and no media" check.

A caption-only image is still stored. `content` is empty and `media_type` is `image`, so
the row survives the "skip if there is no content and no media" check. Webhooks and the
MCP transcript then show the media type and an empty body. Keyword search looks at
`messages.content`, so a caption matches nothing. `CarriesText` is true only for plain
text and `poll`, and R6 is written for that (ADR 0001).

Live `messages` table, counted 2026-09-22:

| `media_type` | rows | rows with `content` | rows with `direct_path` | rows with `mimetype` |
|---|---:|---:|---:|---:|
| `image` | 47,139 | 0 | 0 | 0 |
| `video` | 9,914 | 0 | 0 | 0 |
| `document` | 730 | 0 | 0 | 0 |
| `audio` | 9,888 | 0 | 15 | 15 |

The protobuf is not kept. Those 57,783 image, video and document rows cannot grow a caption
later from this database. The 15 audio rows with a direct path are the ones ingested after
schema v1. Image and video rows have the URL and the key, which is enough for
`resolveDirectPath` to derive a CDN path, and they are missing the fields audio now stores
on purpose.

`POST /api/download`, the batch downloader and auto-download accept audio only. `GET
/api/media/` serves `.ogg` and `.mp3` only. Webhooks still set `media_download_url` to
`/api/download` for every image, video and document. That call answers "not an audio
message".

CDN links expire in about 11–19 days, and a media retry can come back encrypted with a
different key (AUDIO_DOWNLOAD_PLAN.md, B1). That limit is the same for a photo as for a
voice note.

Audio view-once is not stored at all. `ExtractMediaInfo` returns an empty result for it,
so the media type is `""`, the content is `""`, and `HandleMessage` skips both the row and
the webhooks. Image and video view-once are stored like any other image or video, keys
included.

Outbound sends are a separate hole, recorded here so it is not mistaken for the receive
path. `SendMessage` calls `StoreMessage` with an empty conversation and an empty
`MediaMetadata`. `StoreMessage` returns without writing when both are empty, and the error
is ignored. The JPEG sent on 2026-09-22 is not in `messages`.

## Decision

### 1. A caption is `content`

For a new message, `ExtractTextContent` grows one fallback, after plain text and before the
poll question:

1. `conversation`
2. extended text
3. image caption, then video caption, then document caption
4. poll question

The first non-empty value wins. Today the extended-text branch returns whatever it finds,
empty or not; it has to test for non-empty like the others for this order to hold. A
message carries one of these, so the order only matters when a wrapper has more than one.
The caption is read off the same image, video or document object `ExtractMediaInfo`
already finds. Neither function unwraps anything: the live path is already unwrapped by
whatsmeow, and history sync stays raw (see Context).

History sync stops keeping its own extractor and calls `ExtractTextContent`. It still
hands over the raw message, so a wrapped history message still yields nothing. Unwrapping
history sync is out of scope.

No new column and no new webhook field. `content` is the caption. MCP search, the
transcript and webhook `message.content` already display that column.

Document captions are included. They are the same gap in the same function, and leaving
them out would keep a third kind of text invisible. The title of this ADR is images and
video because that is the body of the decision; documents ride along.

### 2. Keywords match captions

This supersedes ADR 0001 open question 3.

`CarriesText` becomes true for `image`, `video` and `document`, as well as plain text and
`poll`. It stays false for `audio`, `poll_vote` and view-once audio.

R6's rejection text today says keywords never read captions. That sentence becomes false.
The rule itself stays: a keyword AND a media type is rejected only when none of the allowed
types carry text. After this change, `keyword = invoice` AND `media_type = image` is
allowed. `keyword = invoice` AND `media_type = audio` is still rejected.

Auto-download is unchanged, so R2 is unchanged. An image still has no audio for a listener
to capture.

### 3. Store the download fields that already have columns

On ingest of an image, a video or a document, `ExtractMediaInfo` also fills:

| Field | Image | Video | Document |
|---|---|---|---|
| `direct_path` | yes | yes | yes |
| `mimetype` | yes | yes | yes |
| `seconds` | no | yes | no |

These are the schema v1 columns. Audio already fills all three, plus `is_ptt`. `is_ptt`
stays audio-only, and `StoreMessage` keeps writing it only when `media_type` is `audio`.

No new columns. Width, height, the JPEG thumbnail and the GIF-playback flag are not stored.

Image and video filenames follow the audio pattern: the message ID, and an extension taken
from the mimetype (`.jpg`, `.jpeg`, `.png`, `.webp`, `.mp4`, and `.gif` when the mimetype
says so). A missing mimetype falls back to `.jpg` or `.mp4`. A missing message ID falls
back to the timestamp name used today. WhatsApp sends a GIF as `video/mp4` with the
playback flag set, so `.gif` will be rare; it is listed so an actual `image/gif` mimetype
is not mislabelled.

### 4. View-once images and videos keep no keys

If the image or video reports view-once, store `media_type` and the caption, and store no
URL, key, hash, file length or direct path. Same intent as view-once audio: nothing here
can download it later. Unlike view-once audio, which has no row and reaches no listener,
the row still says `image` or `video`, so a listener can tell a vanished photo from plain
text. This only applies on the live path; history sync never sees a view-once image
unwrapped (see Context).

### 5. Existing rows stay as they are

No backfill. The caption and the direct path were never stored, and the protobuf is gone.

`StoreMessage`'s `ON CONFLICT` clause updates only `direct_path`, `mimetype`, `seconds` and
`is_ptt`, and only by filling NULLs. It does not gain a `content` update. Replaying a
message that is already stored will not overwrite a caption, and it will not invent one.

A history re-sync that inserts a **new** row goes through decision 1 and gets the caption.
A re-sync of an **existing** id does not. It does fill `direct_path`, `mimetype` and, for
video, `seconds` where they are NULL, because decision 3 now supplies them and the conflict
clause already fills those columns. That is the one way an existing image or video row
changes under this ADR, and it only happens if the phone replays the message.

### 6. Downloading the bytes is the next task in this ADR, not a new one

`POST /api/download` accepts `image` and `video` as well as `audio`, and calls whatsmeow
with `MediaImage` or `MediaVideo`. The file extension comes from the mimetype, as in
decision 3. `GET /api/media/` may serve those extensions (`.jpg`, `.jpeg`, `.png`, `.webp`,
`.gif`, `.mp4`). Documents are not downloadable in this ADR. Their captions are stored;
their bytes are not fetched.

Auto-download stays audio-only. A listener that captures voice notes does not start saving
every photo.

`media_download_url` stays the static `/api/download` endpoint. It is a lie for images and
videos until this task ships, and it becomes true when it does. Phase 1 does not change the
payload.

The CDN window and B1 apply. A photo older than that window fails the same way a voice note
does, and the failure is recorded in `media.db` rather than retried forever.

### Out of scope

- Repairing the 47,139 image rows and 9,914 video rows already stored.
- Writing the outbound `SendMessage` row with its media fields. The sent JPEG is still
  dropped. That is a send-path bug, not this receive decision.
- The MCP `send_file` path, which the bridge and the MCP container do not share.
- Stickers. They are still unrecognised (ADR 0001).
- Width, height, JPEG thumbnail, GIF playback.
- Auto-download of images or videos.
- Asking the phone for history in order to recover old captions.
- Unwrapping history-sync messages (view-once, ephemeral, document-with-caption). They are
  dropped today and stay dropped.

## Consequences

**Positive**

- A new photo or video with a caption is searchable, shows up in the MCP transcript, and is
  delivered to webhooks as `content`.
- `keyword` AND `media_type = image` means what it says.
- Image and video rows grow the same direct path and mimetype audio has, so the download
  task does not need a migration.
- Two images in the same second no longer share a filename.
- View-once photos no longer leave decryption keys in `messages`.

**Negative**

- **Old captions are gone.** Search over the existing 57,783 media rows still finds
  nothing. Only messages ingested after the change have a caption.
- **Keyword listeners start matching photos and videos.** A listener with `keyword =
  invoice` and no media-type trigger, in OR mode, today matches only plain text and polls.
  After this change it also matches an image, video or document whose caption contains
  "invoice". That is the point of decision 2, and it can increase webhook deliveries for
  keyword listeners that already exist.
- **R6 gets quieter.** Combinations it used to reject (`keyword` AND `image`, `video` or
  `document`) become valid. `keyword` AND `audio` stays rejected.
- **`ON CONFLICT` will not fill a caption onto a row that already exists.** A message
  ingested before the deploy keeps an empty `content` even if the phone sends it again.
- **Webhook `seconds` becomes non-zero for video.** The field already exists in the
  payload and is `0` for everything but audio today. After T3 a video carries its duration
  there. No field is added or renamed.
- **The Downloads view will list images once T4 ships.** It was built for audio and shows a
  duration column; an image row shows `—` there. The download link still works.
- **Download still fails until task T4.** Until then, webhooks keep advertising
  `media_download_url` for images and videos, and the endpoint keeps refusing them.
- **No migration, so no rollback of a schema.** Rolling back the binary restores the old
  extractor. Rows written by the new binary keep their caption, direct path and mimetype;
  the old binary simply ignores the path and the mimetype for non-audio, which is what it
  does today.

## Review

Checked against the bridge on 2026-09-22, before implementation:

- `ExtractTextContent` and `CarriesText` live in `internal/whatsapp/media.go`. The live
  receive path calls both through `HandleMessage`. History sync does not.
- `StoreMessage` skips the write when `content` and `media_type` are both empty, and its
  conflict update does not touch `content`. Decision 5 depends on that staying true.
- The v1 columns are `direct_path`, `mimetype`, `seconds`, `is_ptt` (migration v1). Schema
  is v5. This ADR adds no version.
- `media_extract_test.go` asserts that a non-audio result has an empty direct path, zero
  seconds and `IsPTT == false`. T3 changes the first two for image and video and must
  change that assertion. `IsPTT` stays false.
- R6's text is in `checkKeywordsHaveText` (`internal/webhook/validation_rules.go`). The
  rule already asks `CarriesText`, so the behaviour change is in `CarriesText`. The
  message text is the part that would otherwise keep saying "never captions".
- `DownloadMediaWithOptions` rejects `media_type != "audio"` before it looks at the URL.
  Image rows could already be decrypted from the stored URL and key once that gate and
  `whatsmeow.MediaImage` are in place. `resolveDirectPath` falls back to the URL when
  `direct_path` is NULL.
- `mediapath.servableTypes` is `.ogg` and `.mp3`. T4 extends it. Retention deletes through
  the same allowlist, so one change covers both. Correction from the T4 pass: retention only
  sweeps files an auto-download listener captured, and auto-download stays audio-only. A
  photo fetched through `POST /api/download` is a manual download and is never swept,
  exactly like a manually downloaded voice note today.
- There is no caption test anywhere in the repo. There is also no test that exercises
  `HandleHistorySync`, so the T1 history-sync check is a code reading unless a test is
  added for it.
- No test pins `keyword` AND `media_type = image` as rejected. The R6 tests cover
  `audio` and `poll_vote` only, so T2 adds a test rather than changing one.

Second pass, 2026-09-22, against the same tree (`go build`, `go vet` and `go test ./...`
all pass):

- `events.Message.UnwrapRaw` in whatsmeow `20260917111002` removes the device-sent,
  bot-invoke, ephemeral, view-once (v1, v2, v2 extension), lottie-sticker,
  document-with-caption and edited wrappers before the event reaches `HandleMessage`.
  History sync reads `WebMessageInfo.Message` raw. Context and decisions 1 and 4 now say so.
- `ImageMessage`, `VideoMessage` and `DocumentMessage` each have `GetCaption`,
  `GetMimetype` and `GetDirectPath`; `VideoMessage` has `GetSeconds` and `GetGifPlayback`;
  `ImageMessage` and `VideoMessage` have `GetViewOnce`. `whatsmeow.MediaImage` and
  `MediaVideo` exist.
- View-once audio is skipped, not stored: `ExtractMediaInfo` returns nothing for it and
  `HandleMessage` returns before the write and before the webhooks. Context said "stored
  with no decryption material"; corrected.
- `media_download_url` is set for every media type except `poll` and `poll_vote`, so
  documents get it too. Context corrected.
- `GetMediaInfo` selects `direct_path` but not `mimetype`, and `DownloadMediaWithOptions`
  hardcodes the `.ogg` filename, `MediaType: "audio"` and `Mimetype: "audio/ogg;
  codecs=opus"` at three return sites. T4 lists these.
- The R6 sentence also lives in the rule table of `whatsapp-bridge/WEBHOOK_README.md` and
  in the `CarriesText` doc comment. T2 lists both. The UI has no copy of it.
- The `messages` counts drift as messages arrive. At this pass: image 47,141, video 9,916,
  document 730, audio 9,890 with 17 carrying `direct_path` and `mimetype`. The table above
  is the morning snapshot; the argument does not depend on the exact figures.

## Implementation

**Log, 2026-09-22.** T1–T3 are implemented together, with tests for each check below.
`captions_test.go` drives `HandleMessage` and `HandleHistorySync` against a real
`messages.db`, which closes the "no history-sync test" gap from the review. View-once
images and videos keep only their media type: no filename, URL, key, hashes, length,
direct path or mimetype. `go vet` and `go test ./...` pass. Not deployed.

**Log, 2026-09-22, T4.** Implemented after T1–T3, on the same branch, before either is
deployed. The download decides the key type, extension and reported mimetype per media
type in one place (`downloadKindFor`). Audio keeps its `.ogg` name and Opus mimetype
exactly, so existing files and `media.db` records still match. An image or video row with
no mimetype, which is every row stored before this ADR, falls back to `.jpg` or `.mp4`.
Tests cover the cache paths for image and video, a document refused before anything is
recorded, a keyless view-once image recorded as `incomplete_metadata`, image keys failing
under the audio label, serving `.png` as `image/png`, and retention removing a captured
`.jpg`. A real CDN download of an image cannot be unit-tested here; that is the first
live check. The README endpoint list now names the new types.

Tasks run in this order. A task is done when its checks pass. T1 and T2 ship together: a
caption stored in `content` that listeners still refuse to match is the inconsistency this
ADR exists to avoid. T3 can follow in the same change. T4 is the download task and can land
after T3 is deployed.

### T1. Captions into `content`

1. Extend `ExtractTextContent` as in decision 1, and make the extended-text branch fall
   through when its text is empty, so "first non-empty wins" is true of every step.
2. Replace the history-sync extractor with `ExtractTextContent`, called on the raw
   `msg.Message.Message` as today. No unwrapping is added.
3. Add tests: image caption, video caption, document caption, plain text winning over a
   caption, empty caption staying empty, poll question unchanged, extended text with an
   empty body not shadowing a later value.

**Checks.** A caption-only image stores `content` equal to the caption and
`media_type = image`. A plain-text message is unchanged. History sync of an unwrapped
image with a caption stores the same text as the live path. A wrapped history message
(view-once, ephemeral, document-with-caption) still yields nothing, as today.

### T2. Listeners

1. `CarriesText` returns true for `image`, `video` and `document`. Update its doc comment,
   which says captions are not read.
2. Rewrite the R6 message so it no longer says captions are never read, and let its advice
   name image, video and document beside poll. Keep the rule. Update the R6 row in
   `whatsapp-bridge/WEBHOOK_README.md`, which repeats the sentence.
3. Add validation tests: `keyword` AND `media_type = image` validates; `keyword` AND
   `media_type = audio` and `keyword` AND `poll_vote` are still R6. No existing test pins
   the old rejection of `image`, so nothing is removed.

**Checks.** `keyword = invoice` AND `media_type = image` validates. `keyword = invoice`
AND `media_type = audio` is still R6. An OR listener with only `keyword = invoice` matches
an image whose caption contains "invoice", and does not match an image with an empty
caption.

### T3. Download fields on ingest

1. Fill `direct_path`, `mimetype` and, for video, `seconds` in `ExtractMediaInfo`.
2. Name image and video files from the message ID and the mimetype, with the fallbacks in
   decision 3.
3. Apply decision 4 for view-once.
4. Update `media_extract_test.go` so image and video are allowed to carry `direct_path`
   and, for video, `seconds`. `is_ptt` stays unset.

**Checks.** A new image row has `direct_path` and `mimetype` set and `is_ptt` NULL. A
view-once image row has `media_type = image`, the caption in `content`, and NULL url,
media key and direct path. Two images with different ids in the same second get different
filenames.

### T4. Download the file

1. Add `mimetype` to `MediaInfo` and to the `GetMediaInfo` query. The download needs it
   for the extension, and today the query does not select it.
2. Let `DownloadMediaWithOptions` accept `image` and `video`, pick `whatsmeow.MediaImage`
   or `MediaVideo`, and name the file from the mimetype. The `.ogg` filename, the
   `MediaType: "audio"` and the `Mimetype: "audio/ogg; codecs=opus"` are hardcoded at the
   two cached returns and the final return; all three follow the row. A NULL mimetype
   on an audio row keeps today's values.
3. Add those extensions to `servableTypes`. Retention deletes through the same
   `mediapath.Resolve`, so one change covers both.
4. Leave the batch downloader and auto-download on audio. `POST /api/download` for one
   message is the way an image is fetched.
5. Keep refusing `document` and view-once rows that have no key. The existing
   decryption-material check already records `incomplete_metadata` for a keyless row.

**Checks.** A recent image downloads, is served by `GET /api/media/` with an image content
type, and a second call is idempotent. An audio download still writes `.ogg`. An image
older than the CDN window records a failure and is not retried forever. Auto-download does
not enqueue an image.

## Alternatives considered

- **A `caption` column beside `content`.** Callers would have to learn a second field.
  Webhooks, MCP search and the transcript already read `content`. A caption is the text of
  that message.
- **Leave keywords blind to captions.** Store the text for search, and keep `CarriesText`
  false so existing keyword listeners do not start firing on photos. That splits one piece
  of text into two meanings, and it leaves R6's "never captions" sentence true in the
  listener and false in the database. Decision 2 keeps them the same.
- **Backfill by re-reading `messages.db`.** The caption is not in the database. There is
  nothing to backfill from.
- **New columns for width, height, thumbnail and GIF playback.** Nothing in the bridge
  reads them. They can be a later migration if a client needs a preview before the file
  downloads.
- **Download images inside this same task as the caption change.** The caption change is
  safe to roll back and needs no CDN calls. The download change shares the audio
  downloader's expiry and re-key behaviour. Splitting them (T1–T3, then T4) keeps a
  caption deploy from waiting on that.
- **Turn auto-download on for images.** A listener built to capture voice notes would
  start writing every matched photo to disk. That is a separate product decision.
