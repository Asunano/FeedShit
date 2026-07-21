package app

import (
	"strings"

	"github.com/microcosm-cc/bluemonday"
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
