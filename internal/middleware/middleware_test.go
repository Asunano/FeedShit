package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"
)

// solvePoW brute-forces a valid nonce for the given parameters (test helper).
func solvePoW(projectID, timestamp string, difficulty int) string {
	prefix := strings.Repeat("0", difficulty)
	for i := 0; i < 5_000_000; i++ {
		nonce := strconv.Itoa(i)
		h := sha256.Sum256([]byte(projectID + timestamp + nonce))
		if hex.EncodeToString(h[:])[:difficulty] == prefix {
			return nonce
		}
	}
	return ""
}

func TestVerifyPoW(t *testing.T) {
	proj := "test-proj"
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := solvePoW(proj, ts, 2)
	if nonce == "" {
		t.Fatal("could not solve PoW within budget")
	}
	if !VerifyPoW(proj, ts, nonce, 2) {
		t.Fatal("valid PoW was rejected")
	}
	// Wrong nonce must be rejected.
	if VerifyPoW(proj, ts, nonce+"x", 2) {
		t.Fatal("invalid PoW was accepted")
	}
	// Expired timestamp must be rejected.
	old := strconv.FormatInt(time.Now().Unix()-400, 10)
	if VerifyPoW(proj, old, solvePoW(proj, old, 2), 2) {
		t.Fatal("expired timestamp was accepted")
	}
	// Future timestamp must be rejected.
	fut := strconv.FormatInt(time.Now().Unix()+400, 10)
	if VerifyPoW(proj, fut, solvePoW(proj, fut, 2), 2) {
		t.Fatal("future timestamp was accepted")
	}
	// Malformed timestamp must be rejected.
	if VerifyPoW(proj, "not-a-number", nonce, 2) {
		t.Fatal("malformed timestamp was accepted")
	}
}

func TestSecureCompare(t *testing.T) {
	if !SecureCompare("abc", "abc") {
		t.Fatal("equal strings should compare equal")
	}
	if SecureCompare("abc", "abd") {
		t.Fatal("different strings should not compare equal")
	}
	if SecureCompare("abc", "abcd") {
		t.Fatal("different-length strings should not compare equal")
	}
}

func TestFormatSize(t *testing.T) {
	cases := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
	}
	for _, c := range cases {
		if got := FormatSize(c.bytes); got != c.want {
			t.Fatalf("FormatSize(%d) = %q, want %q", c.bytes, got, c.want)
		}
	}
}

func TestIsAPIRoute(t *testing.T) {
	if !isAPIRoute("/api/v1/feedback") {
		t.Fatal("/api/v1/feedback should be an API route")
	}
	if isAPIRoute("/admin/login") {
		t.Fatal("/admin/login should not be an API route")
	}
	if isAPIRoute("/api") {
		t.Fatal("bare /api without trailing slash should not match prefix /api/")
	}
}

func TestSessionManagerLifecycle(t *testing.T) {
	sm := NewSessionManager()
	token := sm.Create("alice", "admin")
	if token == "" {
		t.Fatal("Create returned empty token")
	}
	user, role, ok := sm.Validate(token)
	if !ok || user != "alice" || role != "admin" {
		t.Fatalf("Validate failed: ok=%v user=%q role=%q", ok, user, role)
	}
	sm.Revoke(token)
	if _, _, ok := sm.Validate(token); ok {
		t.Fatal("Validate should fail after Revoke")
	}
}
