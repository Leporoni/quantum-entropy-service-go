package audit

import (
	"errors"
	"fmt"
	"log/slog"
	"math"

	"github.com/leporoni/quantum-entropy-go-service/internal/audit/validators"
)

const DefaultPRNGSeed int64 = 12345

// ErrUnknownSuite is returned when the requested suite id is not registered.
var ErrUnknownSuite = errors.New("unknown suite")

// Verdict is the colored pass/warn/fail outcome of a single metric.
type Verdict string

const (
	VerdictPass Verdict = "pass"
	VerdictWarn Verdict = "warn"
	VerdictFail Verdict = "fail"
)

// Metric is a single named result row inside a suite.
type Metric struct {
	Name      string
	Value     string
	Reference string
	Verdict   Verdict
}

// SourceResult groups the metrics computed for a single entropy source.
type SourceResult struct {
	Source  string
	Metrics []Metric
}

// SuiteResult is the full outcome of running one suite over the sources.
type SuiteResult struct {
	SuiteID     string
	Name        string
	Description string
	MinNote     string
	SampleSize  int
	Indicative  bool
	Results     []SourceResult
}

type suiteDef struct {
	id          string
	name        string
	description string
	minNote     string
	minBytes    int
	run         func(data []byte) []Metric
}

func registry() []suiteDef {
	return []suiteDef{
		{
			id:          "basic",
			name:        "Basic",
			description: "Global statistical metrics (existing suite) with automatic verdicts.",
			minNote:     "1–16 KB is enough.",
			minBytes:    1024,
			run:         runBasic,
		},
		{
			id:          "min-entropy",
			name:        "Min-Entropy",
			description: "NIST SP 800-90B most-common-value min-entropy estimates (8-bit and bit-level).",
			minNote:     "Best with >= 1M samples; below that it is INDICATIVE.",
			minBytes:    1 << 20,
			run:         runMinEntropy,
		},
		{
			id:          "nist",
			name:        "NIST SP 800-22",
			description: "Practical subset of NIST SP 800-22 tests (p-values, alpha 0.01).",
			minNote:     "Best with >= 125 KB (1M bits); below that it is INDICATIVE.",
			minBytes:    1 << 17,
			run:         runNIST,
		},
		{
			id:          "structure",
			name:        "Structure",
			description: "Order/correlation analysis: bit bias, autocorrelation, runs and serial correlation.",
			minNote:     "Recommended >= 64 KB.",
			minBytes:    1 << 16,
			run:         runStructure,
		},
	}
}

// RunSuites runs the requested lab suite over the quantum/CSPRNG/PRNG sources,
// sharing one sample per source. The PRNG source is deterministically seeded
// (seed == 0 selects DefaultPRNGSeed) so results are replicable.
func (s *Service) RunSuites(suiteID string, requestedSize int, seed int64) (*SuiteResult, error) {
	var def *suiteDef
	for i := range registry() {
		if registry()[i].id == suiteID {
			def = &registry()[i]
			break
		}
	}
	if def == nil {
		return nil, ErrUnknownSuite
	}
	if seed == 0 {
		seed = DefaultPRNGSeed
	}

	slog.Info("Starting Entropy Lab Suite", "suite", suiteID, "requestedSize", requestedSize, "seed", seed)

	var results []SourceResult
	realSampleSize := 0

	queueSample, err := s.getQuantumSample("LFD", requestedSize)
	if err == nil && len(queueSample) > 0 {
		realSampleSize = len(queueSample)
		results = append(results, SourceResult{Source: "Quantum (LFD)", Metrics: def.run(queueSample)})
	}

	if realSampleSize > 0 {
		results = append(results, SourceResult{
			Source:  "Java SecureRandom (CSPRNG)",
			Metrics: def.run(getCsprngSample(realSampleSize)),
		})
		results = append(results, SourceResult{
			Source:  "Java Random (LCRNG)",
			Metrics: def.run(getPrngSample(realSampleSize, seed)),
		})
	}

	return &SuiteResult{
		SuiteID:     def.id,
		Name:        def.name,
		Description: def.description,
		MinNote:     def.minNote,
		SampleSize:  realSampleSize,
		Indicative:  realSampleSize < def.minBytes,
		Results:     results,
	}, nil
}

