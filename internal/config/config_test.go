package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoadConfigDefaults(t *testing.T) {
	// Clear env vars that might interfere
	for _, k := range []string{"PORT", "ADMIN_USERNAME", "ADMIN_PASSWORD", "DATA_DIR",
		"SMTP_HOST", "SMTP_PORT", "NOTIFY_ENABLE", "BASE_URL", "POW_DIFFICULTY",
		"RATE_LIMIT_PER_HOUR", "MAX_UPLOAD_MB", "TRUSTED_PROXIES",
		"API_TOKEN_DEFAULT_RATE_LIMIT", "BACKUP_RETENTION_DAYS"} {
		os.Unsetenv(k)
	}

	cfg := LoadConfig()

	assert.Equal(t, "8080", cfg.Port)
	assert.Equal(t, "admin", cfg.AdminUsername)
	assert.Equal(t, "", cfg.AdminPassword)
	assert.Equal(t, "./data", cfg.DataDir)
	if cfg.UploadDir != "./data/uploads" {
		t.Fatalf("UploadDir = %q, want ./data/uploads", cfg.UploadDir)
	}
	if cfg.DBPath != "./data/feedbacks.db" {
		t.Fatalf("DBPath = %q, want ./data/feedbacks.db", cfg.DBPath)
	}
	if cfg.SMTPPort != 587 {
		t.Fatalf("SMTPPort = %d, want 587", cfg.SMTPPort)
	}
	if cfg.NotifyEnable {
		t.Fatal("NotifyEnable should be false by default")
	}
	if cfg.BaseURL != "http://localhost:8080" {
		t.Fatalf("BaseURL = %q, want http://localhost:8080", cfg.BaseURL)
	}
	if cfg.PoWDifficulty != 4 {
		t.Fatalf("PoWDifficulty = %d, want 4", cfg.PoWDifficulty)
	}
	if cfg.MaxUploadSize != 20*1024*1024 {
		t.Fatalf("MaxUploadSize = %d, want %d", cfg.MaxUploadSize, 20*1024*1024)
	}
	if cfg.APITokenDefaultRateLimit != 60 {
		t.Fatalf("APITokenDefaultRateLimit = %d, want 60", cfg.APITokenDefaultRateLimit)
	}
	if cfg.BackupRetentionDays != 30 {
		t.Fatalf("BackupRetentionDays = %d, want 30", cfg.BackupRetentionDays)
	}
	if len(cfg.TrustedProxies) != 0 {
		t.Fatalf("TrustedProxies = %v, want empty", cfg.TrustedProxies)
	}
}

