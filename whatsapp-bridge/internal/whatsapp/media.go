package whatsapp

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"whatsapp-bridge/internal/types"
)

// ExtractTextContent extracts the text of a WhatsApp message (ADR 0002, decision 1).
//
// The first non-empty value wins, in this order: plain conversation text, extended text,
// the caption of an image, a video or a document, then a poll's question. It unwraps
// nothing: on the live path whatsmeow has already removed view-once, ephemeral and
// document-with-caption wrappers, and history sync hands over the raw message.
func ExtractTextContent(msg *waProto.Message) string {
	if msg == nil {
		return ""
	}

	if text := msg.GetConversation(); text != "" {
		return text
	}
	if text := msg.GetExtendedTextMessage().GetText(); text != "" {
		return text
	}
	if caption := msg.GetImageMessage().GetCaption(); caption != "" {
		return caption
	}
	if caption := msg.GetVideoMessage().GetCaption(); caption != "" {
		return caption
	}
	if caption := msg.GetDocumentMessage().GetCaption(); caption != "" {
		return caption
	}

	// A poll's question is its text
	if poll := GetPollCreationFromMessage(msg); poll != nil {
		return poll.GetName()
	}

	return ""
}

// GetPollCreationFromMessage checks all poll creation message versions and returns the first non-nil one
func GetPollCreationFromMessage(msg *waProto.Message) *waProto.PollCreationMessage {
	if msg == nil {
		return nil
	}
	if p := msg.GetPollCreationMessage(); p != nil {
		return p
	}
	if p := msg.GetPollCreationMessageV2(); p != nil {
		return p
	}
	if p := msg.GetPollCreationMessageV3(); p != nil {
		return p
	}
	if p := msg.GetPollCreationMessageV5(); p != nil {
		return p
	}
	if p := msg.GetPollCreationMessageV6(); p != nil {
		return p
	}
	return nil
}

// ExtractPollInfo extracts poll creation information from a WhatsApp message
func ExtractPollInfo(msg *waProto.Message) *types.PollInfo {
	poll := GetPollCreationFromMessage(msg)
	if poll == nil {
		return nil
	}

	options := make([]types.PollOption, len(poll.GetOptions()))
	for i, opt := range poll.GetOptions() {
		optName := opt.GetOptionName()
		optHash := sha256.Sum256([]byte(optName))
		options[i] = types.PollOption{
			Name:   optName,
			SHA256: fmt.Sprintf("%x", optHash[:]),
		}
	}

	return &types.PollInfo{
		Question:        poll.GetName(),
		Options:         options,
		SelectableCount: poll.GetSelectableOptionsCount(),
	}
}

// ProducedMediaTypes is every media type ExtractMediaInfo can return, other than "" for a
// plain text message. It is a closed set: listener validation (ADR 0001) rejects a trigger
// that could only match a type outside it. Keep it in step with ExtractMediaInfo; a test
// checks that every type it returns is listed here.
var ProducedMediaTypes = []string{"audio", "document", "image", "poll", "poll_vote", "video"}

// CarriesText reports whether a message of this media type has text ExtractTextContent
// reads: plain text (""), a poll's question, and the caption of an image, a video or a
// document (ADR 0002). Audio, a poll vote and view-once audio have none.
func CarriesText(mediaType string) bool {
	switch mediaType {
	case "", "poll", "image", "video", "document":
		return true
	}
	return false
}

