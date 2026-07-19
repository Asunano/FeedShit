package database

// M5 duplicate detection — fingerprint pure function regression tests.
//
// Acceptance item 1:
//   指纹确定性：normalizeContent 去空白+小写+去 Unicode 标点+折叠多空白；
//   "Hello World!" 和 "   hello, world!!   " 得相同 SHA-256；
//   空内容归一化后得确定值；
//   ComputeContentHash("title","body") == ComputeContentHashFromText("title body")（一致性契约 §3.2）。

import (
	"encoding/hex"
	"strings"
	"testing"
)

// ---------- normalizeContent ----------

func TestNormalizeContentTrimsAndLowercases(t *testing.T) {
	got := normalizeContent("  Hello World  ")
	want := "hello world"
	if got != want {
		t.Fatalf("normalizeContent(%q) = %q, want %q", "  Hello World  ", got, want)
	}
}

func TestNormalizeContentRemovesPunctuation(t *testing.T) {
	got := normalizeContent("Hello, World!!! How's it going?")
	// Commas, exclamation marks, apostrophes, question marks removed.
	// "How's" → "hows" (apostrophe removed)
	if strings.Contains(got, ",") {
		t.Fatalf("normalizeContent still contains comma: %q", got)
	}
	if strings.Contains(got, "!") {
		t.Fatalf("normalizeContent still contains exclamation: %q", got)
	}
	if strings.Contains(got, "'") {
		t.Fatalf("normalizeContent still contains apostrophe: %q", got)
	}
	if strings.Contains(got, "?") {
		t.Fatalf("normalizeContent still contains question mark: %q", got)
	}
	// Verify the expected cleaned result
	want := "hello world hows it going"
	if got != want {
		t.Fatalf("normalizeContent(%q) = %q, want %q",
			"Hello, World!!! How's it going?", got, want)
	}
}

func TestNormalizeContentFoldsMultipleWhitespace(t *testing.T) {
	got := normalizeContent("hello    world   foo  bar")
	want := "hello world foo bar"
	if got != want {
		t.Fatalf("normalizeContent(%q) = %q, want %q", "hello    world   foo  bar", got, want)
	}
}

func TestNormalizeContentTabsAndNewlines(t *testing.T) {
	got := normalizeContent("hello\tworld\nfoo  bar")
	want := "hello world foo bar"
	if got != want {
		t.Fatalf("normalizeContent with tabs/newlines = %q, want %q", got, want)
	}
}

func TestNormalizeContentEmpty(t *testing.T) {
	got := normalizeContent("")
	if got != "" {
		t.Fatalf("normalizeContent('') = %q, want ''", got)
	}
}

func TestNormalizeContentOnlyWhitespace(t *testing.T) {
	got := normalizeContent("   \t\n  ")
	if got != "" {
		t.Fatalf("normalizeContent(whitespace only) = %q, want ''", got)
	}
}

func TestNormalizeContentOnlyPunctuation(t *testing.T) {
	got := normalizeContent("!!!???!!!")
	if got != "" {
		t.Fatalf("normalizeContent(punctuation only) = %q, want ''", got)
	}
}

func TestNormalizeContentUnicodePunctuation(t *testing.T) {
	// Chinese/Japanese punctuation: full-width comma, full-width period, etc.
	got := normalizeContent("Hello，World．test…")
	// The full-width comma and period and ellipsis are all punctuation.
	if strings.ContainsAny(got, "，．…") {
		t.Fatalf("normalizeContent still contains unicode punctuation: %q", got)
	}
	// Punctuation is stripped without inserting spaces, so "Hello，World" → "helloworld"
	want := "helloworldtest"
	if got != want {
		t.Fatalf("normalizeContent(unicode punct) = %q, want %q", got, want)
	}
}

// ---------- ComputeContentHash determinism ----------

func TestComputeContentHashDeterminism(t *testing.T) {
	// Same content, same hash (determinism)
	h1 := ComputeContentHash("Hello World!", "")
	h2 := ComputeContentHash("Hello World!", "")
	if h1 != h2 {
		t.Fatalf("same input produced different hashes: %q vs %q", h1, h2)
	}
}

func TestComputeContentHashNormalizedEquivalence(t *testing.T) {
	// "Hello World!" and "   hello, world!!   " must produce the same hash.
	h1 := ComputeContentHash("Hello World!", "")
	h2 := ComputeContentHash("   hello, world!!   ", "")
	if h1 != h2 {
		t.Fatalf("'Hello World!' and '   hello, world!!   ' must hash the same\n  h1=%q\n  h2=%q", h1, h2)
	}
}