func TestLoadConfigCustomEnv(t *testing.T) {
	os.Setenv("PORT", "9090")
	os.Setenv("ADMIN_USERNAME", "myadmin")
	os.Setenv("ADMIN_PASSWORD", "secret123")
	os.Setenv("DATA_DIR", "/custom/data")
	os.Setenv("SMTP_HOST", "smtp.example.com")
	os.Setenv("SMTP_PORT", "465")
	os.Setenv("NOTIFY_ENABLE", "true")
	os.Setenv("BASE_URL", "https://feedback.example.com")
	os.Setenv("POW_DIFFICULTY", "5")
	os.Setenv("RATE_LIMIT_PER_HOUR", "20")
	os.Setenv("MAX_UPLOAD_MB", "50")
	os.Setenv("API_TOKEN_DEFAULT_RATE_LIMIT", "100")
	os.Setenv("BACKUP_RETENTION_DAYS", "90")
	os.Setenv("TRUSTED_PROXIES", "10.0.0.1, 10.0.0.2")
	t.Cleanup(func() {
		for _, k := range []string{"PORT", "ADMIN_USERNAME", "ADMIN_PASSWORD", "DATA_DIR",
			"SMTP_HOST", "SMTP_PORT", "NOTIFY_ENABLE", "BASE_URL", "POW_DIFFICULTY",
			"RATE_LIMIT_PER_HOUR", "MAX_UPLOAD_MB", "API_TOKEN_DEFAULT_RATE_LIMIT",
			"BACKUP_RETENTION_DAYS", "TRUSTED_PROXIES"} {
			os.Unsetenv(k)
		}
	})

	cfg := LoadConfig()

	if cfg.Port != "9090" {
		t.Fatalf("Port = %q, want 9090", cfg.Port)
	}
	if cfg.AdminUsername != "myadmin" {
		t.Fatalf("AdminUsername = %q, want myadmin", cfg.AdminUsername)
	}
	if cfg.AdminPassword != "secret123" {
		t.Fatalf("AdminPassword = %q, want secret123", cfg.AdminPassword)
	}
	if cfg.DataDir != "/custom/data" {
		t.Fatalf("DataDir = %q, want /custom/data", cfg.DataDir)
	}
	if cfg.UploadDir != "/custom/data/uploads" {
		t.Fatalf("UploadDir = %q, want /custom/data/uploads", cfg.UploadDir)
	}
	if cfg.DBPath != "/custom/data/feedbacks.db" {
		t.Fatalf("DBPath = %q, want /custom/data/feedbacks.db", cfg.DBPath)
	}
	if cfg.SMTPHost != "smtp.example.com" {
		t.Fatalf("SMTPHost = %q, want smtp.example.com", cfg.SMTPHost)
	}
	if cfg.SMTPPort != 465 {
		t.Fatalf("SMTPPort = %d, want 465", cfg.SMTPPort)
	}
	if !cfg.NotifyEnable {
		t.Fatal("NotifyEnable should be true")
	}
	if cfg.BaseURL != "https://feedback.example.com" {
		t.Fatalf("BaseURL = %q, want https://feedback.example.com", cfg.BaseURL)
	}
	if cfg.PoWDifficulty != 5 {
		t.Fatalf("PoWDifficulty = %d, want 5", cfg.PoWDifficulty)
	}
	if cfg.MaxUploadSize != 50*1024*1024 {
		t.Fatalf("MaxUploadSize = %d, want %d", cfg.MaxUploadSize, 50*1024*1024)
	}
	if cfg.APITokenDefaultRateLimit != 100 {
		t.Fatalf("APITokenDefaultRateLimit = %d, want 100", cfg.APITokenDefaultRateLimit)
	}
	if cfg.BackupRetentionDays != 90 {
		t.Fatalf("BackupRetentionDays = %d, want 90", cfg.BackupRetentionDays)
	}
	if len(cfg.TrustedProxies) != 2 || cfg.TrustedProxies[0] != "10.0.0.1" || cfg.TrustedProxies[1] != "10.0.0.2" {
		t.Fatalf("TrustedProxies = %v, want [10.0.0.1 10.0.0.2]", cfg.TrustedProxies)
	}
}

func TestGetEnv(t *testing.T) {
	os.Unsetenv("TEST_GETENV_KEY")
	if v := getEnv("TEST_GETENV_KEY", "fallback"); v != "fallback" {
		t.Fatalf("got %q, want fallback", v)
	}
	os.Setenv("TEST_GETENV_KEY", "custom")
	t.Cleanup(func() { os.Unsetenv("TEST_GETENV_KEY") })
	if v := getEnv("TEST_GETENV_KEY", "fallback"); v != "custom" {
		t.Fatalf("got %q, want custom", v)
	}
}

func TestGetEnvInt(t *testing.T) {
	os.Unsetenv("TEST_INT_KEY")
	if v := getEnvInt("TEST_INT_KEY", 42); v != 42 {
		t.Fatalf("got %d, want 42", v)
	}
	os.Setenv("TEST_INT_KEY", "99")
	t.Cleanup(func() { os.Unsetenv("TEST_INT_KEY") })
	if v := getEnvInt("TEST_INT_KEY", 42); v != 99 {
		t.Fatalf("got %d, want 99", v)
	}
	// Invalid int should return fallback
	os.Setenv("TEST_INT_KEY", "not-a-number")
	if v := getEnvInt("TEST_INT_KEY", 42); v != 42 {
		t.Fatalf("invalid int: got %d, want 42", v)
	}
}

func TestGetEnvBool(t *testing.T) {
	os.Unsetenv("TEST_BOOL_KEY")
	if v := getEnvBool("TEST_BOOL_KEY", true); v != true {
		t.Fatal("expected true fallback")
	}
	os.Setenv("TEST_BOOL_KEY", "false")
	t.Cleanup(func() { os.Unsetenv("TEST_BOOL_KEY") })
	if v := getEnvBool("TEST_BOOL_KEY", true); v != false {
		t.Fatal("expected false")
	}
	// Invalid bool should return fallback
	os.Setenv("TEST_BOOL_KEY", "maybe")
	if v := getEnvBool("TEST_BOOL_KEY", true); v != true {
		t.Fatal("invalid bool: expected fallback true")
	}
}
