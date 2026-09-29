package keymanager

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/leporoni/quantum-entropy-go-service/internal/messaging"
)

const (
	entropyPerKey    = 5 // QuantumData records consumed per RSA key generation
	entropyPerExport = 2 // QuantumData records consumed per key export
	poolLowThreshold = 200
)

// Service handles RSA key generation and AES-256-GCM key wrapping.
type Service struct {
	store EntropyStore
	keys  KeyStore
	pub   messaging.EventPublisher

	// The master key is derived once: SHA-256 of the secret, reused for every
	// encrypt/decrypt call. Before this it was re-hashed on every call.
	//
	// sync.Once is the right tool here and nowhere else in this codebase. The
	// derivation is a pure function of an immutable secret, so it cannot fail
	// transiently and there is nothing to retry. The messaging connection is the
	// opposite case — dial and declareTopology both fail transiently, and both the
	// session and the channel can be replaced underneath us — which is why it uses
	// a bool under a mutex. The rejected Once and why it was rejected is written
	// up in docs/PROGRESS_STATUS.md.
	masterSecret string
	keyOnce      sync.Once
	key          []byte // 32-byte AES-256 master key derived from MASTER_KEY_SECRET

	// OnPoolLow, when set, is invoked whenever the pool drops below the low watermark.
	// Used by the entrypoint to trigger an immediate scheduler refill (no consumer loop).
	OnPoolLow func()
}

// NewService creates a new keymanager Service.
// masterKeySecret is the raw secret from env; it is SHA-256 hashed to produce a 32-byte AES key.
// pub may be nil (messaging disabled).
func NewService(store EntropyStore, keys KeyStore, masterKeySecret string, pub messaging.EventPublisher) (*Service, error) {
	if masterKeySecret == "" {
		return nil, errors.New("MASTER_KEY_SECRET must not be empty")
	}
	return &Service{store: store, keys: keys, masterSecret: masterKeySecret, pub: pub}, nil
}

// masterKey returns the 32-byte AES-256 key derived from MASTER_KEY_SECRET,
// hashing the secret on first use and reusing the result afterwards.
//
// The derivation is deliberately lazy, and not for the reason it is usually given.
// "Hashing 32 bytes is cheaper than a service that never encrypts" is not a real
// trade-off: SHA-256 over 32 bytes is on the order of 100 ns, once per process,
// against an RSA key generation that takes hundreds of milliseconds. The actual
// reason is that NewService validating the secret is the single gate on it, and
// deriving eagerly means the key material exists in the struct before anything has
// asked for it. Pinned by TestNewServiceDoesNotDeriveTheKeyEagerly.
func (s *Service) masterKey() []byte {
	s.keyOnce.Do(func() {
		hash := sha256.Sum256([]byte(s.masterSecret))
		s.key = hash[:]
	})
	return s.key
}

// GenerateKey creates a new RSA key pair using quantum entropy as the seed source.
// keySize must be 2048 or 4096.
func (s *Service) GenerateKey(alias string, keySize int) (*RsaKey, error) {
	if keySize != 2048 && keySize != 4096 {
		return nil, errors.New("keySize must be 2048 or 4096")
	}

	// Consume quantum entropy to seed the generation
	entropyRecords, err := s.store.ConsumeEntropy(entropyPerKey)
	if err != nil {
		return nil, fmt.Errorf("pool exhausted: %w", err)
	}

	// Build a quantum-seeded reader by XOR-mixing entropy with crypto/rand
	quantumSeed := buildQuantumSeed(entropyRecords)
	seededReader := newXORReader(quantumSeed)

	privateKey, err := rsa.GenerateKey(seededReader, keySize)
	if err != nil {
		return nil, fmt.Errorf("RSA generation failed: %w", err)
	}

	// Encode public key to PEM
	pubDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, err
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})

	// Encode private key to PEM
	privDER := x509.MarshalPKCS1PrivateKey(privateKey)
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: privDER})

	// Wrap (encrypt) private key with AES-256-GCM
	encrypted, nonce, err := s.aesGCMEncrypt(privPEM)
	if err != nil {
		return nil, fmt.Errorf("key wrapping failed: %w", err)
	}

	key := &RsaKey{
		Alias:               alias,
		KeySize:             keySize,
		PublicKeyPEM:        string(pubPEM),
		EncryptedPrivatePEM: encrypted,
		Nonce:               nonce,
	}
	if err := s.keys.SaveKey(key); err != nil {
		return nil, fmt.Errorf("failed to persist key: %w", err)
	}

	slog.Info("RSA key generated", "id", key.ID, "alias", alias, "keySize", keySize)
	s.publish(messaging.ExchangeKeyEvents, messaging.RoutingKeyKeyCreated,
		messaging.KeyCreatedEvent{ID: key.ID, Alias: alias, KeySize: keySize, Timestamp: time.Now()})
	s.checkPoolStatus()
	return key, nil
}

