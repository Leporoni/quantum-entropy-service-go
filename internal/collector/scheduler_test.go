package collector

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leporoni/quantum-entropy-go-service/internal/keymanager"
	"github.com/leporoni/quantum-entropy-go-service/internal/messaging"
)

type fakeEntropyStore struct {
	count   int64
	saved   []*keymanager.QuantumData
	saveErr error
}

func (f *fakeEntropyStore) SaveEntropy(q *keymanager.QuantumData) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	q.ID = uint(len(f.saved) + 1)
	f.saved = append(f.saved, q)
	f.count++
	return nil
}
func (f *fakeEntropyStore) ConsumeEntropy(n int) ([]keymanager.QuantumData, error) { return nil, nil }
func (f *fakeEntropyStore) CountAllUnusedEntropy() (int64, error)                  { return f.count, nil }
func (f *fakeEntropyStore) FindAllUnusedBySource(source string) ([]keymanager.QuantumData, error) {
	return nil, nil
}

type fakePublisher struct {
	exchanges  []string
	routingKey []string
	events     []interface{}
}

func (f *fakePublisher) Publish(exchange, routingKey string, event interface{}) error {
	f.exchanges = append(f.exchanges, exchange)
	f.routingKey = append(f.routingKey, routingKey)
	f.events = append(f.events, event)
	return nil
}

func (f *fakePublisher) countRoutingKey(rk string) int {
	n := 0
	for _, key := range f.routingKey {
		if key == rk {
			n++
		}
	}
	return n
}

// quietLogger silences the per-record info logs so test output stays readable.
func quietLogger(t *testing.T) {
	t.Helper()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
}

// fakeQuantumAPI serves the quantum-api contract: {"data": "<base64>"} with
// payloadSize bytes of deterministic entropy per request.
func fakeQuantumAPI(t *testing.T, payloadSize int, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		data := make([]byte, payloadSize)
		for i := range data {
			data[i] = byte(i % 251)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"data": base64.StdEncoding.EncodeToString(data),
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestScheduler(apiURL string, pub messaging.EventPublisher) *Scheduler {
	s := NewScheduler(&fakeEntropyStore{}, apiURL, pub)
	s.retryDelay = 0
	return s
}

func TestPublishPoolOk(t *testing.T) {
	pub := &fakePublisher{}
	s := NewScheduler(&fakeEntropyStore{}, "http://quantum-api:8081", pub)

	s.publishPoolOk(1000)

	if len(pub.exchanges) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(pub.exchanges))
	}
	if pub.exchanges[0] != messaging.ExchangeEntropyPool {
		t.Errorf("exchange = %q, want %q", pub.exchanges[0], messaging.ExchangeEntropyPool)
	}
	if pub.routingKey[0] != messaging.RoutingKeyPoolOk {
		t.Errorf("routingKey = %q, want %q", pub.routingKey[0], messaging.RoutingKeyPoolOk)
	}
	evt, ok := pub.events[0].(messaging.PoolOkEvent)
	if !ok {
		t.Fatalf("event type = %T, want messaging.PoolOkEvent", pub.events[0])
	}
	if evt.CurrentCount != 1000 {
		t.Errorf("CurrentCount = %d, want 1000", evt.CurrentCount)
	}
	if evt.Threshold != s.highWatermark {
		t.Errorf("Threshold = %d, want %d", evt.Threshold, s.highWatermark)
	}
}

func TestPublishPoolOkNoPublisher(t *testing.T) {
	s := NewScheduler(&fakeEntropyStore{}, "http://quantum-api:8081", nil)
	s.publishPoolOk(1000) // must not panic
}