func TestComputeContentHashLength(t *testing.T) {
	h := ComputeContentHash("anything", "")
	if len(h) != 64 {
		t.Fatalf("hash length = %d, want 64 (SHA-256 hex)", len(h))
	}
	if _, err := hex.DecodeString(h); err != nil {
		t.Fatalf("hash is not valid hex: %q, err=%v", h, err)
	}
}

func TestComputeContentHashEmpty(t *testing.T) {
	// Empty content produces a deterministic (non-empty) value.
	h := ComputeContentHash("", "")
	if h == "" {
		t.Fatal("empty content should produce non-empty hash")
	}
	if len(h) != 64 {
		t.Fatalf("empty content hash length = %d, want 64", len(h))
	}
	// Deterministic
	h2 := ComputeContentHash("", "")
	if h != h2 {
		t.Fatalf("empty content not deterministic: %q vs %q", h, h2)
	}
}

func TestComputeContentHashDifferentContent(t *testing.T) {
	h1 := ComputeContentHash("hello", "world")
	h2 := ComputeContentHash("goodbye", "world")
	if h1 == h2 {
		t.Fatal("different content should NOT produce same hash")
	}
}

// ---------- ComputeContentHashFromText ----------

func TestComputeContentHashFromTextLength(t *testing.T) {
	h := ComputeContentHashFromText("hello world")
	if len(h) != 64 {
		t.Fatalf("hash length = %d, want 64", len(h))
	}
}

func TestComputeContentHashFromTextEmpty(t *testing.T) {
	h := ComputeContentHashFromText("")
	if h == "" {
		t.Fatal("empty text should produce non-empty hash")
	}
	if len(h) != 64 {
		t.Fatalf("empty text hash length = %d, want 64", len(h))
	}
}

// ---------- Consistency contract (§3.2) ----------

// ComputeContentHash("title","body") must equal ComputeContentHashFromText("title body").
func TestConsistencyContractTitleBody(t *testing.T) {
	title := "Hello World!"
	body := "This is the description."
	hStore := ComputeContentHash(title, body)
	hQuery := ComputeContentHashFromText(title + " " + body)
	if hStore != hQuery {
		t.Fatalf("consistency contract violated:\n  ComputeContentHash(%q,%q) = %q\n  ComputeContentHashFromText(%q) = %q",
			title, body, hStore, title+" "+body, hQuery)
	}
}

// When body is empty, ComputeContentHash must NOT append a trailing space,
// so the query side must use just the title (no space appended).
func TestConsistencyContractEmptyBody(t *testing.T) {
	title := "Just a title"
	hStore := ComputeContentHash(title, "")
	// No trailing space because body is empty.
	hQuery := ComputeContentHashFromText(title)
	if hStore != hQuery {
		t.Fatalf("consistency contract violated for empty body:\n  ComputeContentHash(%q,'') = %q\n  ComputeContentHashFromText(%q) = %q",
			title, hStore, title, hQuery)
	}
}

// With punctuation/whitespace differences, consistency must still hold.
func TestConsistencyContractNormalizedVariants(t *testing.T) {
	title := "  Hello, World!!  "
	body := "   This is   the description...   "
	hStore := ComputeContentHash(title, body)
	hQuery := ComputeContentHashFromText(title + " " + body)
	if hStore != hQuery {
		t.Fatalf("consistency contract violated with whitespace/punctuation:\n  hStore=%q\n  hQuery=%q", hStore, hQuery)
	}
}

// Different content must produce different hashes via both functions.
func TestConsistencyContractDifferentContent(t *testing.T) {
	a1 := ComputeContentHash("foo", "bar")
	a2 := ComputeContentHashFromText("foo bar")
	b1 := ComputeContentHash("baz", "qux")
	b2 := ComputeContentHashFromText("baz qux")
	if a1 != a2 {
		t.Fatalf("self-inconsistency for 'foo bar': %q vs %q", a1, a2)
	}
	if b1 != b2 {
		t.Fatalf("self-inconsistency for 'baz qux': %q vs %q", b1, b2)
	}
	if a1 == b1 {
		t.Fatal("different content must produce different hashes")
	}
}

// Unicode content consistency (CJK characters)
func TestConsistencyContractUnicode(t *testing.T) {
	title := "报告一个问题"
	body := "登录页面无法打开"
	hStore := ComputeContentHash(title, body)
	hQuery := ComputeContentHashFromText(title + " " + body)
	if hStore != hQuery {
		t.Fatalf("consistency contract violated for CJK:\n  hStore=%q\n  hQuery=%q", hStore, hQuery)
	}
}
