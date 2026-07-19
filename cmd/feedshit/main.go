package main

import (
	"context"
	"crypto/rand"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"feedshit/internal/app"
	"feedshit/internal/config"
	"feedshit/internal/database"
	"feedshit/internal/email"
	"feedshit/internal/middleware"
	"feedshit/internal/report"
	"feedshit/internal/routes"
	"feedshit/internal/security"
)

func main() {
	cfg := config.LoadConfig()

	// Initialize encryption key: if FEEDSHIT_MASTER_KEY is not set, generate a
	// random key for the session (development mode). In production, always set
	// FEEDSHIT_MASTER_KEY to persist encrypted secrets across restarts.
	if err := security.Init(); err != nil {
		key := make([]byte, 32)
		if _, rErr := rand.Read(key); rErr != nil {
			log.Fatalf("Failed to generate temporary master key: %v", rErr)
		}
		if err := security.InitWithKey(key); err != nil {
			log.Fatalf("Failed to set temporary master key: %v", err)
		}
		log.Println("[WARN] FEEDSHIT_MASTER_KEY not set — using ephemeral key (encrypted secrets will not persist across restarts)")
		log.Println("[WARN] Set FEEDSHIT_MASTER_KEY in production: openssl rand -hex 32")
	}

	// Ensure data directory exists
	if err := os.MkdirAll(cfg.UploadDir, 0755); err != nil {
		log.Fatalf("Failed to create data dir: %v", err)
	}

	// Initialize database
	db, err := database.NewDatabase(cfg.DBPath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	// Seed default config
	db.InitDefaultConfig(cfg)

	// Upgrade any legacy plaintext secrets (smtp_pass, webhook secrets) to
	// encryption at rest. Idempotent: already-encrypted values are left alone.
	// Fail-fast — an inconsistent secret store must not be left behind.
	if err := db.ReEncryptSecrets(); err != nil {
		log.Fatalf("Failed to re-encrypt secrets: %v", err)
	}

	// Initialize components
	sm := middleware.NewSessionManager()
	rl := middleware.NewRateLimiter(cfg.RateLimitPerHour)
	mailer := email.NewMailer(db, cfg.BaseURL)

	application := app.New(cfg, db, sm, rl, mailer)

	// Auto backup on startup
	backupDir := cfg.DataDir + "/backups"
	if bp, err := db.BackupDatabase(backupDir); err != nil {
		log.Printf("Startup backup failed: %v", err)
	} else {
		log.Printf("  Startup backup: %s", bp)
		pruneBackups(db, backupDir, cfg.BackupRetentionDays)
	}

	// Daily backup scheduler
	go func() {
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day()+1, 3, 0, 0, 0, now.Location())
			time.Sleep(next.Sub(now))
			if bp, err := db.BackupDatabase(backupDir); err != nil {
				log.Printf("Daily backup failed: %v", err)
			} else {
				log.Printf("  Daily backup: %s", bp)
				pruneBackups(db, backupDir, cfg.BackupRetentionDays)
			}
		}
	}()

	// Webhook outbox retry ticker (M6): delivers due webhook notifications with backoff.
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			application.ProcessWebhookOutbox()
		}
	}()

	// Weekly report ticker (M13): 每周一 08:00 发送周报邮件。
	go func() {
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day(), 8, 0, 0, 0, now.Location())
			weekday := next.Weekday()
			if weekday != time.Monday {
				// 回退到最近的周一
				offset := (7 - int(weekday) + 1) % 7
				next = next.AddDate(0, 0, offset)
			}
			if next.Before(now) || next.Equal(now) {
				next = next.AddDate(0, 0, 7)
			}
			sleepDuration := next.Sub(now)
			log.Printf("[REPORT] 下次周报时间 %s（还有 %v）", next.Format(time.RFC3339), sleepDuration)
			time.Sleep(sleepDuration)

			if report.AcquireJobLock(db, "weekly_report", 1*time.Hour) {
				if err := report.GenerateWeeklyReport(db, mailer); err != nil {
					log.Printf("[REPORT] 周报生成失败: %v", err)
				}
				report.ReleaseJobLock(db, "weekly_report")
			} else {
				log.Println("[REPORT] 周报锁被其他实例持有，跳过本轮")
			}
		}
	}()

	// Configure trusted proxies for CDN header validation
	middleware.SetTrustedProxies(cfg.TrustedProxies)

	// Setup Gin
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	r.RedirectTrailingSlash = false
	r.RedirectFixedPath = false

	// Configure Gin's trusted proxies
	if len(cfg.TrustedProxies) > 0 {
		r.SetTrustedProxies(cfg.TrustedProxies)
	} else {
		r.SetTrustedProxies(nil)
	}

	// Register all routes
	routes.Register(r, application)

	// --- Start server ---
	addr := ":" + cfg.Port
	srv := &http.Server{Addr: addr, Handler: r}

	log.Printf("FeedShit starting on %s", addr)
	log.Printf("  Landing page:  http://localhost:%s/", cfg.Port)
	log.Printf("  Admin panel:   http://localhost:%s/admin", cfg.Port)
	log.Printf("  Feedback page: http://localhost:%s/fb/{project-slug}", cfg.Port)
	if len(cfg.TrustedProxies) > 0 {
		log.Printf("  Trusted proxies: %v", cfg.TrustedProxies)
	}

	// Graceful shutdown: drain in-flight requests on SIGINT/SIGTERM
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("Shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("Shutdown error: %v", err)
		}
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server failed: %v", err)
	}
	log.Println("Server stopped gracefully")
}

// pruneBackups prunes old backup files according to the retention policy.
// A retention of <= 0 means "no automatic cleanup" and is a no-op, matching the
// documented semantics of BACKUP_RETENTION_DAYS=0.
func pruneBackups(db *database.Database, backupDir string, daysOld int) {
	if daysOld <= 0 {
		return
	}
	pruned, err := db.PruneOldBackups(backupDir, daysOld)
	if err != nil {
		log.Printf("Backup pruning failed: %v", err)
		return
	}
	if pruned > 0 {
		log.Printf("  Pruned %d old backup(s) (retention %d days)", pruned, daysOld)
	}
}