// ExtractMediaInfo extracts media information from a WhatsApp message.
//
// msgID names the file for audio, images and videos, so two messages in the same second no
// longer collide (the old name was built from time.Now() at second resolution). Callers
// that only need the media type may pass "".
func ExtractMediaInfo(msg *waProto.Message, msgID string) types.MediaMetadata {
	if msg == nil {
		return types.MediaMetadata{}
	}

	// Check for poll creation message
	if GetPollCreationFromMessage(msg) != nil {
		return types.MediaMetadata{MediaType: "poll"}
	}

	// Check for poll update (vote) message
	if msg.GetPollUpdateMessage() != nil {
		return types.MediaMetadata{MediaType: "poll_vote"}
	}

	// Check for image message
	if img := msg.GetImageMessage(); img != nil {
		// View-once is meant to vanish: keep the type (the caption is content) but no
		// decryption material, so nothing here can download it later (ADR 0002, decision 4).
		if img.GetViewOnce() {
			return types.MediaMetadata{MediaType: "image"}
		}
		return types.MediaMetadata{
			MediaType:     "image",
			Filename:      mediaFilename("image_", msgID, img.GetMimetype(), ".jpg"),
			URL:           img.GetURL(),
			MediaKey:      img.GetMediaKey(),
			FileSHA256:    img.GetFileSHA256(),
			FileEncSHA256: img.GetFileEncSHA256(),
			FileLength:    img.GetFileLength(),
			DirectPath:    img.GetDirectPath(),
			Mimetype:      img.GetMimetype(),
		}
	}

	// Check for video message
	if vid := msg.GetVideoMessage(); vid != nil {
		if vid.GetViewOnce() {
			return types.MediaMetadata{MediaType: "video"}
		}
		return types.MediaMetadata{
			MediaType:     "video",
			Filename:      mediaFilename("video_", msgID, vid.GetMimetype(), ".mp4"),
			URL:           vid.GetURL(),
			MediaKey:      vid.GetMediaKey(),
			FileSHA256:    vid.GetFileSHA256(),
			FileEncSHA256: vid.GetFileEncSHA256(),
			FileLength:    vid.GetFileLength(),
			DirectPath:    vid.GetDirectPath(),
			Mimetype:      vid.GetMimetype(),
			Seconds:       vid.GetSeconds(),
		}
	}

	// Check for audio message
	if aud := msg.GetAudioMessage(); aud != nil {
		// View-once audio is meant to vanish after playing: keep no decryption material
		// for it, so nothing here can download it later.
		if aud.GetViewOnce() {
			return types.MediaMetadata{}
		}
		return types.MediaMetadata{
			MediaType:     "audio",
			Filename:      audioFilename(aud.GetPTT(), msgID),
			URL:           aud.GetURL(),
			MediaKey:      aud.GetMediaKey(),
			FileSHA256:    aud.GetFileSHA256(),
			FileEncSHA256: aud.GetFileEncSHA256(),
			FileLength:    aud.GetFileLength(),
			DirectPath:    aud.GetDirectPath(),
			Mimetype:      aud.GetMimetype(),
			Seconds:       aud.GetSeconds(),
			IsPTT:         aud.GetPTT(),
		}
	}

	// Check for document message
	if doc := msg.GetDocumentMessage(); doc != nil {
		filename := doc.GetFileName()
		if filename == "" {
			filename = "document_" + time.Now().Format("20060102_150405")
		}
		return types.MediaMetadata{
			MediaType:     "document",
			Filename:      filename,
			URL:           doc.GetURL(),
			MediaKey:      doc.GetMediaKey(),
			FileSHA256:    doc.GetFileSHA256(),
			FileEncSHA256: doc.GetFileEncSHA256(),
			FileLength:    doc.GetFileLength(),
			DirectPath:    doc.GetDirectPath(),
			Mimetype:      doc.GetMimetype(),
		}
	}

	return types.MediaMetadata{}
}

// MediaExtension maps an image or video mimetype to a file extension, or returns fallback
// for a missing or unrecognised one. Parameters such as "; codecs=..." are ignored.
func MediaExtension(mimetype, fallback string) string {
	base, _, _ := strings.Cut(mimetype, ";")
	switch strings.ToLower(strings.TrimSpace(base)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "video/mp4":
		return ".mp4"
	}
	return fallback
}

// mediaFilename names an image or video after its message ID, with the extension its
// mimetype implies, falling back to a timestamp when no ID is available.
func mediaFilename(prefix, msgID, mimetype, fallbackExt string) string {
	ext := MediaExtension(mimetype, fallbackExt)
	if msgID == "" {
		return prefix + time.Now().Format("20060102_150405") + ext
	}
	return prefix + msgID + ext
}

