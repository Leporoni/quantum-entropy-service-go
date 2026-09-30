package validators

import (
	"math"
	"testing"
)

// pseudoRandBytes returns deterministic pseudo-random bytes (LCG), good enough
// for sanity-range assertions in tests.
func pseudoRandBytes(n int, seed uint32) []byte {
	out := make([]byte, n)
	x := seed
	for i := range out {
		x = x*1664525 + 1013904223
		out[i] = byte(x >> 24)
	}
	return out
}

func allZeros(n int) []byte {
	return make([]byte, n)
}

func allOnes(n int) []byte {
	out := make([]uint8, n)
	for i := range out {
		out[i] = 1
	}
	return out
}

// alternating returns 1010..., the input where every run-based test has a
// known answer: runs at their maximum, monobit at its maximum, cumulative
// sums pinned at zero.
func alternating(n int) []uint8 {
	out := make([]uint8, n)
	for i := range out {
		if i%2 == 0 {
			out[i] = 1
		}
	}
	return out
}

func TestIgamcReferenceValues(t *testing.T) {
	cases := []struct {
		a, x, want float64
	}{
		{1, 1, math.Exp(-1)},           // Q(1,1) = e^-1
		{2, 2, 3 * math.Exp(-2)},       // Q(2,2) = e^-2 (1+2)
		{3, 2, 5 * math.Exp(-2)},       // Q(3,2) = e^-2 (1+2+2)
		{0.5, 0.5, 0.3173105078629141}, // Q(1/2,1/2) = erfc(sqrt(1/2))
		{1, 0, 1},
	}
	for _, c := range cases {
		got := igamc(c.a, c.x)
		if math.Abs(got-c.want) > 1e-6 {
			t.Errorf("igamc(%v,%v)=%v want %v", c.a, c.x, got, c.want)
		}
	}
	// Monotonic decreasing in x and bounded.
	for _, x := range []float64{0.1, 1, 5, 20, 100} {
		if q := igamc(2.5, x); q < 0 || q > 1 || math.IsNaN(q) {
			t.Errorf("igamc(2.5,%v)=%v out of [0,1]", x, q)
		}
	}
}

func TestNormalCDF(t *testing.T) {
	if got := normalCDF(0); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("Phi(0)=%v want 0.5", got)
	}
	if got := normalCDF(1.96); math.Abs(got-0.9750021) > 1e-4 {
		t.Fatalf("Phi(1.96)=%v want ~0.975", got)
	}
	if got := normalCDF(-1.96); math.Abs(got-0.0249979) > 1e-4 {
		t.Fatalf("Phi(-1.96)=%v want ~0.025", got)
	}
}

// TestBiasedInputRejected covers the shape shared by every NIST test: a
// deliberately non-random input must produce a p-value pinned near zero. These
// were six near-identical functions before; as one table a failure names the
// validator and the input instead of pointing at a line inside a 15-line body.
func TestBiasedInputRejected(t *testing.T) {
	cases := []struct {
		name string
		bits []uint8
		run  func([]uint8) float64
		tol  float64
	}{
		{"monobit/all-ones", allOnes(1000), NISTMonobit, 1e-6},
		{"runs/alternating", alternating(1000), NISTRuns, 1e-3},
		{"block-frequency/all-zeros", ToBits(allZeros(4096)), func(b []uint8) float64 {
			return NISTBlockFrequency(b, 128)
		}, 1e-6},
		{"longest-run/all-zeros", ToBits(allZeros(10000)), NISTLongestRunOfOnes, 1e-3},
		{"longest-run/all-ones", allOnes(10000), NISTLongestRunOfOnes, 1e-3},
		{"approx-entropy/all-zeros", ToBits(allZeros(10000)), func(b []uint8) float64 {
			return NISTApproximateEntropy(b, 5)
		}, 1e-3},
		{"serial/all-zeros", ToBits(allZeros(8192)), func(b []uint8) float64 {
			p1, p2, _ := NISTSerial(b)
			return math.Max(p1, p2)
		}, 1e-3},
		{"cumulative-sums/all-zeros", ToBits(allZeros(4096)), func(b []uint8) float64 {
			fwd, rev := NISTCumulativeSums(b)
			return math.Max(fwd, rev)
		}, 0}, // exact: an all-zero walk never leaves zero, so p is exactly 0
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if p := c.run(c.bits); p > c.tol {
				t.Fatalf("p = %v, want <= %v (biased input must be rejected)", p, c.tol)
			}
		})
	}
}

// TestMonobitAlternating is the one inverted assertion: 1010... is the input
// with maximal monobit bias, so p must be near 1 rather than near 0.
func TestMonobitAlternating(t *testing.T) {
	if p := NISTMonobit(alternating(1000)); p < 0.9 {
		t.Fatalf("alternating monobit should be ~1, got %v", p)
	}
}

// TestRandomInputInRange is the mirror of TestBiasedInputRejected: the same
// validators on pseudo-random data must land in [0,1] and never NaN. NaN would
// silently pass a bare `p < 0 || p > 1` check, since every comparison with
// NaN is false, so it is asserted separately.
func TestRandomInputInRange(t *testing.T) {
	cases := []struct {
		name string
		bits []uint8
		run  func([]uint8) float64
	}{
		{"monobit", ToBits(pseudoRandBytes(2048, 42)), NISTMonobit},
		{"runs", ToBits(pseudoRandBytes(2048, 7)), NISTRuns},
		{"block-frequency", ToBits(pseudoRandBytes(8192, 99)), func(b []uint8) float64 {
			return NISTBlockFrequency(b, 128)
		}},
		{"longest-run", ToBits(pseudoRandBytes(8192, 3)), NISTLongestRunOfOnes},
		{"approx-entropy", ToBits(pseudoRandBytes(10000, 5)), func(b []uint8) float64 {
			return NISTApproximateEntropy(b, 5)
		}},
		{"cumulative-sums", ToBits(pseudoRandBytes(8192, 21)), func(b []uint8) float64 {
			fwd, rev := NISTCumulativeSums(b)
			return math.Max(fwd, rev)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, p := range []float64{c.run(c.bits), c.run(c.bits)} {
				if math.IsNaN(p) {
					t.Fatalf("p is NaN")
				}
				if p < 0 || p > 1 {
					t.Fatalf("p = %v, want within [0,1]", p)
				}
			}
		})
	}
}

