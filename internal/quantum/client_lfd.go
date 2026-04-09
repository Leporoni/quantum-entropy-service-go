package quantum

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// LfdApiResponse represents the response from the LfD (Laboratory for Digitalisation) API.
type LfdApiResponse struct {
	Qrn string `json:"qrn"` // Hex-encoded quantum random numbers
}

// LfdClient is an HTTP client for the LfD quantum random API.
type LfdClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewLfdClient creates a new LfD API client.
func NewLfdClient(baseURL string) *LfdClient {
	return &LfdClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// FetchRandomBytes fetches quantum random bytes from the LfD API.
// The API returns hex-encoded data which is decoded to raw bytes.
func (c *LfdClient) FetchRandomBytes(count int) ([]byte, error) {
	url := fmt.Sprintf("%s/api/v1/quantum-random?count=%d&format=HEX", c.baseURL, count)
	slog.Debug("Fetching from LfD API", "url", url)

	resp, err := c.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("LfD API returned status %d: %s", resp.StatusCode, string(body))
	}

	var apiResp LfdApiResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode LfD API response: %w", err)
	}

	if apiResp.Qrn == "" {
		return nil, fmt.Errorf("empty quantum data received from LfD API")
	}

	// Decode hex string to raw bytes
	quantumBytes, err := hex.DecodeString(apiResp.Qrn)
	if err != nil {
		return nil, fmt.Errorf("failed to decode hex from LfD API: %w", err)
	}

	slog.Debug("Received quantum bytes from LfD API",
		"hexLength", len(apiResp.Qrn), "byteLength", len(quantumBytes))

	return quantumBytes, nil
}
