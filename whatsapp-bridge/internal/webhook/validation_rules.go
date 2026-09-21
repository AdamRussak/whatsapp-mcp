package webhook

import (
	"fmt"
	"regexp"
	"strings"

	"whatsapp-bridge/internal/types"
	"whatsapp-bridge/internal/whatsapp"
)

// ValidationError is one reason a listener is rejected. TriggerIndex is the trigger's index
// in the submitted triggers array, or -1 when the problem is not about one trigger. Rule is
// R1–R6 from ADR 0001, or empty for the basic checks (name, URL, mode, trigger syntax).
type ValidationError struct {
	Rule         string `json:"rule"`
	TriggerIndex int    `json:"trigger_index"`
	Message      string `json:"message"`
}

// ValidationErrors is every reason a listener is rejected. It is the error
// ValidateWebhookConfig returns, and its message is the first one, which is what the API's
// single "error" string has always carried.
type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string {
	if len(e) == 0 {
		return ""
	}
	return e[0].Message
}

// messageKinds is every media type a message can have, "" being plain text. The rules
// reason over this finite set, which is what makes R1 and R5 exact.
var messageKinds = append([]string{""}, whatsapp.ProducedMediaTypes...)

const (
	fieldChat   = "chat_jid"
	fieldSender = "sender"
	fieldMedia  = "media_type"
	fieldText   = "keyword"
)

// indexedTrigger is an enabled trigger with its position in the submitted array.
type indexedTrigger struct {
	index int
	types.WebhookTrigger
}

// ruleCheck holds what one validation needs: the triggers still in play and the LID map.
type ruleCheck struct {
	wm       *Manager
	live     []indexedTrigger
	resolved map[string][]string
	lidOK    bool
	errs     ValidationErrors
}

func (c *ruleCheck) fail(rule string, index int, format string, args ...interface{}) {
	c.errs = append(c.errs, ValidationError{Rule: rule, TriggerIndex: index, Message: rule + ": " + fmt.Sprintf(format, args...)})
}

// checkTriggerRules applies R1–R6 to the enabled triggers. The principle: reject a listener
// only when no possible message could make it fire. Anything the rules cannot decide, such
// as two regexes, is allowed. Only called once every trigger passed the basic checks, so
// every regex here compiles.
func (wm *Manager) checkTriggerRules(config *types.WebhookConfig, modeOK bool) ValidationErrors {
	c := &ruleCheck{wm: wm, resolved: make(map[string][]string)}
	enabled := 0

	// R1, both modes. A trigger that fails it is reported once and left out of the rest:
	// every rule after this would only restate that it matches nothing.
	for i, trigger := range config.Triggers {
		if !trigger.Enabled {
			continue
		}
		enabled++
		if trigger.TriggerType == fieldMedia && len(c.acceptedKinds(trigger)) == 0 {
			c.fail("R1", i, "%s can never match: the bridge only ever produces the media types %s (and plain text, which has none). Pick one of those.",
				describe(trigger), strings.Join(whatsapp.ProducedMediaTypes, ", "))
			continue
		}
		c.live = append(c.live, indexedTrigger{i, trigger})
	}
	if !modeOK {
		return c.errs
	}

	and := config.Mode() == types.TriggerModeAnd
	if and {
		c.lidOK = whatsapp.LIDMapReadable(wm.storeDir)
		mediaConflict := c.checkOneValue()
		c.checkPatternsAgree()
		kinds := c.checkMediaOverlap(mediaConflict)
		if len(kinds) > 0 {
			c.checkKeywordsHaveText(kinds)
		}
	}
	if config.AutoDownload {
		c.checkAudioCanFire(and, enabled)
	}
	return c.errs
}

