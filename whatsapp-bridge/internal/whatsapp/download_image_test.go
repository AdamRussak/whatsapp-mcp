package whatsapp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/util/cbcutil"
	"go.mau.fi/whatsmeow/util/hkdfutil"
	"whatsapp-bridge/internal/database"
	bridgetypes "whatsapp-bridge/internal/types"
)

// ADR 0002, T4: which stored types download, with which WhatsApp key type and file name.
func TestDownloadKindFor(t *testing.T) {
	cases := []struct {
		mediaType, mimetype string
		ok                  bool
		waType              whatsmeow.MediaType
		ext, wantMime       string
	}{
		{"audio", "", true, whatsmeow.MediaAudio, ".ogg", "audio/ogg; codecs=opus"},
		{"audio", "audio/mpeg", true, whatsmeow.MediaAudio, ".ogg", "audio/ogg; codecs=opus"}, // unchanged from before T4
		{"image", "image/jpeg", true, whatsmeow.MediaImage, ".jpg", "image/jpeg"},
		{"image", "image/png", true, whatsmeow.MediaImage, ".png", "image/png"},
		{"image", "image/webp", true, whatsmeow.MediaImage, ".webp", "image/webp"},
		{"image", "", true, whatsmeow.MediaImage, ".jpg", "image/jpeg"}, // row stored before ADR 0002
		{"video", "video/mp4", true, whatsmeow.MediaVideo, ".mp4", "video/mp4"},
		{"video", "", true, whatsmeow.MediaVideo, ".mp4", "video/mp4"},
		{"document", "application/pdf", false, "", "", ""},
		{"poll", "", false, "", "", ""},
		{"", "", false, "", "", ""},
	}
	for _, tc := range cases {
		kind, ok := downloadKindFor(&database.MediaInfo{MediaType: tc.mediaType, Mimetype: tc.mimetype})
		if ok != tc.ok || kind.waType != tc.waType || kind.ext != tc.ext || kind.mimetype != tc.wantMime {
			t.Errorf("downloadKindFor(%q, %q) = %+v, %v; want {%s %s %s}, %v",
				tc.mediaType, tc.mimetype, kind, ok, tc.waType, tc.ext, tc.wantMime, tc.ok)
		}
	}
}

// storeMedia adds one media message to the download fixture's chat.
func storeMedia(t *testing.T, ms *database.MessageStore, id string, meta bridgetypes.MediaMetadata) {
	t.Helper()
	// The fixture's own timestamp, so file names match the ones the tests expect
	at := time.Date(2026, 9, 18, 10, 20, 48, 0, time.UTC)
	if err := ms.StoreMessage(id, testChat, "972500000001", "", at, false, meta); err != nil {
		t.Fatal(err)
	}
}

// An image or video is named from its stored mimetype and reported as what it is. The
// file is already on disk, so this runs without any network access.
func TestDownloadMediaImageAndVideoCacheHit(t *testing.T) {
	dir, c, ms, media, _ := newDownloadFixture(t, make([]byte, 32))
	key := make([]byte, 32)

	cases := []struct {
		id, mediaType, mimetype, file, wantMime string
	}{
		{"IMG1", "image", "image/png", "2026-09-18_102048_them_IMG1.png", "image/png"},
		{"IMG2", "image", "", "2026-09-18_102048_them_IMG2.jpg", "image/jpeg"},
		{"VID1", "video", "video/mp4", "2026-09-18_102048_them_VID1.mp4", "video/mp4"},
	}
	for _, tc := range cases {
		storeMedia(t, ms, tc.id, bridgetypes.MediaMetadata{MediaType: tc.mediaType, Mimetype: tc.mimetype,
			URL: "https://mmg.whatsapp.net/v/x.enc", MediaKey: key, FileSHA256: key, FileEncSHA256: key})
		rel := filepath.Join("media", "972500000001", tc.file)
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), []byte("bytes"), 0644); err != nil {
			t.Fatal(err)
		}

		res, err := c.DownloadMedia(context.Background(), media, ms, tc.id, testChat, dir)
		if err != nil {
			t.Fatalf("%s: %v", tc.id, err)
		}
		if !res.Cached || res.Path != rel || res.MediaType != tc.mediaType || res.Mimetype != tc.wantMime {
			t.Errorf("%s: result = %+v, want cached %s as %s %s", tc.id, res, rel, tc.mediaType, tc.wantMime)
		}
	}

	// Audio is unchanged: still named .ogg and reported as Opus
	audioRel := filepath.Join("media", "972500000001", "2026-09-18_102048_me_AUD1.ogg")
	if err := os.WriteFile(filepath.Join(dir, audioRel), []byte("OggS"), 0644); err != nil {
		t.Fatal(err)
	}
	res, err := c.DownloadMedia(context.Background(), media, ms, "AUD1", testChat, dir)
	if err != nil || res.Path != audioRel || res.MediaType != "audio" || res.Mimetype != "audio/ogg; codecs=opus" {
		t.Errorf("audio: result = %+v, %v", res, err)
	}
}