// A single fetch (entropyChunkBytes) must be split into recordsPerFetch
// records of entropyRecordBytes, preserving the pool accounting.
func TestFetchSplitIntoRecords(t *testing.T) {
	quietLogger(t)
	srv := fakeQuantumAPI(t, entropyChunkBytes, nil)
	s := newTestScheduler(srv.URL, nil)

	data, err := s.fetch()
	if err != nil {
		t.Fatalf("fetch() error = %v", err)
	}
	if len(data) != entropyChunkBytes {
		t.Fatalf("fetched %d bytes, want %d", len(data), entropyChunkBytes)
	}

	store := &fakeEntropyStore{}
	s.store = store

	saved := s.saveRecords(data)
	if saved != recordsPerFetch {
		t.Fatalf("saveRecords() = %d, want %d", saved, recordsPerFetch)
	}
	if len(store.saved) != recordsPerFetch {
		t.Fatalf("stored %d records, want %d", len(store.saved), recordsPerFetch)
	}
	for i, qd := range store.saved {
		decoded, err := base64.StdEncoding.DecodeString(qd.DataBase64)
		if err != nil {
			t.Fatalf("record %d: invalid base64: %v", i, err)
		}
		if len(decoded) != entropyRecordBytes {
			t.Errorf("record %d: %d bytes, want %d", i, len(decoded), entropyRecordBytes)
		}
		if !bytesEqual(decoded, data[i*entropyRecordBytes:(i+1)*entropyRecordBytes]) {
			t.Errorf("record %d: content does not match source slice", i)
		}
		if qd.Source != "LFD" {
			t.Errorf("record %d: Source = %q, want LFD", i, qd.Source)
		}
		if qd.Used {
			t.Errorf("record %d: Used = true, want false", i)
		}
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// collectEntropy must drive the pool from below the low watermark up to the
// high watermark and publish pool.ok exactly once.
func TestCollectEntropyRefillsToHighWatermark(t *testing.T) {
	quietLogger(t)
	var hits atomic.Int64
	srv := fakeQuantumAPI(t, entropyChunkBytes, &hits)

	store := &fakeEntropyStore{count: 100}
	pub := &fakePublisher{}
	s := newTestScheduler(srv.URL, pub)
	s.store = store

	s.collectEntropy()

	if store.count < s.highWatermark {
		t.Fatalf("pool count = %d, want >= %d", store.count, s.highWatermark)
	}
	if len(store.saved) != int(store.count)-100 {
		t.Errorf("saved %d records, want %d", len(store.saved), store.count-100)
	}
	if hits.Load() == 0 {
		t.Error("expected at least one API request")
	}
	if got := pub.countRoutingKey(messaging.RoutingKeyPoolOk); got != 1 {
		t.Errorf("pool.ok published %d times, want 1", got)
	}
	if got := pub.countRoutingKey(messaging.RoutingKeyEntropyNew); got != len(store.saved) {
		t.Errorf("entropy.new published %d times, want %d", got, len(store.saved))
	}
	if got := pub.countRoutingKey(messaging.RoutingKeyEntropyValidated); got != len(store.saved) {
		t.Errorf("entropy.validated published %d times, want %d", got, len(store.saved))
	}
}

// A failing API must abort the batch once maxFailures is reached — no infinite
// loop, and no pool.ok announcement for a pool that never recovered.
func TestCollectEntropyAbortsAfterMaxFailures(t *testing.T) {
	quietLogger(t)
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "upstream on fire", http.StatusInternalServerError)
	}))
	defer srv.Close()

	store := &fakeEntropyStore{count: 0}
	pub := &fakePublisher{}
	s := newTestScheduler(srv.URL, pub)
	s.store = store

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.collectEntropy()
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("collectEntropy did not return after repeated API failures")
	}

	if store.count != 0 {
		t.Errorf("pool count = %d, want 0", store.count)
	}
	if hits.Load() > 100 {
		t.Errorf("API called %d times, want the batch to abort early", hits.Load())
	}
	if got := pub.countRoutingKey(messaging.RoutingKeyPoolOk); got != 0 {
		t.Errorf("pool.ok published %d times, want 0", got)
	}
}

// A closed stopChan must abort runRefillBatch before any request is issued.
func TestRunRefillBatchStopSignalBeforeStart(t *testing.T) {
	quietLogger(t)
	var hits atomic.Int64
	srv := fakeQuantumAPI(t, entropyChunkBytes, &hits)

	store := &fakeEntropyStore{}
	s := newTestScheduler(srv.URL, nil)
	s.store = store
	s.Stop() // closes stopChan

	saved, failures := s.runRefillBatch(10)

	if saved != 0 || failures != 0 {
		t.Errorf("runRefillBatch() = (%d, %d), want (0, 0)", saved, failures)
	}
	if hits.Load() != 0 {
		t.Errorf("API called %d times after stop, want 0", hits.Load())
	}
}

// A stop signal raised mid-refill must make collectEntropy return promptly,
// even while fetches are still in flight.
func TestCollectEntropyStopsMidRefill(t *testing.T) {
	quietLogger(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		data := make([]byte, entropyChunkBytes)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"data": base64.StdEncoding.EncodeToString(data),
		})
	}))
	defer srv.Close()

	store := &fakeEntropyStore{count: 0}
	s := newTestScheduler(srv.URL, nil)
	s.store = store

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.collectEntropy()
	}()

	time.Sleep(30 * time.Millisecond)
	s.Stop()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("collectEntropy did not return after the stop signal")
	}
	if got := store.count; got >= s.highWatermark {
		t.Errorf("pool count = %d, want the refill to be interrupted below %d", got, s.highWatermark)
	}
}

func TestSaveRecordsSkipsFailedSaves(t *testing.T) {
	quietLogger(t)
	srv := fakeQuantumAPI(t, entropyChunkBytes, nil)
	store := &fakeEntropyStore{saveErr: fmt.Errorf("disk full")}
	s := newTestScheduler(srv.URL, nil)
	s.store = store

	data, err := s.fetch()
	if err != nil {
		t.Fatalf("fetch() error = %v", err)
	}
	if saved := s.saveRecords(data); saved != 0 {
		t.Errorf("saveRecords() = %d, want 0 when every save fails", saved)
	}
}
