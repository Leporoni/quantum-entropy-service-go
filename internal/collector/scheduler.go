package collector

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/leporoni/quantum-entropy-go-service/internal/keymanager"
	"github.com/leporoni/quantum-entropy-go-service/internal/messaging"
)

// Scheduler collects quantum entropy from the Quantum API and stores it in the database.
// Implements hysteresis logic: refill when below lowWatermark, stop when above highWatermark.
type Scheduler struct {
	repo          *keymanager.Repository
	pub           *messaging.Publisher
	apiBaseURL    string
	httpClient    *http.Client
	lowWatermark  int64
	highWatermark int64
	stopChan      chan struct{}
	refillChan    chan struct{} // triggered by pool.low events from RabbitMQ
}

// NewScheduler creates a new entropy collector Scheduler.
func NewScheduler(repo *keymanager.Repository, apiBaseURL string, pub *messaging.Publisher) *Scheduler {
	return &Scheduler{
		repo:          repo,
		pub:           pub,
		apiBaseURL:    apiBaseURL,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
		lowWatermark:  200,
		highWatermark: 1000,
		stopChan:      make(chan struct{}),
		refillChan:    make(chan struct{}, 1), // buffered: coalesce multiple signals
	}
}

// TriggerRefill signals the scheduler to start a refill immediately (called on pool.low event).
func (s *Scheduler) TriggerRefill() {
	select {
	case s.refillChan <- struct{}{}:
	default: // already signalled, drop duplicate
	}
}

// Start begins the entropy collection loop in a goroutine.
func (s *Scheduler) Start() {
	go s.run()
	slog.Info("🚀 Entropy Collector started",
		"lowWatermark", s.lowWatermark,
		"highWatermark", s.highWatermark)
}

// Stop gracefully stops the scheduler.
func (s *Scheduler) Stop() {
	close(s.stopChan)
	slog.Info("Entropy Collector stopped")
}

func (s *Scheduler) run() {
	ticker := time.NewTicker(5 * time.Second) // Check every 5 seconds
	defer ticker.Stop()

	for {
		select {
		case <-s.stopChan:
			return
		case <-s.refillChan:
			slog.Info("⚡ Refill triggered by pool.low event")
			s.collectEntropy()
		case <-ticker.C:
			s.collectEntropy()
		}
	}
}

func (s *Scheduler) collectEntropy() {
	count, err := s.repo.CountAllUnusedEntropy()
	if err != nil {
		slog.Error("Failed to count entropy", "error", err)
		return
	}

	if count < s.lowWatermark {
		slog.Info("⛽ Entropy low. Starting rapid refill...", "current", count)

		consecutiveFailures := 0
		maxFailures := 10

		for count < s.highWatermark && consecutiveFailures < maxFailures {
			if s.fetchAndSave() {
				count++
				consecutiveFailures = 0
				time.Sleep(200 * time.Millisecond) // Fast mode
			} else {
				consecutiveFailures++
				time.Sleep(2 * time.Second) // Slow down on error
			}

			// Check for stop signal
			select {
			case <-s.stopChan:
				return
			default:
			}
		}

		if consecutiveFailures >= maxFailures {
			slog.Warn("Stopped refill after consecutive failures",
				"failures", maxFailures, "currentCount", count)
		}
		slog.Info("⛽ Entropy refilled", "currentCount", count)
	}
}

func (s *Scheduler) fetchAndSave() bool {
	// Request 256 bytes (2048 bits) of PURE quantum entropy
	url := fmt.Sprintf("%s/api/v1/quantum-random?count=256&pure=true", s.apiBaseURL)
	slog.Debug("Fetching entropy", "url", url)

	resp, err := s.httpClient.Get(url)
	if err != nil {
		slog.Error("Error fetching from API", "error", err)
		return false
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Error("Error reading response body", "error", err)
		return false
	}

	var result struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		slog.Error("Error parsing response", "error", err)
		return false
	}

	if result.Data == "" {
		slog.Warn("Empty data received from API")
		return false
	}

	// Validate the data can be decoded
	decoded, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil {
		slog.Error("Invalid Base64 data from API", "error", err)
		return false
	}

	// TODO: Add NIST SP 800-90B entropy validation here
	// (Shannon, Chi-Square, Compression checks)

	quantumData := &keymanager.QuantumData{
		DataBase64: result.Data,
		Used:       false,
		Source:     "LFD",
	}

	if err := s.repo.SaveEntropy(quantumData); err != nil {
		slog.Error("Failed to save entropy", "error", err)
		return false
	}

	if s.pub != nil {
		evt := messaging.EntropyNewEvent{
			Source:     "LFD",
			Base64Data: result.Data,
			ByteCount:  len(decoded),
			Timestamp:  time.Now(),
		}
		if err := s.pub.Publish(messaging.ExchangeEntropyCollected, messaging.RoutingKeyEntropyNew, evt); err != nil {
			slog.Warn("Failed to publish entropy.new event", "error", err)
		}
	}

	slog.Info("✅ Entropy saved",
		"id", quantumData.ID, "bytes", len(decoded))
	return true
}
