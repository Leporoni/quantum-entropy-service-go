package quantum

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
)

// generateSystemEntropy generates cryptographically secure random bytes
// using Go's crypto/rand (equivalent to Java's SecureRandom.getInstanceStrong()).
func generateSystemEntropy(size int) ([]byte, error) {
	entropy := make([]byte, size)
	_, err := rand.Read(entropy)
	if err != nil {
		return nil, fmt.Errorf("failed to generate system entropy: %w", err)
	}
	return entropy, nil
}

// generateKeystream generates a deterministic keystream from a key using SHA-256
// in counter mode. This is used for NIST SP 800-90C entropy mixing.
//
// keystream = SHA-256(key || counter_0) || SHA-256(key || counter_1) || ...
func generateKeystream(key []byte, length int) []byte {
	keystream := make([]byte, 0, length)
	counter := 0

	for len(keystream) < length {
		hasher := sha256.New()
		hasher.Write(key)
		hasher.Write([]byte{
			byte(counter >> 24),
			byte(counter >> 16),
			byte(counter >> 8),
			byte(counter),
		})
		block := hasher.Sum(nil)

		remaining := length - len(keystream)
		if remaining < len(block) {
			keystream = append(keystream, block[:remaining]...)
		} else {
			keystream = append(keystream, block...)
		}
		counter++
	}

	return keystream
}
