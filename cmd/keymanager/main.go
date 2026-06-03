package main

import (
	"log/slog"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/leporoni/quantum-entropy-go-service/internal/audit"
	"github.com/leporoni/quantum-entropy-go-service/internal/collector"
	"github.com/leporoni/quantum-entropy-go-service/internal/keymanager"
	"github.com/leporoni/quantum-entropy-go-service/internal/messaging"
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

	// RabbitMQ
	var pub *messaging.Publisher
	mqConn, err := messaging.NewConnection(rabbitmqURL)
	if err != nil {
		slog.Warn("RabbitMQ unavailable, continuing without messaging", "error", err)
	} else {
		defer mqConn.Close()
		if err := messaging.SetupExchangesAndQueues(mqConn); err != nil {
			slog.Warn("Failed to setup exchanges/queues", "error", err)
		} else {
			pub = messaging.NewPublisher(mqConn)
		}
	}

	svc, err := keymanager.NewService(repo, masterKeySecret, pub)

	// Entropy collector (background goroutine)
	scheduler := collector.NewScheduler(repo, apiBaseURL)
	scheduler.Start()
	defer scheduler.Stop()

	// Audit service
	auditSvc := audit.NewService(repo)
	auditHandler := audit.NewHandler(auditSvc)

	// HTTP server
	kmHandler := keymanager.NewHandler(svc, repo)
	auditHandler := audit.NewHandler(auditSvc)
	uiHandler := ui.NewHandler(svc, repo, auditSvc)

	r := gin.Default()

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok", "service": "keymanager"})
	})

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
