package audit

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/leporoni/quantum-entropy-go-service/internal/keymanager"
	"github.com/leporoni/quantum-entropy-go-service/internal/messaging"
)

// publishedEvent records one Publish call so tests can assert not only how many
// events were emitted but which routing keys carried them.
type publishedEvent struct {
	exchange   string
	routingKey string
}

func (e publishedEvent) String() string { return e.exchange + "/" + e.routingKey }

// recordingPublisher captures every Publish call. The audit service publishes
// from a single goroutine, but the mutex keeps the fake safe under -race.
type recordingPublisher struct {
	mu     sync.Mutex
	events []publishedEvent
}

func (p *recordingPublisher) Publish(exchange, routingKey string, _ interface{}) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, publishedEvent{exchange: exchange, routingKey: routingKey})
	return nil
}

func (p *recordingPublisher) recorded() []publishedEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]publishedEvent(nil), p.events...)
}

func (p *recordingPublisher) count(routingKey string) int {
	n := 0
	for _, e := range p.recorded() {
		if e.routingKey == routingKey {
			n++
		}
	}
	return n
}

// assertEvents fails unless the recorded publications match want exactly, in
// order. An empty want asserts that nothing was published at all.
func assertEvents(t *testing.T, pub *recordingPublisher, want []string) {
	t.Helper()
	got := pub.recorded()
	gotKeys := make([]string, 0, len(got))
	for _, e := range got {
		gotKeys = append(gotKeys, e.String())
	}
	if len(gotKeys) != len(want) {
		t.Fatalf("published %v, want %v", gotKeys, want)
	}
	for i := range want {
		if gotKeys[i] != want[i] {
			t.Errorf("event %d = %q, want %q (full: %v)", i, gotKeys[i], want[i], gotKeys)
		}
	}
}

// fakeEntropyStore is a keymanager.EntropyStore stub backed by an in-memory
// slice of LFD records; a nil slice models a pool that has never been filled.
type fakeEntropyStore struct {
	records []keymanager.QuantumData
}

func (f *fakeEntropyStore) SaveEntropy(*keymanager.QuantumData) error { return nil }
func (f *fakeEntropyStore) ConsumeEntropy(int) ([]keymanager.QuantumData, error) {
	return nil, nil
}
func (f *fakeEntropyStore) CountAllUnusedEntropy() (int64, error) { return int64(len(f.records)), nil }
func (f *fakeEntropyStore) FindAllUnusedBySource(string) ([]keymanager.QuantumData, error) {
	return f.records, nil
}

// fullPoolStore returns a store holding one 256-byte LFD record, enough for a
// pool that satisfies the smallest audit and suite request sizes.
func fullPoolStore() *fakeEntropyStore {
	return &fakeEntropyStore{records: []keymanager.QuantumData{{
		Source:     "LFD",
		DataBase64: base64.StdEncoding.EncodeToString(deterministicSample(256, 7)),
	}}}
}

func TestRunFullAuditEmptyPoolPublishesNothing(t *testing.T) {
	pub := &recordingPublisher{}
	svc := NewService(&fakeEntropyStore{}, pub)

	report, err := svc.RunFullAudit(256)
	if !errors.Is(err, ErrNoQuantumData) {
		t.Fatalf("RunFullAudit error = %v, want ErrNoQuantumData", err)
	}
	if report != nil {
		t.Errorf("RunFullAudit report = %+v, want nil", report)
	}
	assertEvents(t, pub, nil)
}

func TestRunSuitesEmptyPoolPublishesNothing(t *testing.T) {
	pub := &recordingPublisher{}
	svc := NewService(&fakeEntropyStore{}, pub)

	result, err := svc.RunSuites("basic", 256, 0)
	if !errors.Is(err, ErrNoQuantumData) {
		t.Fatalf("RunSuites error = %v, want ErrNoQuantumData", err)
	}
	if result != nil {
		t.Errorf("RunSuites result = %+v, want nil", result)
	}
	assertEvents(t, pub, nil)
}

func TestRunSuitesUnknownSuiteBeatsPoolCheck(t *testing.T) {
	// The pool check must not mask ErrUnknownSuite: an invalid suite id is a
	// caller bug and stays a caller bug even when no quantum data is available.
	pub := &recordingPublisher{}
	svc := NewService(&fakeEntropyStore{}, pub)

	result, err := svc.RunSuites("does-not-exist", 256, 0)
	if !errors.Is(err, ErrUnknownSuite) {
		t.Fatalf("RunSuites error = %v, want ErrUnknownSuite", err)
	}
	if result != nil {
		t.Errorf("RunSuites result = %+v, want nil", result)
	}
	assertEvents(t, pub, nil)
}

func TestRunFullAuditFullPoolPublishesStartAndComplete(t *testing.T) {
	pub := &recordingPublisher{}
	svc := NewService(fullPoolStore(), pub)

	report, err := svc.RunFullAudit(256)
	if err != nil {
		t.Fatalf("RunFullAudit: unexpected error %v", err)
	}
	if report.SampleSize != 256 {
		t.Errorf("SampleSize = %d, want 256", report.SampleSize)
	}
	if len(report.Results) != 3 {
		t.Fatalf("got %d sources, want 3 (quantum, CSPRNG, LCRNG)", len(report.Results))
	}
	assertEvents(t, pub, []string{
		messaging.ExchangeAuditRequests + "/" + messaging.RoutingKeyAuditStart,
		messaging.ExchangeAuditResults + "/" + messaging.RoutingKeyAuditComplete,
	})
}

func TestRunSuitesFullPoolPublishesStartAndComplete(t *testing.T) {
	pub := &recordingPublisher{}
	svc := NewService(fullPoolStore(), pub)

	result, err := svc.RunSuites("basic", 256, 0)
	if err != nil {
		t.Fatalf("RunSuites: unexpected error %v", err)
	}
	if result.SampleSize != 256 {
		t.Errorf("SampleSize = %d, want 256", result.SampleSize)
	}
	if len(result.Results) != 3 {
		t.Fatalf("got %d sources, want 3 (quantum, CSPRNG, LCRNG)", len(result.Results))
	}
	assertEvents(t, pub, []string{
		messaging.ExchangeAuditRequests + "/" + messaging.RoutingKeyAuditStart,
		messaging.ExchangeAuditResults + "/" + messaging.RoutingKeyAuditComplete,
	})
}

// The REST endpoint treats an empty pool as a legitimate 200 with an empty
// result set, not a 500: a caller polling before the pool fills must not see
// the request as failed.
func TestRunAuditEndpointEmptyPoolReturns200(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pub := &recordingPublisher{}
	svc := NewService(&fakeEntropyStore{}, pub)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/quantum-entropy/audit", nil)

	NewHandler(svc).RunAudit(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"sampleSize":0`) {
		t.Errorf("body = %s, want a sampleSize:0 payload", rec.Body.String())
	}
	assertEvents(t, pub, nil)
}
