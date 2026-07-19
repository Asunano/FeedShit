package security

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// testKey is a fixed 32-byte AES key used across tests.
var testKey = bytes.Repeat([]byte{0x42}, 32)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	plaintext := []byte("smtp-password-12345")
	token, err := Encrypt(plaintext, testKey)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}
	if !strings.HasPrefix(token, Prefix) {
		t.Fatalf("ciphertext missing prefix: %q", token)
	}
	decrypted, err := Decrypt(token, testKey)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("round-trip mismatch: got %q want %q", decrypted, plaintext)
	}
}

func TestEncryptWithMasterRequiresInit(t *testing.T) {
	// Reset master key by initializing with an invalid-length placeholder is not
	// possible via public API; rely on the fact that a fresh process has no key.
	// We unset the env and call Init (which must fail) to ensure masterKey stays
	// unset for this check.
	old, had := os.LookupEnv("FEEDSHIT_MASTER_KEY")
	os.Unsetenv("FEEDSHIT_MASTER_KEY")
	defer func() {
		if had {
			os.Setenv("FEEDSHIT_MASTER_KEY", old)
		}
	}()
	// Best-effort: if a prior test initialized the key, skip this assertion.
	if err := Init(); err != nil {
		// Key not initialized via env -> masterKey should be empty.
		if _, e := EncryptWithMaster("x"); e == nil {
			t.Fatal("EncryptWithMaster should fail when master key is not initialized")
		}
	}
}

func TestEncryptWithMasterDecryptWithMaster(t *testing.T) {
	if err := InitWithKey(testKey); err != nil {
		t.Fatalf("InitWithKey failed: %v", err)
	}
	token, err := EncryptWithMaster("topsecret-webhook-secret")
	if err != nil {
		t.Fatalf("EncryptWithMaster failed: %v", err)
	}
	plain, err := DecryptWithMaster(token)
	if err != nil {
		t.Fatalf("DecryptWithMaster failed: %v", err)
	}
	if plain != "topsecret-webhook-secret" {
		t.Fatalf("round-trip mismatch: got %q", plain)
	}
}

func TestIsEncrypted(t *testing.T) {
	if !IsEncrypted(Prefix + "abc") {
		t.Fatal("expected IsEncrypted=true for prefixed token")
	}
	if IsEncrypted("plain-text-value") {
		t.Fatal("expected IsEncrypted=false for plaintext")
	}
	if IsEncrypted("") {
		t.Fatal("expected IsEncrypted=false for empty string")
	}
}

func TestDecryptTamperFails(t *testing.T) {
	token, err := Encrypt([]byte("payload"), testKey)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}
	// Flip a byte in the base64 body.
	body := []byte(token)
	body[len(body)-1] ^= 0x01
	if _, err := Decrypt(string(body), testKey); err == nil {
		t.Fatal("expected decryption to fail on tampered ciphertext")
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	token, err := Encrypt([]byte("payload"), testKey)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}
	wrongKey := bytes.Repeat([]byte{0x99}, 32)
	if _, err := Decrypt(token, wrongKey); err == nil {
		t.Fatal("expected decryption to fail with wrong key")
	}
}

func TestInitWithKeyRejectsShortKey(t *testing.T) {
	if err := InitWithKey([]byte("tooshort")); err == nil {
		t.Fatal("expected error for non-32-byte key")
	}
}

func TestInitMissingEnvFails(t *testing.T) {
	old, had := os.LookupEnv("FEEDSHIT_MASTER_KEY")
	os.Unsetenv("FEEDSHIT_MASTER_KEY")
	defer func() {
		if had {
			os.Setenv("FEEDSHIT_MASTER_KEY", old)
		}
	}()
	if err := Init(); err == nil {
		t.Fatal("expected error when FEEDSHIT_MASTER_KEY is unset")
	}
}