// acceptedKinds is the message kinds a media_type trigger accepts, decided by the matcher.
func (c *ruleCheck) acceptedKinds(trigger types.WebhookTrigger) []string {
	var kinds []string
	for _, kind := range messageKinds {
		if c.wm.matchesString(kind, trigger.TriggerValue, trigger.MatchType) {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

// canFireOn reports whether some message of this kind could satisfy the trigger. Chat and
// sender say nothing about the media type. A keyword reads text; on a kind without text it
// sees "", which only a pattern that matches the empty string accepts.
func (c *ruleCheck) canFireOn(trigger types.WebhookTrigger, kind string) bool {
	switch trigger.TriggerType {
	case fieldMedia:
		return c.wm.matchesString(kind, trigger.TriggerValue, trigger.MatchType)
	case fieldText:
		return whatsapp.CarriesText(kind) || c.wm.matchesString("", trigger.TriggerValue, trigger.MatchType)
	default:
		return true
	}
}

// forms is every identifier a chat or sender value stands for: what was typed, and every
// JID the LID map says is the same contact. A sender also matches by bare user.
func (c *ruleCheck) forms(field, value string) []string {
	if _, ok := c.resolved[value]; !ok {
		jids, err := whatsapp.ResolveChatJIDs(value, c.wm.storeDir)
		if err != nil {
			jids = nil
		}
		c.resolved[value] = jids
	}
	out := []string{value}
	for _, jid := range c.resolved[value] {
		out = append(out, jid)
		if user, _, ok := strings.Cut(jid, "@"); ok && field == fieldSender {
			out = append(out, user)
		}
	}
	return out
}

// sameContact reports whether two exact chat or sender values can name the same one.
func (c *ruleCheck) sameContact(field, a, b string) bool {
	if a == b {
		return true
	}
	seen := make(map[string]bool)
	for _, f := range c.forms(field, a) {
		seen[f] = true
	}
	for _, f := range c.forms(field, b) {
		if seen[f] {
			return true
		}
	}
	return false
}

// lidNote is appended when R3 or R4 had to compare values as typed.
func (c *ruleCheck) lidNote() string {
	if c.lidOK {
		return ""
	}
	return " (The phone/LID check was skipped because whatsapp.db could not be read, so a phone number and the LID of the same contact count as different here.)"
}

func (c *ruleCheck) exacts(field string) []indexedTrigger {
	var out []indexedTrigger
	for _, t := range c.live {
		if t.TriggerType == field && t.MatchType == "exact" {
			out = append(out, t)
		}
	}
	return out
}

// checkOneValue is R3 (AND): a message has one chat, one sender and one media type, so two
// exact values on one of those fields must name the same thing. It reports whether it found
// a media-type conflict, which R5 would otherwise report a second time.
func (c *ruleCheck) checkOneValue() (mediaConflict bool) {
	for _, field := range []string{fieldChat, fieldSender, fieldMedia} {
		exacts := c.exacts(field)
		if len(exacts) < 2 {
			continue
		}
		first := exacts[0]
		for _, other := range exacts[1:] {
			if field == fieldMedia {
				if other.TriggerValue == first.TriggerValue {
					continue
				}
				mediaConflict = true
				c.fail("R3", other.index, "A message has one media type, so %s and %s can never both match. To catch either: set Match to OR, or use one trigger with regex ^(%s|%s)$.",
					describe(first.WebhookTrigger), describe(other.WebhookTrigger),
					regexp.QuoteMeta(first.TriggerValue), regexp.QuoteMeta(other.TriggerValue))
				continue
			}
			if c.sameContact(field, first.TriggerValue, other.TriggerValue) {
				continue
			}
			if field == fieldChat {
				c.fail("R3", other.index, "A message belongs to one chat, so %s and %s can never both match. For audio from either chat: set Match to OR with just the two chat triggers if this listener auto-downloads, since it only ever captures audio; otherwise create one listener per chat.%s",
					describe(first.WebhookTrigger), describe(other.WebhookTrigger), c.lidNote())
			} else {
				c.fail("R3", other.index, "A message has one sender, so %s and %s can never both match. For messages from either: set Match to OR, or create one listener per sender.%s",
					describe(first.WebhookTrigger), describe(other.WebhookTrigger), c.lidNote())
			}
		}
	}
	return mediaConflict
}

// checkPatternsAgree is R4 (AND): when a chat or sender has an exact trigger, every contains
// or regex trigger on that field must match that value or one of its LID forms. Media types
// are left to R5, which decides them exactly.
func (c *ruleCheck) checkPatternsAgree() {
	for _, field := range []string{fieldChat, fieldSender} {
		exacts := c.exacts(field)
		if len(exacts) == 0 {
			continue
		}
		var forms []string
		for _, e := range exacts {
			forms = append(forms, c.forms(field, e.TriggerValue)...)
		}
		for _, t := range c.live {
			if t.TriggerType != field || t.MatchType == "exact" {
				continue
			}
			matched := false
			for _, f := range forms {
				if c.wm.matchesString(f, t.TriggerValue, t.MatchType) {
					matched = true
					break
				}
			}
			if !matched {
				c.fail("R4", t.index, "%s can never match together with %s: that value, and every phone/LID form of it, fails the pattern. Remove one of the two, change the pattern so it matches %s, or set Match to OR.%s",
					describe(t.WebhookTrigger), describe(exacts[0].WebhookTrigger), exacts[0].TriggerValue, c.lidNote())
			}
		}
	}
}

// checkMediaOverlap is R5 (AND): some media type must satisfy every media trigger. The set
// is finite, so this is exact. It returns the kinds every media trigger accepts — all of
// them when there are no media triggers, none when the check failed.
func (c *ruleCheck) checkMediaOverlap(skipReport bool) []string {
	kinds := messageKinds
	for _, t := range c.live {
		if t.TriggerType != fieldMedia {
			continue
		}
		var next []string
		for _, kind := range kinds {
			if c.canFireOn(t.WebhookTrigger, kind) {
				next = append(next, kind)
			}
		}
		if len(next) == 0 {
			if !skipReport {
				c.fail("R5", t.index, "Under AND every media trigger must accept the same media type, and none does: the media triggers above allow only %s, and %s allows only %s. Change this trigger, or set Match to OR.",
					kindList(kinds), describe(t.WebhookTrigger), kindList(c.acceptedKinds(t.WebhookTrigger)))
			}
			return nil
		}
		kinds = next
	}
	return kinds
}

// checkKeywordsHaveText is R6 (AND): keywords read only plain text and poll questions, so
// the media types the listener allows must include one that carries text.
func (c *ruleCheck) checkKeywordsHaveText(kinds []string) {
	for _, kind := range kinds {
		if whatsapp.CarriesText(kind) {
			return
		}
	}
	for _, t := range c.live {
		if t.TriggerType == fieldText && !c.canFireOn(t.WebhookTrigger, kinds[0]) {
			c.fail("R6", t.index, "Keywords only read plain text and poll questions, never captions, and the media triggers here allow only %s, which has no text. So %s can never match. Remove this keyword, allow media type = poll, or set Match to OR.",
				kindList(kinds), describe(t.WebhookTrigger))
		}
	}
}

// checkAudioCanFire is R2 (both modes, auto-download on): auto-download only ever captures
// audio, so some audio message must be able to fire the listener. Under OR one trigger an
// audio message can satisfy is enough; under AND every trigger must be. A trigger another
// rule already rejected is not reported again.
func (c *ruleCheck) checkAudioCanFire(and bool, enabled int) {
	const advice = "Turn off auto-download, or add a trigger an audio message can match: a chat, a sender, or media type = audio."
	if enabled == 0 {
		c.fail("R2", -1, "Auto-download only captures audio, and this listener has no enabled trigger, so it can never match anything. %s", advice)
		return
	}
	if len(c.live) == 0 {
		return // every enabled trigger already failed R1, which says why
	}

	if !and {
		for _, t := range c.live {
			if c.canFireOn(t.WebhookTrigger, "audio") {
				return
			}
		}
		c.fail("R2", -1, "Auto-download only captures audio, and none of these triggers can match an audio message (it has no text for a keyword to read). %s", advice)
		return
	}

	reported := make(map[int]bool)
	for _, e := range c.errs {
		reported[e.TriggerIndex] = true
	}
	for _, t := range c.live {
		if c.canFireOn(t.WebhookTrigger, "audio") || reported[t.index] {
			continue
		}
		why := "never matches audio"
		if t.TriggerType == fieldText {
			why = "can never match audio, which has no text"
		}
		c.fail("R2", t.index, "Auto-download only captures audio, and under AND every trigger must match, but %s %s. Remove this trigger, set Match to OR, or turn off auto-download.",
			describe(t.WebhookTrigger), why)
	}
}

// describe renders a trigger the way the messages and the form's sentence talk about it.
func describe(t types.WebhookTrigger) string {
	name := map[string]string{
		fieldChat: "chat", fieldSender: "sender", fieldText: "keyword", fieldMedia: "media type",
	}[t.TriggerType]
	switch {
	case t.TriggerType == "all":
		return "all messages"
	case t.MatchType == "contains":
		return fmt.Sprintf("%s contains %q", name, t.TriggerValue)
	case t.MatchType == "regex":
		return fmt.Sprintf("%s matches regex %s", name, t.TriggerValue)
	default:
		return fmt.Sprintf("%s = %s", name, t.TriggerValue)
	}
}

func kindList(kinds []string) string {
	names := make([]string, len(kinds))
	for i, kind := range kinds {
		if kind == "" {
			kind = "plain text"
		}
		names[i] = kind
	}
	return strings.Join(names, ", ")
}
