package whatsapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/util/cbcutil"
	"go.mau.fi/whatsmeow/util/hkdfutil"
)

const (
	mediaMACLength   = 10
	rawMediaHost     = "https://mmg.whatsapp.net"
	rawMediaMaxBytes = 64 << 20
)

var (
	errBlobMACMismatch   = errors.New("media MAC mismatch with original media key")
	errBlobPlainMismatch = errors.New("decrypted media SHA256 mismatch")
)

var rawMediaHTTP = &http.Client{Timeout: 60 * time.Second}

// fetchRawMedia downloads an encrypted media blob by direct path without any hash checks.
func fetchRawMedia(ctx context.Context, directPath string) ([]byte, error) {
	if !strings.HasPrefix(directPath, "/") {
		return nil, fmt.Errorf("direct path does not start with slash: %s", directPath)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawMediaHost+directPath, nil)
	if err != nil {
		return nil, err
	}
	// Same Origin/Referer whatsmeow sends for media downloads
	req.Header.Set("Origin", "https://web.whatsapp.com")
	req.Header.Set("Referer", "https://web.whatsapp.com/")
	resp, err := rawMediaHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("raw media download failed with status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, rawMediaMaxBytes))
}

// decryptMediaBlob verifies an encrypted blob's MAC with mediaKey, decrypts it, and requires the
// plaintext to match fileSHA256. It does not check the ciphertext hash, so it accepts a
// re-upload that differs from the original ciphertext but carries the same content and key.
func decryptMediaBlob(blob, mediaKey, fileSHA256 []byte, mediaType whatsmeow.MediaType) ([]byte, error) {
	if len(fileSHA256) != 32 {
		return nil, fmt.Errorf("missing plaintext SHA256")
	}
	if len(blob) <= mediaMACLength {
		return nil, fmt.Errorf("media blob too short (%d bytes)", len(blob))
	}
	expanded := hkdfutil.SHA256(mediaKey, nil, []byte(mediaType), 112)
	iv, cipherKey, macKey := expanded[:16], expanded[16:48], expanded[48:80]
	ciphertext, mac := blob[:len(blob)-mediaMACLength], blob[len(blob)-mediaMACLength:]

	h := hmac.New(sha256.New, macKey)
	h.Write(iv)
	h.Write(ciphertext)
	if !hmac.Equal(h.Sum(nil)[:mediaMACLength], mac) {
		return nil, errBlobMACMismatch
	}
	// cbcutil.Decrypt works in place; decrypt a copy so the caller's blob stays intact
	plain, err := cbcutil.Decrypt(cipherKey, iv, append([]byte(nil), ciphertext...))
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt media: %w", err)
	}
	if sum := sha256.Sum256(plain); !hmac.Equal(sum[:], fileSHA256) {
		return nil, errBlobPlainMismatch
	}
	return plain, nil
}
