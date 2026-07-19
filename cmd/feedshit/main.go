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

	// Ensure data directory exists before security init (needs DATA_DIR/key/)
	dataDir := cfg.DataDir
	if err := os.MkdirAll(dataDir+"/key", 0755); err != nil {
		log.Fatalf("Failed to create key dir: %v", err)
	}
	if err := os.MkdirAll(cfg.UploadDir, 0755); err != nil {
		log.Fatalf("Failed to create upload dir: %v", err)
	}

	// Initialize encryption key:
	//   1. If FEEDSHIT_MASTER_KEY env var is set, use it (production isolation).
	//   2. Else if data/key/master.key exists, read it (persisted across restarts).
	//   3. Else generate a random key, save to data/key/master.key (first run).
	keyPath := dataDir + "/key/master.key"
	if err := security.Init(); err != nil {
		key, rErr := os.ReadFile(keyPath)
		if rErr != nil || len(key) != 32 {
			// Generate a new random key
			key = make([]byte, 32)
			if _, genErr := rand.Read(key); genErr != nil {
				log.Fatalf("Failed to generate master key: %v", genErr)
			}
			if wErr := os.WriteFile(keyPath, key, 0600); wErr != nil {
				log.Fatalf("Failed to save master key to %s: %v", keyPath, wErr)
			}
			keyGenerated := true
			if err := security.InitWithKey(key); err != nil {
				log.Fatalf("Failed to set master key: %v", err)
			}
			log.Printf("[INFO] 加密密钥已生成并保存到 %s", keyPath)
			log.Printf("[INFO] 请备份此文件！丢失后将无法解密已存储的敏感信息")
			_ = keyGenerated
		} else {
			if err := security.InitWithKey(key); err != nil {
				log.Fatalf("Failed to set master key from file: %v", err)
			}
			log.Printf("[INFO] 已从 %s 加载加密密钥", keyPath)
		}
	} else {
		log.Println("[INFO] 使用 FEEDSHIT_MASTER_KEY 环境变量作为加密密钥")
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