// TestSerialAdaptiveWindow covers NISTSerial separately because it also returns
// the window m it picked, which the p-value alone cannot verify.
func TestSerialAdaptiveWindow(t *testing.T) {
	p1, p2, m := NISTSerial(ToBits(pseudoRandBytes(8192, 11)))
	if m < 3 || m > 16 {
		t.Errorf("adaptive m = %d, want within [3,16]", m)
	}
	for i, p := range []float64{p1, p2} {
		if math.IsNaN(p) || p < 0 || p > 1 {
			t.Errorf("random serial p%d = %v, want within [0,1]", i+1, p)
		}
	}

	p1z, p2z, mz := NISTSerial(ToBits(allZeros(8192)))
	if mz < 3 {
		t.Errorf("adaptive m on all-zeros = %d, want >= 3", mz)
	}
	if p1z > 1e-3 || p2z > 1e-3 {
		t.Errorf("all-zeros serial should fail, got p1=%v p2=%v", p1z, p2z)
	}
}

func TestCumulativeSumsDistribution(t *testing.T) {
	// On random data the CUSUM p-values should look roughly Uniform(0,1):
	// alpha=0.01 => roughly 1% failures, never a systematic 100% fail.
	fail := 0
	n := 200
	for i := 0; i < n; i++ {
		fwd, rev := NISTCumulativeSums(ToBits(pseudoRandBytes(10000, uint32(i+7))))
		if fwd < 0.01 {
			fail++
		}
		_ = rev
	}
	if fail > 8 { // ~1% expected, allow a generous margin
		t.Fatalf("cumulative sums fail rate too high: %d/%d", fail, n)
	}
}

func TestMinEntropy(t *testing.T) {
	if v := EstimateMinEntropyMCV(allZeros(4096)); v > 0.01 {
		t.Fatalf("all-zeros MCV min-entropy should be ~0, got %v", v)
	}
	if v := EstimateMinEntropyBits(allZeros(4096)); v > 0.01 {
		t.Fatalf("all-zeros bit min-entropy should be ~0, got %v", v)
	}
	rnd := pseudoRandBytes(1<<20, 42)
	if v := EstimateMinEntropyMCV(rnd); v < 7.9 || v > 8.0 {
		t.Fatalf("random MCV min-entropy should be ~8, got %v", v)
	}
	if v := EstimateMinEntropyBits(rnd); v < 0.999 {
		t.Fatalf("random bit min-entropy should be ~1, got %v", v)
	}
	if c := MostCommonValue(rnd); c < 3800 || c > 4400 {
		t.Fatalf("expected MCV count ~4096 for 2^20 random bytes, got %d", c)
	}
	if d := DistinctByteValues(rnd); d < 250 {
		t.Fatalf("expected near all distinct values, got %d", d)
	}
	if e := ExpectedDistinctValues(1 << 20); e < 255 {
		t.Fatalf("expected ~256 distinct for 1M draws, got %v", e)
	}
}

func TestLongestRunRandomDistribution(t *testing.T) {
	// On random data, Longest Run p-values should be roughly uniform: with
	// alpha=0.01 expect ~1% failures, not a systematic 100%.
	fail := 0
	n := 200
	for i := 0; i < n; i++ {
		p := NISTLongestRunOfOnes(ToBits(pseudoRandBytes(10000, uint32(i+99))))
		if p < 0.01 {
			fail++
		}
	}
	if fail > 8 {
		t.Fatalf("longest run fail rate too high: %d/%d", fail, n)
	}
}

func TestStructure(t *testing.T) {
	zeros := allZeros(4096)
	if z := StructureBitBias(zeros); z < 10 {
		t.Fatalf("all-zeros bit bias should be huge, got %v", z)
	}
	maxZ, out, _ := StructureAutocorrelation(zeros, 16)
	if maxZ < 10 || out != 16 {
		t.Fatalf("all-zeros autocorrelation should be huge, got maxZ=%v out=%d", maxZ, out)
	}
	if z := StructureRunsZ(ToBits(zeros)); !math.IsInf(z, 1) {
		t.Fatalf("all-zeros runs z should be +Inf, got %v", z)
	}

	rnd := pseudoRandBytes(1<<16, 8)
	if z := StructureBitBias(rnd); z > 6 {
		t.Fatalf("random bit bias |z|=%v too large", z)
	}
	maxZ2, _, _ := StructureAutocorrelation(rnd, 16)
	if maxZ2 > 5 {
		t.Fatalf("random autocorrelation max |z|=%v too large", maxZ2)
	}
	if z := StructureRunsZ(ToBits(rnd)); z > 4 {
		t.Fatalf("random runs z=%v too large", z)
	}
	r, z := StructureSerialCorrelation(rnd)
	if math.Abs(r) > 0.05 || math.Abs(z) > 4 {
		t.Fatalf("random serial correlation r=%v z=%v out of expected range", r, z)
	}
}
