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
)

const (
	entropyPerKey    = 5 // QuantumData records consumed per RSA key generation
	entropyPerExport = 2 // QuantumData records consumed per key export
)

// Service handles RSA key generation and AES-256-GCM key wrapping.
type Service struct {
	repo      *Repository
	masterKey []byte // 32-byte AES-256 master key derived from MASTER_KEY_SECRET
}

// NewService creates a new keymanager Service.
// masterKeySecret is the raw secret from env; it is SHA-256 hashed to produce a 32-byte AES key.
func NewService(repo *Repository, masterKeySecret string) (*Service, error) {
	if masterKeySecret == "" {
		return nil, errors.New("MASTER_KEY_SECRET must not be empty")
	}
	hash := sha256.Sum256([]byte(masterKeySecret))
	return &Service{repo: repo, masterKey: hash[:]}, nil
}

// GenerateKey creates a new RSA key pair using quantum entropy as the seed source.
// keySize must be 2048 or 4096.
func (s *Service) GenerateKey(alias string, keySize int) (*RsaKey, error) {
	if keySize != 2048 && keySize != 4096 {
		return nil, errors.New("keySize must be 2048 or 4096")
	}

	// Consume quantum entropy to seed the generation
	entropyRecords, err := s.repo.ConsumeEntropy(entropyPerKey)
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
	if err := s.repo.SaveKey(key); err != nil {
		return nil, fmt.Errorf("failed to persist key: %w", err)
	}

	slog.Info("RSA key generated", "id", key.ID, "alias", alias, "keySize", keySize)
	return key, nil
}

// ExportPrivateKey decrypts and returns the PEM-encoded private key for the given key ID.
// Consumes quantum entropy for the wrapping operation.
func (s *Service) ExportPrivateKey(id uint) ([]byte, error) {
	key, err := s.repo.FindKeyByID(id)
	if err != nil {
		return nil, err
	}
	if key == nil {
		return nil, errors.New("key not found")
	}

	// Consume entropy for the export operation
	if _, err := s.repo.ConsumeEntropy(entropyPerExport); err != nil {
		return nil, fmt.Errorf("pool exhausted for export: %w", err)
	}

	privPEM, err := s.aesGCMDecrypt(key.EncryptedPrivatePEM, key.Nonce)
	if err != nil {
		return nil, fmt.Errorf("key unwrapping failed: %w", err)
	}

	slog.Info("RSA key exported", "id", id, "alias", key.Alias)
	return privPEM, nil
}

// PoolStatus returns the current count of unused entropy records.
func (s *Service) PoolStatus() (int64, error) {
	return s.repo.CountAllUnusedEntropy()
}

// --- AES-256-GCM helpers ---

func (s *Service) aesGCMEncrypt(plaintext []byte) (ciphertext, nonce []byte, err error) {
	block, err := aes.NewCipher(s.masterKey)
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
	block, err := aes.NewCipher(s.masterKey)
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
type xorReader struct {
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
	for i := 0; i < n; i++ {
		if len(x.seed) > 0 {
			p[i] ^= x.seed[x.offset%len(x.seed)]
			x.offset++
		}
	}
	return n, nil
}