// ---- verdict helpers ----

// bandVerdict passes inside [okLow, okHigh], warns inside [warnLow, warnHigh].
func bandVerdict(v, okLow, okHigh, warnLow, warnHigh float64) Verdict {
	if v >= okLow && v <= okHigh {
		return VerdictPass
	}
	if v >= warnLow && v <= warnHigh {
		return VerdictWarn
	}
	return VerdictFail
}

// highVerdict passes when v >= okThreshold, warns when v >= warnThreshold.
func highVerdict(v, okThreshold, warnThreshold float64) Verdict {
	if v >= okThreshold {
		return VerdictPass
	}
	if v >= warnThreshold {
		return VerdictWarn
	}
	return VerdictFail
}

// lowVerdict passes when v <= okThreshold, warns when v <= warnThreshold.
func lowVerdict(v, okThreshold, warnThreshold float64) Verdict {
	if v <= okThreshold {
		return VerdictPass
	}
	if v <= warnThreshold {
		return VerdictWarn
	}
	return VerdictFail
}

// zVerdict derives a verdict from an absolute z-score.
func zVerdict(z float64) Verdict {
	switch {
	case z < 2:
		return VerdictPass
	case z < 3:
		return VerdictWarn
	default:
		return VerdictFail
	}
}

// pVerdict derives a verdict from a p-value with NIST alpha 0.01.
func pVerdict(p float64) Verdict {
	switch {
	case p >= 0.01:
		return VerdictPass
	case p >= 0.001:
		return VerdictWarn
	default:
		return VerdictFail
	}
}

// fmtP renders a p-value compactly, switching to scientific for very small values.
func fmtP(p float64) string {
	if math.IsNaN(p) {
		return "n/a"
	}
	if p > 0 && p < 1e-4 {
		return fmt.Sprintf("%.3e", p)
	}
	return fmt.Sprintf("%.6f", p)
}

// ---- suites ----

func runBasic(data []byte) []Metric {
	shannon := validators.CalculateShannonEntropy(data)
	chi := validators.CalculateChiSquare(data)
	pi := validators.EstimatePiMonteCarlo(data)
	comp := validators.CalculateCompressionRatio(data)
	reps := validators.CountRepetitions(data)

	expected := float64(len(data)-1) / 256.0
	ratio := 1.0
	if expected > 0 {
		ratio = float64(reps) / expected
	}

	return []Metric{
		{Name: "Shannon Entropy", Value: fmt.Sprintf("%.3f bits/byte", shannon), Reference: "8.0 (uniform)", Verdict: highVerdict(shannon, 7.9, 7.5)},
		{Name: "Chi-Square", Value: fmt.Sprintf("%.2f", chi), Reference: "~255", Verdict: bandVerdict(chi, 200, 310, 170, 350)},
		{Name: "Pi Estimate (Monte Carlo)", Value: fmt.Sprintf("%.4f", pi), Reference: "3.1416", Verdict: bandVerdict(pi, 3.12, 3.16, 3.06, 3.22)},
		{Name: "Compression Ratio", Value: fmt.Sprintf("%.4f", comp), Reference: "~1.0 (incompressible)", Verdict: lowVerdict(comp, 1.08, 1.20)},
		{Name: "Repetitions", Value: fmt.Sprintf("%d (%.1fx expected)", reps, ratio), Reference: "~n/256", Verdict: lowVerdict(ratio, 2, 4)},
	}
}

func runMinEntropy(data []byte) []Metric {
	mcv := validators.MostCommonValue(data)
	mcvp := float64(mcv) / float64(len(data))
	distinct := validators.DistinctByteValues(data)
	expectedDistinct := validators.ExpectedDistinctValues(len(data))
	distinctRatio := 1.0
	if expectedDistinct > 0 {
		distinctRatio = float64(distinct) / expectedDistinct
	}

	return []Metric{
		{Name: "Min-Entropy (8-bit MCV)", Value: fmt.Sprintf("%.4f bits/byte", validators.EstimateMinEntropyMCV(data)), Reference: "8.0 ideal", Verdict: highVerdict(validators.EstimateMinEntropyMCV(data), 7.9, 7.0)},
		{Name: "Min-Entropy (bit-level)", Value: fmt.Sprintf("%.4f bits/bit", validators.EstimateMinEntropyBits(data)), Reference: "1.0 ideal", Verdict: highVerdict(validators.EstimateMinEntropyBits(data), 0.99, 0.95)},
		{Name: "Most Common Value", Value: fmt.Sprintf("%d (%.4f)", mcv, mcvp), Reference: "~n/256", Verdict: lowVerdict(mcvp, 0.0042, 0.0080)},
		{Name: "Distinct byte values", Value: fmt.Sprintf("%d / %.1f expected", distinct, expectedDistinct), Reference: "256(1-e^(-n/256))", Verdict: highVerdict(distinctRatio, 0.99, 0.95)},
	}
}