// A record written under another name is still found, and reported with the row's type.
func TestDownloadMediaImageCacheHitFromRecord(t *testing.T) {
	dir, c, ms, media, _ := newDownloadFixture(t, make([]byte, 32))
	key := make([]byte, 32)
	storeMedia(t, ms, "IMG1", bridgetypes.MediaMetadata{MediaType: "image", Mimetype: "image/webp",
		URL: "https://mmg.whatsapp.net/v/x.enc", MediaKey: key, FileSHA256: key, FileEncSHA256: key})
	rel := filepath.Join("media", "972500000001", "older.webp")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rel), []byte("RIFF"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := media.RecordDownload(&database.MediaDownloadRecord{
		MessageID: "IMG1", ChatJID: testChat, LocalPath: rel, SHA256: "abc", Status: database.DownloadStatusOK,
	}); err != nil {
		t.Fatal(err)
	}
	res, err := c.DownloadMedia(context.Background(), media, ms, "IMG1", testChat, dir)
	if err != nil || !res.Cached || res.Path != rel || res.MediaType != "image" || res.Mimetype != "image/webp" {
		t.Errorf("result = %+v, %v", res, err)
	}
}

// Documents are still refused, before anything is recorded.
func TestDownloadMediaRejectsDocument(t *testing.T) {
	dir, c, ms, media, _ := newDownloadFixture(t, make([]byte, 32))
	key := make([]byte, 32)
	storeMedia(t, ms, "DOC1", bridgetypes.MediaMetadata{MediaType: "document", Filename: "a.pdf",
		URL: "https://mmg.whatsapp.net/v/d.enc", MediaKey: key, FileSHA256: key, FileEncSHA256: key})

	if _, err := c.DownloadMedia(context.Background(), media, ms, "DOC1", testChat, dir); err == nil {
		t.Fatal("a document was accepted for download")
	}
	if rec, _ := media.GetDownload("DOC1", testChat); rec != nil {
		t.Errorf("refusing a document recorded %+v", rec)
	}
}

// A view-once image is stored without a key (decision 4), so a download stops at the
// metadata check and records a permanent failure, before any network access.
func TestDownloadMediaViewOnceImageHasNoKey(t *testing.T) {
	dir, c, ms, media, _ := newDownloadFixture(t, make([]byte, 32))
	storeMedia(t, ms, "VO1", bridgetypes.MediaMetadata{MediaType: "image"})

	if _, err := c.DownloadMedia(context.Background(), media, ms, "VO1", testChat, dir); err == nil {
		t.Fatal("a keyless view-once image was downloaded")
	}
	rec, err := media.GetDownload("VO1", testChat)
	if err != nil || rec == nil || rec.Status != database.DownloadStatusIncompleteMetadata {
		t.Fatalf("record = %+v, %v; want %s", rec, err, database.DownloadStatusIncompleteMetadata)
	}
}

// The key type matters: WhatsApp derives image keys under a different label than audio,
// which is why the downloader must pass MediaImage for an image.
func TestImageKeysNeedTheImageLabel(t *testing.T) {
	plain := []byte("\xff\xd8\xff fake JPEG payload that spans more than one AES block")
	mediaKey := bytes.Repeat([]byte{9}, 32)
	hash := sha256.Sum256(plain)

	expanded := hkdfutil.SHA256(mediaKey, nil, []byte(whatsmeow.MediaImage), 112)
	iv, cipherKey, macKey := expanded[:16], expanded[16:48], expanded[48:80]
	ciphertext, err := cbcutil.Encrypt(cipherKey, iv, append([]byte(nil), plain...))
	if err != nil {
		t.Fatal(err)
	}
	h := hmac.New(sha256.New, macKey)
	h.Write(iv)
	h.Write(ciphertext)
	blob := append(ciphertext, h.Sum(nil)[:mediaMACLength]...)

	kind, _ := downloadKindFor(&database.MediaInfo{MediaType: "image", Mimetype: "image/jpeg"})
	got, err := decryptMediaBlob(blob, mediaKey, hash[:], kind.waType)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("image blob with the image key type: err = %v", err)
	}
	if _, err := decryptMediaBlob(blob, mediaKey, hash[:], whatsmeow.MediaAudio); !errors.Is(err, errBlobMACMismatch) {
		t.Errorf("image blob with the audio key type: got %v, want MAC mismatch", err)
	}
}
