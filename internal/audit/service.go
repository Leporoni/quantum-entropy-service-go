package audit

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"math"
	mrand "math/rand"

	"github.com/leporoni/quantum-entropy-go-service/internal/audit/validators"
	"github.com/leporoni/quantum-entropy-go-service/internal/keymanager"
)

// AuditMetrics holds the results of an entropy audit for a single source.
type AuditMetrics struct {
	Source           string  `json:"source"`
	ShannonEntropy   float64 `json:"shannonEntropy"`
	ChiSquare        float64 `json:"chiSquare"`
	PiEstimate       float64 `json:"piEstimate"`
	CompressionRatio float64 `json:"compressionRatio"`
	Repetitions      int     `json:"repetitions"`
	Base64Sample     string  `json:"base64Sample"`
	FingerprintHex   string  `json:"fingerprintHex"`
}

// AuditReport holds the full audit results across all sources.
type AuditReport struct {
	SampleSize int            `json:"sampleSize"`
	Results    []AuditMetrics `json:"results"`
}

// Service performs entropy quality audits across multiple sources.
type Service struct {
	repo *keymanager.Repository
}

// NewService creates a new audit Service.
func NewService(repo *keymanager.Repository) *Service {
	return &Service{repo: repo}
}

// RunFullAudit runs a multi-source entropy audit comparing quantum vs. PRNG sources.
func (s *Service) RunFullAudit(requestedSize int) (*AuditReport, error) {
	slog.Info("Starting Dynamic Multi-Source Audit", "requestedSize", requestedSize)

	var results []AuditMetrics
	realSampleSize := 0

	// 1. Audit LFD Quantum Source
	lfdSample, err := s.getQuantumSample("LFD", requestedSize)
	if err == nil && len(lfdSample) > 0 {
		realSampleSize = len(lfdSample)
		results = append(results, auditSource("Quantum (LFD)", lfdSample))
	}

	// 2. Audit Local PRNGs (using the same sample size for fair comparison)
	if realSampleSize > 0 {
		results = append(results, auditSource("Java SecureRandom (CSPRNG)", getCsprngSample(realSampleSize)))
		results = append(results, auditSource("Java Random (LCRNG)", getPrngSample(realSampleSize)))
	}

	return &AuditReport{
		SampleSize: realSampleSize,
		Results:    results,
	}, nil
}

func auditSource(name string, data []byte) AuditMetrics {
	shannon := validators.CalculateShannonEntropy(data)
	chiSquare := validators.CalculateChiSquare(data)
	piEstimate := validators.EstimatePiMonteCarlo(data)
	compressionRatio := validators.CalculateCompressionRatio(data)
	repetitions := validators.CountRepetitions(data)

	fingerprint := fmt.Sprintf("%x", data[:min(len(data), 16)])
	slog.Info("AUDIT TRACEABILITY", "source", name, "fingerprint", fingerprint)

	return AuditMetrics{
		Source:           name,
		ShannonEntropy:   math.Round(shannon*1000) / 1000,
		ChiSquare:        math.Round(chiSquare*100) / 100,
		PiEstimate:       math.Round(piEstimate*10000) / 10000,
		CompressionRatio: math.Round(compressionRatio*10000) / 10000,
		Repetitions:      repetitions,
		Base64Sample:     base64.StdEncoding.EncodeToString(data),
		FingerprintHex:   fingerprint,
	}
}

func (s *Service) getQuantumSample(source string, size int) ([]byte, error) {
	// TODO: Fetch actual quantum data from repository
	// This is a placeholder - will mirror the Java getQuantumSample logic
	data, err := s.repo.FindAllUnusedBySource(source)
	if err != nil || len(data) == 0 {
		return nil, fmt.Errorf("no quantum data available for source: %s", source)
	}

	var sample []byte
	for _, q := range data {
		chunk, _ := base64.StdEncoding.DecodeString(q.DataBase64)
		sample = append(sample, chunk...)
		if len(sample) >= size {
			break
		}
	}

	if len(sample) > size {
		sample = sample[:size]
	}
	return sample, nil
}

func getCsprngSample(size int) []byte {
	sample := make([]byte, size)
	rand.Read(sample)
	return sample
}

func getPrngSample(size int) []byte {
	sample := make([]byte, size)
	mrand.Read(sample)
	return sample
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
