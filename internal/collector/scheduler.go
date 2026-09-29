package collector

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leporoni/quantum-entropy-go-service/internal/keymanager"
	"github.com/leporoni/quantum-entropy-go-service/internal/messaging"
)

const (
	entropyChunkBytes  = 1024                                   // bytes per fetch (== quantum.MaxCount)
	entropyRecordBytes = 256                                    // bytes per QuantumData record (preserves watermarks/consumption)
	refillWorkers      = 4                                      // fan-out workers
	maxFailures        = 10                                     // aborts the refill after N failures
	recordsPerFetch    = entropyChunkBytes / entropyRecordBytes // records produced by a single fetch
	defaultRetryDelay  = time.Second                            // pause after a failed fetch
)

// Scheduler collects quantum entropy from the Quantum API and stores it in the database.
// Implements hysteresis logic: refill when below lowWatermark, stop when above highWatermark.
type Scheduler struct {
	store         keymanager.EntropyStore
	pub           messaging.EventPublisher
	apiBaseURL    string
	httpClient    *http.Client
	lowWatermark  int64
	highWatermark int64
	stopChan      chan struct{}
	refillChan    chan struct{} // triggered by pool.low events from RabbitMQ
	retryDelay    time.Duration // pause after a failed fetch (tests set 0)
}