// audioFilename names a voice note or audio file after its message ID, falling back to a
// timestamp when no ID is available.
func audioFilename(isPTT bool, msgID string) string {
	prefix := "audio_"
	if isPTT {
		prefix = "voice_"
	}
	if msgID == "" {
		return prefix + time.Now().Format("20060102_150405") + ".ogg"
	}
	return prefix + msgID + ".ogg"
}

// AnalyzeOggOpus tries to extract duration and generate a simple waveform from an Ogg Opus file
func AnalyzeOggOpus(data []byte) (duration uint32, waveform []byte, err error) {
	// Try to detect if this is a valid Ogg file by checking for the "OggS" signature
	// at the beginning of the file
	if len(data) < 4 || string(data[0:4]) != "OggS" {
		return 0, nil, fmt.Errorf("not a valid Ogg file (missing OggS signature)")
	}

	// Parse Ogg pages to find the last page with a valid granule position
	var lastGranule uint64
	var sampleRate uint32 = 48000 // Default Opus sample rate
	var preSkip uint16 = 0
	var foundOpusHead bool

	// Scan through the file looking for Ogg pages
	for i := 0; i < len(data); {
		// Check if we have enough data to read Ogg page header
		if i+27 >= len(data) {
			break
		}

		// Verify Ogg page signature
		if string(data[i:i+4]) != "OggS" {
			// Skip until next potential page
			i++
			continue
		}

		// Extract header fields
		granulePos := binary.LittleEndian.Uint64(data[i+6 : i+14])
		pageSeqNum := binary.LittleEndian.Uint32(data[i+18 : i+22])
		numSegments := int(data[i+26])

		// Extract segment table
		if i+27+numSegments >= len(data) {
			break
		}
		segmentTable := data[i+27 : i+27+numSegments]

		// Calculate page size
		pageSize := 27 + numSegments
		for _, segLen := range segmentTable {
			pageSize += int(segLen)
		}

		// Check if we're looking at an OpusHead packet (should be in first few pages)
		if !foundOpusHead && pageSeqNum <= 1 {
			// Look for "OpusHead" marker in this page
			pageData := data[i : i+pageSize]
			headPos := bytes.Index(pageData, []byte("OpusHead"))
			if headPos >= 0 && headPos+12 < len(pageData) {
				// Found OpusHead, extract sample rate and pre-skip
				// OpusHead format: Magic(8) + Version(1) + Channels(1) + PreSkip(2) + SampleRate(4) + ...
				headPos += 8 // Skip "OpusHead" marker
				// PreSkip is 2 bytes at offset 10
				if headPos+12 <= len(pageData) {
					preSkip = binary.LittleEndian.Uint16(pageData[headPos+10 : headPos+12])
					sampleRate = binary.LittleEndian.Uint32(pageData[headPos+12 : headPos+16])
					foundOpusHead = true
					fmt.Printf("Found OpusHead: sampleRate=%d, preSkip=%d\n", sampleRate, preSkip)
				}
			}
		}

		// Keep track of last valid granule position
		if granulePos != 0 {
			lastGranule = granulePos
		}

		// Move to next page
		i += pageSize
	}

	if !foundOpusHead {
		fmt.Println("Warning: OpusHead not found, using default values")
	}

	// Calculate duration based on granule position
	if lastGranule > 0 {
		// Formula for duration: (lastGranule - preSkip) / sampleRate
		durationSeconds := float64(lastGranule-uint64(preSkip)) / float64(sampleRate)
		duration = uint32(math.Ceil(durationSeconds))
		fmt.Printf("Calculated Opus duration from granule: %f seconds (lastGranule=%d)\n",
			durationSeconds, lastGranule)
	} else {
		// Fallback to rough estimation if granule position not found
		fmt.Println("Warning: No valid granule position found, using estimation")
		durationEstimate := float64(len(data)) / 2000.0 // Very rough approximation
		duration = uint32(durationEstimate)
	}

	// Make sure we have a reasonable duration (at least 1 second, at most 300 seconds)
	if duration < 1 {
		duration = 1
	} else if duration > 300 {
		duration = 300
	}

	// Generate waveform
	waveform = placeholderWaveform(duration)

	fmt.Printf("Ogg Opus analysis: size=%d bytes, calculated duration=%d sec, waveform=%d bytes\n",
		len(data), duration, len(waveform))

	return duration, waveform, nil
}

