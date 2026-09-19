package whatsapp

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/util/cbcutil"
	"go.mau.fi/whatsmeow/util/hkdfutil"
)

// encryptTestMedia builds a WhatsApp-style .enc blob (ciphertext + 10-byte MAC)
func encryptTestMedia(t *testing.T, plain, mediaKey []byte) []byte {
	t.Helper()
	expanded := hkdfutil.SHA256(mediaKey, nil, []byte(whatsmeow.MediaAudio), 112)
	iv, cipherKey, macKey := expanded[:16], expanded[16:48], expanded[48:80]
	ciphertext, err := cbcutil.Encrypt(cipherKey, iv, append([]byte(nil), plain...))
	if err != nil {
		t.Fatal(err)
	}
	h := hmac.New(sha256.New, macKey)
	h.Write(iv)
	h.Write(ciphertext)
	return append(ciphertext, h.Sum(nil)[:mediaMACLength]...)
}

func TestDecryptMediaBlob(t *testing.T) {
	plain := []byte("OggS fake voice note payload that spans more than one AES block")
	mediaKey := bytes.Repeat([]byte{7}, 32)
	plainHash := sha256.Sum256(plain)
	blob := encryptTestMedia(t, plain, mediaKey)

	got, err := decryptMediaBlob(blob, mediaKey, plainHash[:], whatsmeow.MediaAudio)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("valid blob: got err=%v, equal=%v", err, bytes.Equal(got, plain))
	}

	otherKey := bytes.Repeat([]byte{8}, 32)
	if _, err := decryptMediaBlob(blob, otherKey, plainHash[:], whatsmeow.MediaAudio); !errors.Is(err, errBlobMACMismatch) {
		t.Errorf("wrong key: got %v, want MAC mismatch", err)
	}

	wrongHash := sha256.Sum256([]byte("different content"))
	if _, err := decryptMediaBlob(blob, mediaKey, wrongHash[:], whatsmeow.MediaAudio); !errors.Is(err, errBlobPlainMismatch) {
		t.Errorf("wrong plaintext hash: got %v, want plaintext mismatch", err)
	}

	tampered := append([]byte(nil), blob...)
	tampered[0] ^= 1
	if _, err := decryptMediaBlob(tampered, mediaKey, plainHash[:], whatsmeow.MediaAudio); !errors.Is(err, errBlobMACMismatch) {
		t.Errorf("tampered blob: got %v, want MAC mismatch", err)
	}

	if _, err := decryptMediaBlob(blob, mediaKey, nil, whatsmeow.MediaAudio); err == nil {
		t.Error("missing plaintext hash: expected error")
	}
}
