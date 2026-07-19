// Package security provides AES-GCM encryption for sensitive data at rest.
//
// The master key is loaded once at process startup from the FEEDSHIT_MASTER_KEY
// environment variable and must be exactly 32 bytes (or 64 hex characters). It
// is never serialized and never leaves the process. Only the Go standard
// library is used — no third-party dependencies.
//
// Ciphertext format: "aes-gcm:<base64(nonce(12B)|ciphertext)>".
// Decrypting with the wrong key (or tampered data) fails authentication.
package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// Prefix is the fixed prefix for ciphertexts produced by this package. A stored
// value is considered encrypted iff it begins with this prefix.
const Prefix = "aes-gcm:"

// masterKey holds the 32-byte AES key loaded at startup. It is set by Init
// (production) or InitWithKey (tests) and must never be serialized.
var masterKey []byte

// Init loads the master key from the FEEDSHIT_MASTER_KEY environment variable.
// The value must be exactly 32 raw bytes, or 64 hex characters decoding to 32
// bytes. It returns an error if the variable is missing or malformed, and the
// caller MUST fail fast (the application cannot safely decrypt secrets).
func Init() error {
	raw := os.Getenv("FEEDSHIT_MASTER_KEY")
	if raw == "" {
		return fmt.Errorf("FEEDSHIT_MASTER_KEY is not set")
	}

	// Accept 64 hex characters -> 32 bytes.
	if len(raw) == 64 {
		if decoded, err := hex.DecodeString(raw); err == nil && len(decoded) == 32 {
			return setMasterKey(decoded)
		}
	}

	// Otherwise require exactly 32 raw bytes.
	if len(raw) != 32 {
		return fmt.Errorf("FEEDSHIT_MASTER_KEY must be 32 bytes (raw) or 64 hex chars, got %d bytes", len(raw))
	}
	return setMasterKey([]byte(raw))
}

// InitWithKey sets the master key directly. It exists for tests and other
// controlled environments where injecting the key via environment variable is
// inconvenient. Production code must use Init().
func InitWithKey(key []byte) error {
	return setMasterKey(key)
}

// setMasterKey validates and stores the master key as a private copy.
func setMasterKey(key []byte) error {
	if len(key) != 32 {
		return fmt.Errorf("master key must be 32 bytes, got %d", len(key))
	}
	k := make([]byte, 32)
	copy(k, key)
	masterKey = k
	return nil
}

// Encrypt encrypts plaintext with the given 32-byte key using AES-GCM.
// The result is formatted as "aes-gcm:<base64(nonce(12B)|ciphertext)>".
func Encrypt(plaintext []byte, key []byte) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aes new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("aes new gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize()) // 12 bytes for AES-GCM
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	// gcm.Seal appends the ciphertext to the nonce slice, yielding nonce||ciphertext.
	sealed := gcm.Seal(nonce, nonce, plaintext, nil)
	return Prefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt. It returns an error on a malformed token or when
// the key does not match (authentication failure / tampering).
func Decrypt(token string, key []byte) ([]byte, error) {
	if !strings.HasPrefix(token, Prefix) {
		return nil, fmt.Errorf("invalid ciphertext: missing %q prefix", Prefix)
	}
	raw, err := base64.StdEncoding.DecodeString(token[len(Prefix):])
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	const nonceSize = 12
	if len(raw) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := raw[:nonceSize], raw[nonceSize:]
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("aes new gcm: %w", err)
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt (wrong key or tampered data): %w", err)
	}
	return plaintext, nil
}

// EncryptWithMaster encrypts a string with the process master key.
func EncryptWithMaster(plaintext string) (string, error) {
	if len(masterKey) != 32 {
		return "", fmt.Errorf("master key not initialized")
	}
	return Encrypt([]byte(plaintext), masterKey)
}

// DecryptWithMaster decrypts a string produced by EncryptWithMaster.
func DecryptWithMaster(token string) (string, error) {
	if len(masterKey) != 32 {
		return "", fmt.Errorf("master key not initialized")
	}
	b, err := Decrypt(token, masterKey)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// IsEncrypted reports whether s looks like a ciphertext produced by this package.
func IsEncrypted(s string) bool {
	return strings.HasPrefix(s, Prefix)
}

// EncryptFile encrypts an entire file using the master key and writes to dst.
// The output format: [12 bytes nonce][GCM ciphertext + 16 byte auth tag].
// Returns error if master key is not initialized.
func EncryptFile(src, dst string) error {
	if len(masterKey) != 32 {
		return fmt.Errorf("master key not initialized")
	}
	plaintext, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return fmt.Errorf("aes new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("aes new gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("generate nonce: %w", err)
	}
	// Seal appends ciphertext+tag after nonce, yielding nonce||ciphertext||tag.
	out := gcm.Seal(nonce, nonce, plaintext, nil)
	if err := os.WriteFile(dst, out, 0400); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}

// DecryptFile decrypts a file produced by EncryptFile using the master key.
// Format: [12 bytes nonce][GCM ciphertext + 16 byte auth tag].
func DecryptFile(src, dst string) error {
	if len(masterKey) != 32 {
		return fmt.Errorf("master key not initialized")
	}
	encrypted, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	const nonceSize = 12
	if len(encrypted) < nonceSize {
		return fmt.Errorf("encrypted file too short")
	}
	nonce, ciphertext := encrypted[:nonceSize], encrypted[nonceSize:]
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return fmt.Errorf("aes new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("aes new gcm: %w", err)
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return fmt.Errorf("decrypt (wrong key or tampered data): %w", err)
	}
	if err := os.WriteFile(dst, plaintext, 0600); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}