// min returns the smaller of x or y
func min(x, y int) int {
	if x < y {
		return x
	}
	return y
}

// placeholderWaveform generates a synthetic waveform for WhatsApp voice messages
// that appears natural with some variability based on the duration
func placeholderWaveform(duration uint32) []byte {
	// WhatsApp expects a 64-byte waveform for voice messages
	const waveformLength = 64
	waveform := make([]byte, waveformLength)

	// Seed the random number generator for consistent results with the same duration
	rand.Seed(int64(duration))

	// Create a more natural looking waveform with some patterns and variability
	// rather than completely random values

	// Base amplitude and frequency - longer messages get faster frequency
	baseAmplitude := 35.0
	frequencyFactor := float64(min(int(duration), 120)) / 30.0

	for i := range waveform {
		// Position in the waveform (normalized 0-1)
		pos := float64(i) / float64(waveformLength)

		// Create a wave pattern with some randomness
		// Use multiple sine waves of different frequencies for more natural look
		val := baseAmplitude * math.Sin(pos*math.Pi*frequencyFactor*8)
		val += (baseAmplitude / 2) * math.Sin(pos*math.Pi*frequencyFactor*16)

		// Add some randomness to make it look more natural
		val += (rand.Float64() - 0.5) * 15

		// Add some fade-in and fade-out effects
		fadeInOut := math.Sin(pos * math.Pi)
		val = val * (0.7 + 0.3*fadeInOut)

		// Center around 50 (typical voice baseline)
		val = val + 50

		// Ensure values stay within WhatsApp's expected range (0-100)
		if val < 0 {
			val = 0
		} else if val > 100 {
			val = 100
		}

		waveform[i] = byte(val)
	}

	return waveform
}

// MediaDownloader implements whatsmeow.DownloadableMessage for stored media metadata
type MediaDownloader struct {
	URL           string
	DirectPath    string
	MediaKey      []byte
	FileLength    uint64
	FileSHA256    []byte
	FileEncSHA256 []byte
	MediaType     whatsmeow.MediaType
}

func (d *MediaDownloader) GetDirectPath() string             { return d.DirectPath }
func (d *MediaDownloader) GetURL() string                    { return d.URL }
func (d *MediaDownloader) GetMediaKey() []byte               { return d.MediaKey }
func (d *MediaDownloader) GetFileLength() uint64             { return d.FileLength }
func (d *MediaDownloader) GetFileSHA256() []byte             { return d.FileSHA256 }
func (d *MediaDownloader) GetFileEncSHA256() []byte          { return d.FileEncSHA256 }
func (d *MediaDownloader) GetMediaType() whatsmeow.MediaType { return d.MediaType }

// ExtractDirectPathFromURL extracts the direct_path from a stored WhatsApp MMS URL.
// The query string (ccb, oh, oe, _nc_sid) is part of the direct path: oh/oe authorize the
// CDN request, and whatsmeow appends "&hash=..." to it. Only the URL-only mms3 flag is dropped.
func ExtractDirectPathFromURL(url string) string {
	parts := strings.SplitN(url, ".net/", 2)
	if len(parts) < 2 {
		return url
	}
	path, query, hasQuery := strings.Cut(parts[1], "?")
	if !hasQuery {
		return "/" + path
	}
	var kept []string
	for _, param := range strings.Split(query, "&") {
		if param != "" && param != "mms3=true" {
			kept = append(kept, param)
		}
	}
	if len(kept) == 0 {
		return "/" + path
	}
	return "/" + path + "?" + strings.Join(kept, "&")
}
