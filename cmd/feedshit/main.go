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
	dataDir := cfg.DataDir

	// Ensure data directories exist
	if err := os.MkdirAll(dataDir+"/key", 0755); err != nil {
		log.Fatalf("Failed to create key dir: %v", err)
	}
	if err := os.MkdirAll(cfg.UploadDir, 0755); err != nil {
		log.Fatalf("Failed to create upload dir: %v", err)
	}

	// Initialize encryption key (env var → key file → auto-generate)
	keyPath := dataDir + "/key/master.key"
	if err := security.Init(); err != nil {
		key, rErr := os.ReadFile(keyPath)
		if rErr != nil || len(key) != 32 {
			key = make([]byte, 32)
			if _, genErr := rand.Read(key); genErr != nil {
				log.Fatalf("Failed to generate master key: %v", genErr)
			}
			if wErr := os.WriteFile(keyPath, key, 0600); wErr != nil {
				log.Fatalf("Failed to save master key: %v", wErr)
			}
			if err := security.InitWithKey(key); err != nil {
				log.Fatalf("Failed to set master key: %v", err)
			}
			log.Printf("[INFO] 加密密钥已生成并保存到 %s", keyPath)
			log.Printf("[INFO] 请备份此文件！丢失后数据库将无法恢复")
		} else {
			if err := security.InitWithKey(key); err != nil {
				log.Fatalf("Failed to set master key from file: %v", err)
			}
			log.Printf("[INFO] 已从 %s 加载加密密钥", keyPath)
		}
	} else {
		log.Println("[INFO] 使用 FEEDSHIT_MASTER_KEY 环境变量作为加密密钥")
	}

	// Decrypt database file if encrypted version exists
	dbPath := cfg.DBPath
	encPath := dbPath + ".encrypted"
	if _, statErr := os.Stat(encPath); statErr == nil {
		if err := security.DecryptFile(encPath, dbPath); err != nil {
			log.Fatalf("Failed to decrypt database: %v", err)
		}
		os.Remove(encPath)
		log.Println("[INFO] 数据库已解密")
	}

	// Initialize database
	db, err := database.NewDatabase(dbPath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// Seed default config
	db.InitDefaultConfig(cfg)

	// Upgrade any legacy plaintext secrets
	if err := db.ReEncryptSecrets(); err != nil {
		log.Fatalf("Failed to re-encrypt secrets: %v", err)
	}

	// Initialize components
	sm := middleware.NewSessionManager()
	rl := middleware.NewRateLimiter(cfg.RateLimitPerHour)
	mailer := email.NewMailer(db, cfg.BaseURL)
	application := app.New(cfg, db, sm, rl, mailer)

	// Auto backup on startup
	backupDir := dataDir + "/backups"
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

	// Webhook outbox retry ticker
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			application.ProcessWebhookOutbox()
		}
	}()

	// Weekly report ticker
	go func() {
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day(), 8, 0, 0, 0, now.Location())
			weekday := next.Weekday()
			if weekday != time.Monday {
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

	// Configure trusted proxies
	middleware.SetTrustedProxies(cfg.TrustedProxies)

	// Setup Gin
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	r.RedirectTrailingSlash = false
	r.RedirectFixedPath = false
	if len(cfg.TrustedProxies) > 0 {
		r.SetTrustedProxies(cfg.TrustedProxies)
	} else {
		r.SetTrustedProxies(nil)
	}

	// Register routes
	routes.Register(r, application)

	// --- Start server ---
	addr := ":" + cfg.Port
	srv := &http.Server{Addr: addr, Handler: r}

	log.Printf("FeedShit starting on %s", addr)
	log.Printf("  首页: http://localhost:%s/", cfg.Port)
	log.Printf("  后台: http://localhost:%s/admin", cfg.Port)
	log.Printf("  反馈页: http://localhost:%s/fb/{项目slug}", cfg.Port)

	// Graceful shutdown: encrypt database on exit
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("正在关闭服务...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("Shutdown error: %v", err)
		}
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server failed: %v", err)
	}
	log.Println("服务已停止")

	// Close database and encrypt for at-rest protection
	db.Close()
	log.Println("[INFO] 正在加密数据库...")
	if err := security.EncryptFile(dbPath, encPath); err != nil {
		log.Printf("[ERROR] 数据库加密失败: %v（明文未删除，请手动处理）", err)
	} else {
		os.Remove(dbPath)
		os.Remove(dbPath + "-wal")
		os.Remove(dbPath + "-shm")
		log.Println("[INFO] 数据库已加密，明文已清除")
	}
}

// pruneBackups prunes old backup files according to the retention policy.
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
