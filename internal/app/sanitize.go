package app

import (
	"bytes"
	"html"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// announcementPolicy sanitizes admin-authored announcement HTML.
// UGCPolicy() allows a safe subset (links, emphasis, lists, paragraphs, etc.)
// while stripping scripts, event handlers, and javascript: URLs — preventing
// stored XSS through the announcement feature.
var announcementPolicy = bluemonday.UGCPolicy()

// SanitizeHTML returns a safe HTML string for admin-authored announcement content.
func SanitizeHTML(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	return announcementPolicy.Sanitize(raw)
}

// markdownPolicy renders GitHub-flavored Markdown (tables, strikethrough,
// task lists, autolinks) for FAQ answers. The output is always passed through
// announcementPolicy (UGCPolicy) so a maliciously crafted Markdown document
// cannot inject scripts, event handlers, or javascript: URLs.
var markdownPolicy = goldmark.New(goldmark.WithExtensions(extension.GFM))

// RenderMarkdown converts a Markdown string into sanitized HTML suitable for
// direct innerHTML injection. Empty input yields an empty string. On a parse
// failure it falls back to sanitized plain text so the caller always receives
// safe output.
func RenderMarkdown(md string) string {
	if strings.TrimSpace(md) == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := markdownPolicy.Convert([]byte(md), &buf); err != nil {
		return announcementPolicy.Sanitize("<pre>" + html.EscapeString(md) + "</pre>")
	}
	return announcementPolicy.Sanitize(buf.String())
}
