package collector

import (
	"testing"

	"github.com/leporoni/quantum-entropy-go-service/internal/keymanager"
	"github.com/leporoni/quantum-entropy-go-service/internal/messaging"
)

type fakeEntropyStore struct {
	count int64
}

func (f *fakeEntropyStore) SaveEntropy(q *keymanager.QuantumData) error            { return nil }
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
