package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/leporoni/quantum-entropy-go-service/internal/audit"
	"github.com/leporoni/quantum-entropy-go-service/internal/collector"
	"github.com/leporoni/quantum-entropy-go-service/internal/keymanager"
	"github.com/leporoni/quantum-entropy-go-service/internal/messaging"
	"github.com/leporoni/quantum-entropy-go-service/internal/middleware"
	"github.com/leporoni/quantum-entropy-go-service/internal/ui"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func main() {
	port := getEnv("PORT", "8082")
	masterKeySecret := mustGetEnv("MASTER_KEY_SECRET")
	apiBaseURL := getEnv("API_BASE_URL", "http://quantum-api:8081")
	rabbitmqURL := getEnv("RABBITMQ_URL", "amqp://guest:guest@rabbitmq:5672/")

	// Database (SQLite in-memory)
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		slog.Error("Failed to open database", "error", err)
		os.Exit(1)
	}

	repo, err := keymanager.NewRepository(db)
	if err != nil {
		slog.Error("Failed to initialize repository", "error", err)
		os.Exit(1)
	}

	// RabbitMQ. Messaging connects lazily on first use, so a broker that is down at
	// boot no longer disables event publishing for the process lifetime.
	//
	// The defer below never runs: the only way out of main is r.Run() returning an
	// error, and that path calls os.Exit(1), which skips every deferred function in
	// the process. It is here because it is the right thing to write and because a
	// future graceful-shutdown path would make it real. PENDING: replace os.Exit
	// with a real shutdown sequence (signal handling, then return from main) before
	// claiming any of this cleanup happens.
	mqConn := messaging.NewConnection(rabbitmqURL)
	defer mqConn.Close()
	if _, err := mqConn.Channel(); err != nil {
		slog.Warn("RabbitMQ not reachable at boot; will retry on demand", "error", err)
	}
	pub := messaging.NewPublisher(mqConn)

	// Entropy collector (background goroutine)
	scheduler := collector.NewScheduler(repo, apiBaseURL, pub)

	svc, err := keymanager.NewService(repo, repo, masterKeySecret, pub)
	if err != nil {
		slog.Error("Failed to initialize service", "error", err)
		os.Exit(1)
	}

	// Trigger an immediate local refill when the pool drops below the low watermark.
	svc.OnPoolLow = scheduler.TriggerRefill
	scheduler.Start()
	defer scheduler.Stop() // same caveat as mqConn.Close() above: unreachable on os.Exit

	// Audit service
	auditSvc := audit.NewService(repo, pub)
	auditHandler := audit.NewHandler(auditSvc)

	// HTTP server
	kmHandler := keymanager.NewHandler(svc, repo)
	uiHandler := ui.NewHandler(svc, repo, repo, auditSvc)

	r := gin.New()
	r.Use(gin.Logger(), middleware.Recovery())

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "keymanager"})
	})

	// Canary endpoint to demonstrate the custom Recovery middleware.
	// Only registered when PANIC_DEBUG=true (never expose panics in production).
	if getEnv("PANIC_DEBUG", "") == "true" {
		r.GET("/debug/panic", func(c *gin.Context) {
			panic("boom") // triggers the recover in middleware.Recovery()
		})
	}

	uiHandler.RegisterRoutes(r)

	v1 := r.Group("/api/v1")
	kmHandler.RegisterRoutes(v1)
	auditHandler.RegisterRoutes(v1)

	slog.Info("🚀 keymanager starting", "port", port)
	if err := r.Run(":" + port); err != nil {
		slog.Error("Server failed", "error", err)
		os.Exit(1)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustGetEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("Required environment variable not set", "key", key)
		os.Exit(1)
	}
	return v
}
