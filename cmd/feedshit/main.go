package main

import (
	"context"
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
	"feedshit/internal/routes"
)

func main() {
	cfg := config.LoadConfig()

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
