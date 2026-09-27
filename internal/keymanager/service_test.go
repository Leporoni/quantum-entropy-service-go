package keymanager

import (
	"sync"
	"testing"

	"github.com/leporoni/quantum-entropy-go-service/internal/messaging"
)

type fakeStore struct {
	count int64
}

func (f *fakeStore) SaveEntropy(q *QuantumData) error                    { return nil }
func (f *fakeStore) ConsumeEntropy(n int) ([]QuantumData, error)         { return nil, nil }
func (f *fakeStore) CountAllUnusedEntropy() (int64, error)               { return f.count, nil }
func (f *fakeStore) FindAllUnusedBySource(string) ([]QuantumData, error) { return nil, nil }

type fakeKeyStore struct{}

func (f *fakeKeyStore) SaveKey(k *RsaKey) error              { return nil }
func (f *fakeKeyStore) FindAllKeys() ([]RsaKey, error)       { return nil, nil }
func (f *fakeKeyStore) FindKeyByID(id uint) (*RsaKey, error) { return nil, nil }
func (f *fakeKeyStore) DeleteKeyByID(id uint) error          { return nil }
func (f *fakeKeyStore) DeleteAllKeys() error                 { return nil }

type fakePublisher struct {
	mu        sync.Mutex
	exchanges []string
	routeKeys []string
	events    []interface{}
}

func (f *fakePublisher) Publish(exchange, routingKey string, event interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exchanges = append(f.exchanges, exchange)
	f.routeKeys = append(f.routeKeys, routingKey)
	f.events = append(f.events, event)
	return nil
}

func newTestService(pub messaging.EventPublisher) *Service {
	return &Service{store: &fakeStore{count: 100}, keys: &fakeKeyStore{}, pub: pub}
}

func TestCheckPoolStatusPublishesPoolLow(t *testing.T) {
	pub := &fakePublisher{}
	svc := newTestService(pub)
	svc.checkPoolStatus()

	if len(pub.exchanges) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(pub.exchanges))
	}
	if pub.exchanges[0] != messaging.ExchangeEntropyPool {
		t.Errorf("exchange = %q, want %q", pub.exchanges[0], messaging.ExchangeEntropyPool)
	}
	if pub.routeKeys[0] != messaging.RoutingKeyPoolLow {
		t.Errorf("routingKey = %q, want %q", pub.routeKeys[0], messaging.RoutingKeyPoolLow)
	}
	if _, ok := pub.events[0].(messaging.PoolLowEvent); !ok {
		t.Errorf("event type = %T, want messaging.PoolLowEvent", pub.events[0])
	}
}

func TestCheckPoolStatusTriggersRefillWithoutPublisher(t *testing.T) {
	// Regression: OnPoolLow used to sit behind the `pub == nil` guard, so the
	// local refill never fired when RabbitMQ was down.
	svc := newTestService(nil)
	triggered := false
	svc.OnPoolLow = func() { triggered = true }

	svc.checkPoolStatus()

	if !triggered {
		t.Error("OnPoolLow should be invoked even when publisher is nil")
	}
}

func TestCheckPoolStatusHealthyDoesNothing(t *testing.T) {
	pub := &fakePublisher{}
	svc := &Service{store: &fakeStore{count: 500}, keys: &fakeKeyStore{}, pub: pub}
	svc.OnPoolLow = func() { t.Error("OnPoolLow must not fire above the low watermark") }

	svc.checkPoolStatus()

	if len(pub.exchanges) != 0 {
		t.Errorf("expected no publish above low watermark, got %d", len(pub.exchanges))
	}
}
