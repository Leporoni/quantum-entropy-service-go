package keymanager

import (
	"bytes"
	"crypto/sha256"
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

// TestMasterKeyDerivedOnce locks in the sync.Once contract added with the
// derivation cache: the same secret must always produce the same 32-byte key, no
// matter how many times it is asked for and no matter how many goroutines ask at
// once.
//
// The concurrent phase runs on a COLD service, straight out of NewService. That is
// the whole test: the previous version warmed the cache with two sequential calls
// before the goroutines started, so all eight only ever read s.key and replacing
// the sync.Once with an unsynchronised `if s.key == nil` changed nothing it could
// see. Here the eight goroutines all take the write branch, and -race reports the
// race on s.key. Run it with -race — without the detector this only proves the
// values, never the synchronisation.
func TestMasterKeyDerivedOnce(t *testing.T) {
	const (
		secret    = "super_secret_master_key_change_me_in_prod"
		goroutine = 8
	)
	svc, err := NewService(&fakeStore{}, &fakeKeyStore{}, secret, nil)
	if err != nil {
		t.Fatalf("NewService() = %v, want a service", err)
	}
	want := sha256.Sum256([]byte(secret))

	// The barrier makes all goroutines hit masterKey() at the same moment, which is
	// the only way an unsynchronised lazy init shows up under -race.
	var ready, running sync.WaitGroup
	start := make(chan struct{})
	results := make([][]byte, goroutine)
	ready.Add(goroutine)
	running.Add(goroutine)
	for i := 0; i < goroutine; i++ {
		go func(i int) {
			defer running.Done()
			ready.Done()
			<-start
			results[i] = svc.masterKey()
		}(i)
	}
	ready.Wait()
	close(start)
	running.Wait() // no goroutine outlives this line

	for i, got := range results {
		if len(got) != 32 {
			t.Errorf("goroutine %d: masterKey() length = %d, want 32 (AES-256)", i, len(got))
			continue
		}
		if !bytes.Equal(got, want[:]) {
			t.Errorf("goroutine %d: masterKey() = %x, want SHA-256 of the secret %x", i, got, want)
		}
	}

	// The cache must return the identical slice, not re-hash: if the Once were
	// removed the values would still match, so compare the backing array too — and
	// against the array the cold concurrent phase got, not against a warm call.
	first := svc.masterKey()
	second := svc.masterKey()
	if &first[0] != &second[0] {
		t.Error("masterKey() re-derived instead of reusing the cached key")
	}
	if &first[0] != &results[0][0] {
		t.Error("masterKey() returned a different array than the concurrent phase got: the cache is not shared")
	}
}

// TestNewServiceDoesNotDeriveTheKeyEagerly pins the lazy derivation as a decision
// rather than an accident. It is not about cost — hashing 32 bytes is free either
// way — it is that NewService's validation is the single gate on the secret, and a
// key materialised before anything asked for it is one more copy of it sitting in
// the struct.
func TestNewServiceDoesNotDeriveTheKeyEagerly(t *testing.T) {
	svc, err := NewService(&fakeStore{}, &fakeKeyStore{}, "s3cret", nil)
	if err != nil {
		t.Fatalf("NewService() = %v, want a service", err)
	}
	if svc.key != nil {
		t.Errorf("NewService() derived the key eagerly (%d bytes); derivation belongs to the first masterKey() call", len(svc.key))
	}
	if got := len(svc.masterKey()); got != 32 {
		t.Errorf("masterKey() length = %d, want 32", got)
	}
}

// TestNewServiceRejectsEmptySecret covers the eager validation that the lazy
// derivation deliberately does not repeat.
func TestNewServiceRejectsEmptySecret(t *testing.T) {
	if _, err := NewService(&fakeStore{}, &fakeKeyStore{}, "", nil); err == nil {
		t.Error("NewService() with an empty secret = nil error, want a rejection")
	}

	svc, err := NewService(&fakeStore{}, &fakeKeyStore{}, "s3cret", nil)
	if err != nil {
		t.Fatalf("NewService() = %v, want a service", err)
	}
	if svc.masterSecret != "s3cret" {
		t.Errorf("masterSecret = %q, want it stored for the lazy derivation", svc.masterSecret)
	}
}

// TestXORReaderWithoutSeedReadsFromRand covers the guard that has no seed to work
// with. It is not hypothetical: buildQuantumSeed returns nil both for an empty
// record list and for records whose base64 does not decode, and newXORReader(nil)
// is what GenerateKey then hands to rsa.GenerateKey. Without the guard the XOR
// loop divides by len(x.seed) and panics with "integer divide by zero" — inside key
// generation, on a pool that came back empty, which is exactly when it must not
// blow up.
func TestXORReaderWithoutSeedReadsFromRand(t *testing.T) {
	const chunkSize = 32
	x := &xorReader{seed: nil} // the zero value, as newXORReader(nil) builds it

	buf := make([]byte, chunkSize)
	n, err := x.Read(buf)
	if err != nil {
		t.Fatalf("Read with no seed = %v, want a plain read from crypto/rand", err)
	}
	if n != chunkSize {
		t.Errorf("Read with no seed = %d bytes, want %d", n, chunkSize)
	}
	if x.offset != 0 {
		t.Errorf("offset = %d, want 0: with no seed there is nothing to walk and the offset must not move", x.offset)
	}

	// The bytes have to come from the CSPRNG, not from a zeroed buffer standing in
	// for a seed: two independent reads agreeing byte for byte is not a coincidence
	// anyone is going to see in CI.
	other := make([]byte, chunkSize)
	if _, err := x.Read(other); err != nil {
		t.Fatalf("second Read with no seed = %v", err)
	}
	if bytes.Equal(buf, other) {
		t.Error("two reads returned the same bytes; with no seed the output must be pure crypto/rand")
	}
	if x.offset != 0 {
		t.Errorf("offset = %d after two reads, want 0", x.offset)
	}
}

// TestXORReaderConcurrent shares one xorReader across 8 goroutines. Today
// GenerateKey builds a reader per call and reads it from a single goroutine, so
// this is not reachable in production yet — the lock in Read is there because
// CIRCL will reuse the same reader as the ML-KEM/ML-DSA seed source, which may
// consume it in parallel. Run with -race for the assertion to mean anything.
func TestXORReaderConcurrent(t *testing.T) {
	const (
		workers   = 8
		reads     = 100
		chunkSize = 64
	)

	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i)
	}
	r := newXORReader(seed).(*xorReader)

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			buf := make([]byte, chunkSize)
			for j := 0; j < reads; j++ {
				n, err := r.Read(buf)
				if err != nil {
					t.Errorf("Read: %v", err)
					return
				}
				if n != chunkSize {
					t.Errorf("Read returned %d bytes, want %d", n, chunkSize)
					return
				}
			}
		}()
	}
	wg.Wait()

	// The mutex makes offset advance exactly once per byte, no matter how the
	// reads interleave.
	if want := workers * reads * chunkSize; r.offset != want {
		t.Errorf("offset = %d, want %d", r.offset, want)
	}
}
