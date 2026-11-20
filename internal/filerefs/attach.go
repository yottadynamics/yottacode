package filerefs

import (
	"fmt"
	"strings"
	"time"
)

// PrepareOptions configures Prepare for one entry point.
type PrepareOptions struct {
	Cwd string
	// AllowOutside permits absolute / "~/" / "../" references (see
	// LoadOptions). Only interactive callers should set it.
	AllowOutside bool
	// OutsideAllowed narrows AllowOutside per reference (see LoadOptions).
	OutsideAllowed func(path string) bool
	// AllowImages says the active model and transport can carry image
	// blocks. When false, image refs are reported as not attached.
	AllowImages bool
	// ImageRejection is the user-facing reason used when AllowImages is
	// false. Empty means a generic default.
	ImageRejection string
	// Timeout bounds the whole load (zero = DefaultLoadTimeout).
	Timeout time.Duration
}

// Prepared is the result of resolving the @ references in one message.
type Prepared struct {
	// Text is the message with the leading '@' stripped from every
	// successfully loaded reference. Use it where the user's words are
	// needed without file bodies (checkpoint labels, retrieval scoring).
	Text string
	// Content is Text plus the attachment block — what the model sees.
	Content string
	// Images are the decoded image attachments to send with the message.
	Images []Image
	// Refs are all parsed references with their load outcome.
	Refs []Ref
}

// Prepare parses input for @ references, loads them (bounded by a
// timeout), and builds the model-facing message. Input without
// references comes back unchanged with no Refs.
func Prepare(input string, o PrepareOptions) Prepared {
	p := Prepared{Text: input, Content: input}
	refs := Parse(input)
	if len(refs) == 0 {
		return p
	}
	refs = LoadTimeout(refs, o.Cwd, LoadOptions{AllowOutside: o.AllowOutside, OutsideAllowed: o.OutsideAllowed}, o.Timeout)
	if !o.AllowImages {
		reason := o.ImageRejection
		if reason == "" {
			reason = "image input is not available here"
		}
		refs = RejectImages(refs, reason)
	}
	p.Refs = refs
	p.Images = Images(refs)
	p.Text = Rewrite(input, refs)
	p.Content = Attach(p.Text, refs)
	return p
}

// Attach appends the attachment block for refs to a user message. With
// no refs it returns content unchanged. The block always goes last so
// StripAttachment can recover everything the user (and any reminders)
// wrote before it.
func Attach(content string, refs []Ref) string {
	section := BuildSection(refs)
	if section == "" {
		return content
	}
	if content == "" {
		return section
	}
	return content + "\n\n" + section
}

// StripAttachment returns content without a trailing attachment block
// (and without the blank-line separator that preceded it). Content with
// no block is returned unchanged. It is how transcripts and resumed
// sessions show only what the user typed.
func StripAttachment(content string) string {
	idx := strings.Index(content, Marker)
	if idx < 0 {
		return content
	}
	return strings.TrimRight(content[:idx], " \t\r\n")
}

// StripSection removes a block injected into a system prompt by older
// versions of yottacode, so resuming such a session starts clean.
func StripSection(prompt string) string { return StripAttachment(prompt) }

// Describe renders a one-line, user-facing status for a ref ("attached
// …" or "could not attach …") and whether it is an error.
func Describe(r Ref) (text string, isErr bool) {
	if !r.Loaded {
		return fmt.Sprintf("could not attach %s: %s", r.Token, r.Error), true
	}
	where := ""
	if r.Outside {
		where = " · outside project"
	}
	switch {
	case r.IsDir:
		return fmt.Sprintf("attached %s (directory, %d entries%s)", r.Token, dirEntryCount(r.Content), where), false
	case r.IsImage:
		note := ""
		if r.Note != "" {
			note = " · " + r.Note
		}
		return fmt.Sprintf("attached %s (image, %s%s%s)", r.Token, humanBytes(r.Size), where, note), false
	default:
		return fmt.Sprintf("attached %s (%s%s)", r.Token, humanBytes(r.Size), where), false
	}
}

// dirEntryCount counts the entries in a directory listing produced by
// Load (one per line after the "(directory listing)" header, plus a
// possible truncation note).
func dirEntryCount(listing string) int {
	n := 0
	for _, line := range strings.Split(listing, "\n") {
		if line == "" || line == "(directory listing)" || strings.HasPrefix(line, "...") {
			continue
		}
		n++
	}
	return n
}