// ExportPrivateKey decrypts and returns the PEM-encoded private key for the given key ID.
// Consumes quantum entropy for the wrapping operation.
func (s *Service) ExportPrivateKey(id uint) ([]byte, error) {
	key, err := s.keys.FindKeyByID(id)
	if err != nil {
		return nil, err
	}
	if key == nil {
		return nil, errors.New("key not found")
	}

	// Consume entropy for the export operation
	if _, err := s.store.ConsumeEntropy(entropyPerExport); err != nil {
		return nil, fmt.Errorf("pool exhausted for export: %w", err)
	}

	privPEM, err := s.aesGCMDecrypt(key.EncryptedPrivatePEM, key.Nonce)
	if err != nil {
		return nil, fmt.Errorf("key unwrapping failed: %w", err)
	}

	slog.Info("RSA key exported", "id", id, "alias", key.Alias)
	s.publish(messaging.ExchangeKeyEvents, messaging.RoutingKeyKeyExported,
		messaging.KeyExportedEvent{ID: id, Alias: key.Alias, Algorithm: "AES-256-GCM", Timestamp: time.Now()})
	s.checkPoolStatus()
	return privPEM, nil
}

// PoolStatus returns the current count of unused entropy records.
func (s *Service) PoolStatus() (int64, error) {
	return s.store.CountAllUnusedEntropy()
}

// DeleteKey removes a single key and publishes a key.deleted event.
func (s *Service) DeleteKey(id uint) error {
	key, err := s.keys.FindKeyByID(id)
	if err != nil {
		return err
	}
	if key == nil {
		return errors.New("key not found")
	}

	if err := s.keys.DeleteKeyByID(id); err != nil {
		return err
	}

	s.publish(messaging.ExchangeKeyEvents, messaging.RoutingKeyKeyDeleted,
		messaging.KeyDeletedEvent{ID: id, Alias: key.Alias, Timestamp: time.Now()})
	return nil
}

// DeleteAllKeys removes all keys, publishing a key.deleted event per removed key.
func (s *Service) DeleteAllKeys() error {
	keys, err := s.keys.FindAllKeys()
	if err != nil {
		return err
	}

	if err := s.keys.DeleteAllKeys(); err != nil {
		return err
	}

	for _, k := range keys {
		s.publish(messaging.ExchangeKeyEvents, messaging.RoutingKeyKeyDeleted,
			messaging.KeyDeletedEvent{ID: k.ID, Alias: k.Alias, Timestamp: time.Now()})
	}
	return nil
}

// publish sends an event through the Publisher, silently no-oping when messaging is disabled.
func (s *Service) publish(exchange, routingKey string, event interface{}) {
	if s.pub == nil {
		return
	}
	if err := s.pub.Publish(exchange, routingKey, event); err != nil {
		slog.Warn("Failed to publish event", "exchange", exchange, "routingKey", routingKey, "error", err)
	}
}

// checkPoolStatus checks the pool after consuming entropy. When the pool drops
// below the low watermark it triggers a local refill (OnPoolLow) and publishes
// pool.low. The pool.ok event is published by the collector scheduler once a
// refill reaches the high watermark (single source of truth for "pool healthy").
func (s *Service) checkPoolStatus() {
	count, err := s.store.CountAllUnusedEntropy()
	if err != nil {
		slog.Warn("Failed to count entropy for pool event", "error", err)
		return
	}
	if count >= poolLowThreshold {
		return
	}

	// Local refill trigger — independent of messaging so the pool recovers
	// even when RabbitMQ is unavailable.
	if s.OnPoolLow != nil {
		s.OnPoolLow()
	}

	if s.pub == nil {
		return
	}
	evt := messaging.PoolLowEvent{CurrentCount: count, Threshold: poolLowThreshold, Timestamp: time.Now()}
	if err := s.pub.Publish(messaging.ExchangeEntropyPool, messaging.RoutingKeyPoolLow, evt); err != nil {
		slog.Warn("Failed to publish pool.low event", "error", err)
	} else {
		slog.Info("📉 Pool low event published", "count", count)
	}
}

// --- AES-256-GCM helpers ---

func (s *Service) aesGCMEncrypt(plaintext []byte) (ciphertext, nonce []byte, err error) {
	block, err := aes.NewCipher(s.masterKey())
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	ciphertext = gcm.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nonce, nil
}

func (s *Service) aesGCMDecrypt(ciphertext, nonce []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.masterKey())
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, ciphertext, nil)
}

// --- Quantum seed helpers ---

// buildQuantumSeed concatenates and decodes all base64 entropy records into raw bytes.
func buildQuantumSeed(records []QuantumData) []byte {
	var seed []byte
	for _, r := range records {
		b, err := base64.StdEncoding.DecodeString(r.DataBase64)
		if err == nil {
			seed = append(seed, b...)
		}
	}
	return seed
}

// xorReader is an io.Reader that XORs quantum seed bytes with crypto/rand output.
//
// There is no race today: the reader is built per call in GenerateKey and consumed
// by a single goroutine. The lock exists to harden the primitive itself — CIRCL will
// reuse this same reader as the seed source for ML-KEM/ML-DSA, whose generation may
// consume it from several goroutines at once (docs/CIRCL_INTEGRATION_PLAN.md:95,163).
type xorReader struct {
	mu     sync.Mutex
	seed   []byte
	offset int
}

func newXORReader(seed []byte) io.Reader {
	return &xorReader{seed: seed}
}

func (x *xorReader) Read(p []byte) (int, error) {
	n, err := rand.Reader.Read(p)
	if err != nil {
		return n, err
	}
	if len(x.seed) == 0 {
		return n, nil
	}

	// The lock covers only the XOR loop: rand.Reader.Read stays outside it, so the
	// CSPRNG call — the expensive part — still runs fully parallel.
	x.mu.Lock()
	for i := 0; i < n; i++ {
		p[i] ^= x.seed[x.offset%len(x.seed)]
		x.offset++
	}
	x.mu.Unlock()

	return n, nil
}