func runNIST(data []byte) []Metric {
	bits := validators.ToBits(data)
	n := len(bits)

	blockM := 128
	if n < 128 {
		blockM = 8
	}
	p1, p2, serialM := validators.NISTSerial(bits)
	cumFwd, cumRev := validators.NISTCumulativeSums(bits)

	metrics := []Metric{
		{Name: "Monobit (Frequency)", Value: fmtP(validators.NISTMonobit(bits)), Reference: "p >= 0.01", Verdict: pVerdict(validators.NISTMonobit(bits))},
		{Name: fmt.Sprintf("Block Frequency (M=%d)", blockM), Value: fmtP(validators.NISTBlockFrequency(bits, blockM)), Reference: "p >= 0.01", Verdict: pVerdict(validators.NISTBlockFrequency(bits, blockM))},
		{Name: "Runs", Value: fmtP(validators.NISTRuns(bits)), Reference: "p >= 0.01", Verdict: pVerdict(validators.NISTRuns(bits))},
		{Name: "Longest Run of Ones", Value: fmtP(validators.NISTLongestRunOfOnes(bits)), Reference: "p >= 0.01", Verdict: pVerdict(validators.NISTLongestRunOfOnes(bits))},
		{Name: "Approximate Entropy (m=5)", Value: fmtP(validators.NISTApproximateEntropy(bits, 5)), Reference: "p >= 0.01", Verdict: pVerdict(validators.NISTApproximateEntropy(bits, 5))},
		{Name: fmt.Sprintf("Serial (m=%d, p1)", serialM), Value: fmtP(p1), Reference: "p >= 0.01", Verdict: pVerdict(p1)},
		{Name: fmt.Sprintf("Serial (m=%d, p2)", serialM), Value: fmtP(p2), Reference: "p >= 0.01", Verdict: pVerdict(p2)},
		{Name: "Cumulative Sums (forward)", Value: fmtP(cumFwd), Reference: "p >= 0.01", Verdict: pVerdict(cumFwd)},
		{Name: "Cumulative Sums (reverse)", Value: fmtP(cumRev), Reference: "p >= 0.01", Verdict: pVerdict(cumRev)},
	}
	return metrics
}

func runStructure(data []byte) []Metric {
	bits := validators.ToBits(data)
	maxZ, outOfRange, worstLag := validators.StructureAutocorrelation(data, 16)
	runsZ := validators.StructureRunsZ(bits)
	r, rZ := validators.StructureSerialCorrelation(data)

	return []Metric{
		{Name: "Bit bias (max |z| over 8 positions)", Value: fmt.Sprintf("%.3f", validators.StructureBitBias(data)), Reference: "|z| < 3", Verdict: zVerdict(validators.StructureBitBias(data))},
		{Name: fmt.Sprintf("Autocorrelation lags 1-16 (max |z| @lag %d)", worstLag), Value: fmt.Sprintf("%.3f", maxZ), Reference: "|z| < 2 ok, >3 fail", Verdict: zVerdict(maxZ)},
		{Name: "Autocorrelation lags with |z| > 2", Value: fmt.Sprintf("%d", outOfRange), Reference: "0 ideal", Verdict: lowVerdict(float64(outOfRange), 0, 1)},
		{Name: "Runs z-score", Value: fmt.Sprintf("%.3f", runsZ), Reference: "|z| < 3", Verdict: zVerdict(runsZ)},
		{Name: "Serial correlation (bytes)", Value: fmt.Sprintf("r=%.4f (z=%.3f)", r, rZ), Reference: "|z| < 3", Verdict: zVerdict(rZ)},
	}
}