// NewScheduler creates a new entropy collector Scheduler.
func NewScheduler(store keymanager.EntropyStore, apiBaseURL string, pub messaging.EventPublisher) *Scheduler {
	return &Scheduler{
		store:         store,
		pub:           pub,
		apiBaseURL:    apiBaseURL,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
		lowWatermark:  200,
		highWatermark: 1000,
		stopChan:      make(chan struct{}),
		refillChan:    make(chan struct{}, 1), // buffered: coalesce multiple signals
		retryDelay:    defaultRetryDelay,
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

// fetchResult carries one worker's outcome back to the collector. Workers never
// touch the store; the collector is the single writer.
type fetchResult struct {
	data []byte
	err  error
}

func (s *Scheduler) collectEntropy() {
	count, err := s.store.CountAllUnusedEntropy()
	if err != nil {
		slog.Error("Failed to count entropy", "error", err)
		return
	}
	if count >= s.lowWatermark {
		return
	}
	slog.Info("⛽ Entropy low. Starting rapid refill...", "current", count)

	failures := 0
	for count < s.highWatermark && failures < maxFailures {
		needed := s.highWatermark - count
		fetches := int((needed + recordsPerFetch - 1) / recordsPerFetch) // round up
		saved, fails := s.runRefillBatch(fetches)
		count += saved
		failures += fails
		if saved == 0 && fails == 0 {
			break // stop signal: nothing happened
		}
	}

	if failures >= maxFailures {
		slog.Warn("Stopped refill after consecutive failures",
			"failures", maxFailures, "currentCount", count)
	}

	// Reaching (or exceeding) the high watermark means the pool is healthy
	// again — publish pool.ok. Skipped when the refill gave up early or was
	// interrupted, so we only announce a state we actually reached.
	slog.Info("⛽ Entropy refilled", "currentCount", count)
	if count >= s.highWatermark {
		s.publishPoolOk(count)
	}
}

// runRefillBatch fans out `fetches` HTTP requests across refillWorkers
// goroutines and folds the results back in a single collector goroutine.
//
// Fan-out: an unbuffered jobs channel hands each job to exactly one worker.
// Fan-in: workers push onto results; the collector ranges over it. Only the
// collector writes to the store (SQLite in-memory rejects concurrent writers,
// so parallel saves would race into "database is locked").
func (s *Scheduler) runRefillBatch(fetches int) (saved int64, failures int) {
	jobs := make(chan struct{})                      // unbuffered: lets the producer abort early
	results := make(chan fetchResult, refillWorkers) // fan-in

	var wg sync.WaitGroup
	var failCount atomic.Int64

	for i := 0; i < refillWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				select {
				case <-s.stopChan:
					return
				default:
				}
				data, err := s.fetch()
				if err != nil {
					failCount.Add(1)
					results <- fetchResult{err: err}
					if s.retryDelay > 0 {
						time.Sleep(s.retryDelay)
					}
					continue
				}
				results <- fetchResult{data: data}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for i := 0; i < fetches; i++ {
			if failCount.Load() >= maxFailures {
				return
			}
			select {
			case jobs <- struct{}{}:
			case <-s.stopChan:
				return
			}
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	for r := range results {
		if r.err != nil {
			failures++
			continue
		}
		saved += s.saveRecords(r.data)
	}
	return saved, failures
}

// fetch requests one quantum.MaxCount chunk of pure entropy from the Quantum API.
func (s *Scheduler) fetch() ([]byte, error) {
	url := fmt.Sprintf("%s/api/v1/quantum-random?count=%d&pure=true", s.apiBaseURL, entropyChunkBytes)
	slog.Debug("Fetching entropy", "url", url)

	resp, err := s.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch from API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("quantum API returned status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if result.Data == "" {
		return nil, fmt.Errorf("empty data received from API")
	}

	decoded, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil {
		return nil, fmt.Errorf("invalid base64 data from API: %w", err)
	}

	// TODO: Add NIST SP 800-90B entropy validation here
	// (Shannon, Chi-Square, Compression checks)

	return decoded, nil
}

// saveRecords splits the fetched chunk into entropyRecordBytes records, saving
// each one and publishing the matching events. Returns how many were saved.
func (s *Scheduler) saveRecords(data []byte) int64 {
	var saved int64
	for off := 0; off < len(data); off += entropyRecordBytes {
		end := min(off+entropyRecordBytes, len(data))
		chunk := data[off:end]

		qd := &keymanager.QuantumData{
			DataBase64: base64.StdEncoding.EncodeToString(chunk),
			Used:       false,
			Source:     "LFD",
		}
		if err := s.store.SaveEntropy(qd); err != nil {
			slog.Error("Failed to save entropy", "error", err)
			continue
		}
		saved++
		s.publishEntropyEvents(qd, len(chunk))
	}
	return saved
}

func (s *Scheduler) publishEntropyEvents(qd *keymanager.QuantumData, byteCount int) {
	if s.pub == nil {
		return
	}
	newEvt := messaging.EntropyNewEvent{
		Source:     "LFD",
		Base64Data: qd.DataBase64,
		ByteCount:  byteCount,
		Timestamp:  time.Now(),
	}
	if err := s.pub.Publish(messaging.ExchangeEntropyCollected, messaging.RoutingKeyEntropyNew, newEvt); err != nil {
		slog.Warn("Failed to publish entropy.new event", "error", err)
	}

	validatedEvt := messaging.EntropyValidatedEvent{
		ID:        qd.ID,
		Source:    "LFD",
		ByteCount: byteCount,
		PoolSize:  int64(qd.ID),
		Timestamp: time.Now(),
	}
	if err := s.pub.Publish(messaging.ExchangeEntropyCollected, messaging.RoutingKeyEntropyValidated, validatedEvt); err != nil {
		slog.Warn("Failed to publish entropy.validated event", "error", err)
	}
}

// publishPoolOk publishes a pool.ok event when a refill reaches the high watermark.
func (s *Scheduler) publishPoolOk(count int64) {
	if s.pub == nil {
		return
	}
	evt := messaging.PoolOkEvent{
		CurrentCount: count,
		Threshold:    s.highWatermark,
		Timestamp:    time.Now(),
	}
	if err := s.pub.Publish(messaging.ExchangeEntropyPool, messaging.RoutingKeyPoolOk, evt); err != nil {
		slog.Warn("Failed to publish pool.ok event", "error", err)
	} else {
		slog.Info("📈 Pool ok event published", "count", count)
	}
}
