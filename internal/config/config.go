package config

import (
	"os"
	"strconv"
)

// Config holds all application configuration, sourced from environment variables.
type Config struct {
	Port             string
	AdminUsername    string
	AdminPassword    string
	DataDir          string
	UploadDir        string
	DBPath           string
	SMTPHost         string
	SMTPPort         int
	SMTPUser         string
	SMTPPass         string
	SMTPFrom         string
	SMTPTo           string
	NotifyEnable     bool
	BaseURL          string
	PoWDifficulty    int
	RateLimitPerHour int
	MaxUploadSize    int64
}

func LoadConfig() *Config {
	cfg := &Config{
		Port:             getEnv("PORT", "8080"),
		AdminUsername:    getEnv("ADMIN_USERNAME", "admin"),
		AdminPassword:    getEnv("ADMIN_PASSWORD", "changeme"),
		DataDir:          getEnv("DATA_DIR", "./data"),
		SMTPHost:         getEnv("SMTP_HOST", ""),
		SMTPPort:         getEnvInt("SMTP_PORT", 587),
		SMTPUser:         getEnv("SMTP_USER", ""),
		SMTPPass:         getEnv("SMTP_PASS", ""),
		SMTPFrom:         getEnv("SMTP_FROM", ""),
		SMTPTo:           getEnv("SMTP_TO", ""),
		NotifyEnable:     getEnvBool("NOTIFY_ENABLE", false),
		BaseURL:          getEnv("BASE_URL", "http://localhost:8080"),
		PoWDifficulty:    getEnvInt("POW_DIFFICULTY", 4),
		RateLimitPerHour: getEnvInt("RATE_LIMIT_PER_HOUR", 3),
		MaxUploadSize:    int64(getEnvInt("MAX_UPLOAD_MB", 20)) * 1024 * 1024,
	}
	cfg.UploadDir = cfg.DataDir + "/uploads"
	cfg.DBPath = cfg.DataDir + "/feedbacks.db"
	return cfg
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}
