package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/leporoni/quantum-entropy-go-service/internal/quantum"
)

func main() {
	port := getEnv("PORT", "8081")

	// Quantum entropy service (LfD API + NIST SP 800-90C mixing)
	quantumSvc := quantum.NewService(quantum.NewLfdClient(""))
	quantumHandler := quantum.NewHandler(quantumSvc)

	r := gin.Default()
	r.SetTrustedProxies(nil)

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "quantum-api"})
	})

	v1 := r.Group("/api/v1")
	quantumHandler.RegisterRoutes(v1)

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	// Graceful shutdown on SIGINT/SIGTERM (docker stop)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	srvErr := make(chan error, 1)
	go func() {
		slog.Info("🚀 quantum-api starting", "port", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			srvErr <- err
		}
	}()

	select {
	case err := <-srvErr:
		slog.Error("Server failed", "error", err)
		os.Exit(1)
	case sig := <-quit:
		slog.Info("Shutting down", "signal", sig.String())
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			slog.Error("Graceful shutdown failed", "error", err)
			os.Exit(1)
		}
		slog.Info("Server stopped")
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}