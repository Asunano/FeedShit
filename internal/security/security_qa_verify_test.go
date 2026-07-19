package security

// Independent QA verification tests (Phase 0+1) — authored by QA. Focuses on the
// fail-fast master-key length validation via the environment path used by main.

import (
	"os"
	"testing"
)

// Point #3: Init() must reject a raw master key that is neither 32 bytes nor 64
// hex chars, so the process fails fast instead of running with a weak key.
func TestQAVerifyInitRejectsWrongLengthRawKey(t *testing.T) {
	old, had := os.LookupEnv("FEEDSHIT_MASTER_KEY")
	// 16-char raw value: not 32 bytes, not 64 hex -> must fail.
	os.Setenv("FEEDSHIT_MASTER_KEY", "shortkey1234567")
	defer func() {
		if had {
			os.Setenv("FEEDSHIT_MASTER_KEY", old)
		} else {
			os.Unsetenv("FEEDSHIT_MASTER_KEY")
		}
	}()
	if err := Init(); err == nil {
		t.Fatal("expected Init() to fail on a 16-byte raw master key")
	}
}
