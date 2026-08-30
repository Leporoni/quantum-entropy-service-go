package quantum

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
)

// MaxCount is the maximum number of bytes that can be requested per call.
const MaxCount = 1024

// Service implements quantum entropy collection and NIST SP 800-90C mixing.
type Service struct {
	lfdClient *LfdClient
}

// NewService creates a new QuantumService.
func NewService(lfdClient *LfdClient) *Service {
	return &Service{lfdClient: lfdClient}
}

// GetEntropyAsBase64 fetches quantum random bytes from LfD and returns as Base64.
// If pure=true, returns raw quantum bytes without mixing.
// If pure=false, mixes with system entropy per NIST SP 800-90C.
func (s *Service) GetEntropyAsBase64(count int, pure bool) (string, error) {
	if count <= 0 || count > MaxCount {
		return "", fmt.Errorf("count must be between 1 and %d, got %d", MaxCount, count)
	}

	slog.Info("Fetching quantum random bytes from LfD API",
		"count", count, "pure", pure)

	quantumBytes, err := s.lfdClient.FetchRandomBytes(count)
	if err != nil {
		return "", fmt.Errorf("failed to fetch from LfD API: %w", err)
	}

	var finalEntropy []byte
	if pure {
		slog.Info("Returning PURE quantum entropy as requested for audit/lab")
		finalEntropy = quantumBytes
	} else {
		// NIST SP 800-90C: Mix quantum entropy with local system entropy
		finalEntropy, err = MixWithSystemEntropy(quantumBytes)
		if err != nil {
			return "", fmt.Errorf("failed to mix entropy: %w", err)
		}
	}

	base64String := base64.StdEncoding.EncodeToString(finalEntropy)
	slog.Info("Quantum entropy generation completed",
		"mode", modeLabel(pure), "bytes", len(finalEntropy))

	return base64String, nil
}

func modeLabel(pure bool) string {
	if pure {
		return "PURE"
	}
	return "MIXED"
}

// MixWithSystemEntropy mixes quantum bytes with local system entropy
// using SHA-256 keystream approach per NIST SP 800-90C.
func MixWithSystemEntropy(quantumBytes []byte) ([]byte, error) {
	systemEntropy, err := generateSystemEntropy(32)
	if err != nil {
		return nil, err
	}

	// SHA-256 mixing key = H(quantumBytes || systemEntropy)
	hasher := sha256.New()
	hasher.Write(quantumBytes)
	hasher.Write(systemEntropy)
	mixingKey := hasher.Sum(nil)

	// Generate keystream from mixing key
	keystream := generateKeystream(mixingKey, len(quantumBytes))

	// XOR quantum bytes with keystream
	mixed := make([]byte, len(quantumBytes))
	for i := range quantumBytes {
		mixed[i] = quantumBytes[i] ^ keystream[i]
	}

	slog.Debug("Mixed quantum entropy with system-derived keystream",
		"bytes", len(quantumBytes))

	return mixed, nil
}
