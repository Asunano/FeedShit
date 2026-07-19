package app

import (
	"strings"
	"testing"
)

func TestMaskSecret(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"ab", "****"},
		{"abc", "****"},
		{"abcd", "****"},
		{"abcde", "ab****de"},
		{"supersecretvalue", "su****ue"},
	}
	for _, c := range cases {
		if got := maskSecret(c.in); got != c.want {
			t.Fatalf("maskSecret(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestWebhookBackoff(t *testing.T) {
	cases := []struct {
		attempts int
		want     int64
	}{
		{0, 30},
		{1, 60},
		{2, 120},
		{6, 1920},
		{7, 3600}, // capped
		{20, 3600}, // capped
	}
	for _, c := range cases {
		if got := webhookBackoff(c.attempts); got != c.want {
			t.Fatalf("webhookBackoff(%d) = %d, want %d", c.attempts, got, c.want)
		}
	}
}

func TestSanitizeSVG(t *testing.T) {
	// Script blocks are removed.
	if out := sanitizeSVG(`<svg><script>alert(1)</script></svg>`); strings.Contains(out, "script") {
		t.Fatalf("script not removed: %q", out)
	}
	// Inline event handlers are removed.
	if out := sanitizeSVG(`<svg onload="alert(1)" onerror="x()"></svg>`); strings.Contains(out, "onload") || strings.Contains(out, "onerror") {
		t.Fatalf("event handlers not removed: %q", out)
	}
	// javascript: URLs are neutralized.
	if out := sanitizeSVG(`<a href="javascript:alert(1)">x</a>`); !strings.Contains(out, `data-removed-href="`) {
		t.Fatalf("javascript: URL not neutralized: %q", out)
	}
	if out := sanitizeSVG(`<svg><rect/></svg>`); !strings.Contains(out, "<svg>") {
		t.Fatalf("valid svg content should be preserved: %q", out)
	}
}

func TestValidateFileContent(t *testing.T) {
	cases := []struct {
		header   []byte
		ext      string
		expected bool
	}{
		{[]byte{0x89, 0x50, 0x4E, 0x47}, ".png", true},
		{[]byte{0x00, 0x01, 0x02}, ".png", false},
		{[]byte{0xFF, 0xD8, 0xFF}, ".jpg", true},
		{[]byte{0xFF, 0xD9}, ".jpeg", false},
		{[]byte("GIF87a"), ".gif", true},
		{[]byte("GIF89a"), ".gif", true},
		{[]byte("RIFFxxxxWEBP"), ".webp", true},
		{[]byte{0x42, 0x4D, 0x00}, ".bmp", true},
		{[]byte("<svg xmlns='http://www.w3.org/2000/svg'></svg>"), ".svg", true},
		{[]byte("<?xml version='1.0'?><svg></svg>"), ".svg", true},
		{[]byte("<html></html>"), ".svg", false},
		{[]byte("{ \"a\": 1 }"), ".json", true},
		{[]byte("[1,2,3]"), ".json", true},
		{[]byte("not json"), ".json", false},
		{[]byte("just text"), ".txt", true},
		{[]byte("just text"), ".log", true},
		{[]byte("#,a,b\n1,2,3"), ".csv", true},
		{[]byte{0x00}, ".xyz", false}, // unknown extension
	}
	for _, c := range cases {
		if got := validateFileContent(c.header, c.ext); got != c.expected {
			t.Fatalf("validateFileContent(%q ext=%s) = %v, want %v", c.header, c.ext, got, c.expected)
		}
	}
}
