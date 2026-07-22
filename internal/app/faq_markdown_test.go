package app

import (
	"strings"
	"testing"
)

// TestRenderMarkdownStripsXSS verifies that Markdown is rendered to HTML but
// any script / event-handler / javascript: payload is stripped by bluemonday,
// while legitimate Markdown (bold, safe links) survives.
func TestRenderMarkdownStripsXSS(t *testing.T) {
	out := RenderMarkdown("<script>alert(1)</script>\n\n**bold** and [ok](https://example.com)")
	if strings.Contains(out, "<script") {
		t.Fatalf("script tag not stripped: %s", out)
	}
	if strings.Contains(out, "javascript:") {
		t.Fatalf("javascript: URL not stripped: %s", out)
	}
	if !strings.Contains(out, "<strong>bold</strong>") && !strings.Contains(out, "<b>bold</b>") {
		t.Fatalf("bold not rendered: %s", out)
	}
	if !strings.Contains(out, `<a href="https://example.com"`) {
		t.Fatalf("safe link not rendered: %s", out)
	}
}

// TestRenderMarkdownStripsImgOnerror confirms inline event handlers are removed.
func TestRenderMarkdownStripsImgOnerror(t *testing.T) {
	out := RenderMarkdown(`<img src=x onerror=alert(1)>`)
	if strings.Contains(out, "onerror") {
		t.Fatalf("onerror attribute not stripped: %s", out)
	}
}

// TestRenderMarkdownEmpty confirms empty / whitespace input yields empty output.
func TestRenderMarkdownEmpty(t *testing.T) {
	if RenderMarkdown("") != "" {
		t.Fatal("empty input should yield empty output")
	}
	if RenderMarkdown("   ") != "" {
		t.Fatal("whitespace input should yield empty output")
	}
}
