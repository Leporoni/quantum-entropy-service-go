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

func TestIgamcReferenceValues(t *testing.T) {
	cases := []struct {
		a, x, want float64
	}{
		{1, 1, math.Exp(-1)},       // Q(1,1) = e^-1
		{2, 2, 3 * math.Exp(-2)},   // Q(2,2) = e^-2 (1+2)
		{3, 2, 5 * math.Exp(-2)},   // Q(3,2) = e^-2 (1+2+2)
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

func TestMonobit(t *testing.T) {
	allOne := make([]uint8, 1000)
	for i := range allOne {
		allOne[i] = 1
	}
	if p := NISTMonobit(allOne); p > 1e-6 {
		t.Fatalf("all-ones monobit should be ~0, got %v", p)
	}
	alt := make([]uint8, 1000)
	for i := range alt {
		if i%2 == 0 {
			alt[i] = 1
		}
	}
	if p := NISTMonobit(alt); p < 0.9 {
		t.Fatalf("alternating monobit should be ~1, got %v", p)
	}
	if p := NISTMonobit(ToBits(pseudoRandBytes(2048, 42))); p < 0 || p > 1 || math.IsNaN(p) {
		t.Fatalf("random monobit out of range: %v", p)
	}
}

func TestRuns(t *testing.T) {
	alt := make([]uint8, 1000)
	for i := range alt {
		if i%2 == 0 {
			alt[i] = 1
		}
	}
	if p := NISTRuns(alt); p > 1e-3 {
		t.Fatalf("alternating runs p should be tiny, got %v", p)
	}
	if p := NISTRuns(ToBits(pseudoRandBytes(2048, 7))); p < 0 || p > 1 || math.IsNaN(p) {
		t.Fatalf("random runs out of range: %v", p)
	}
}

func TestBlockFrequency(t *testing.T) {
	zeros := allZeros(4096)
	if p := NISTBlockFrequency(ToBits(zeros), 128); p > 1e-6 {
		t.Fatalf("all-zeros block freq should fail, got %v", p)
	}
	if p := NISTBlockFrequency(ToBits(pseudoRandBytes(8192, 99)), 128); p < 0 || p > 1 || math.IsNaN(p) {
		t.Fatalf("random block freq out of range: %v", p)
	}
}

func TestLongestRun(t *testing.T) {
	if p := NISTLongestRunOfOnes(ToBits(allZeros(10000))); p > 1e-3 {
		t.Fatalf("all-zeros longest run should fail, got %v", p)
	}
	var allOne []uint8
	for i := 0; i < 10000; i++ {
		allOne = append(allOne, 1)
	}
	if p := NISTLongestRunOfOnes(allOne); p > 1e-3 {
		t.Fatalf("all-ones longest run should fail, got %v", p)
	}
	if p := NISTLongestRunOfOnes(ToBits(pseudoRandBytes(8192, 3))); p < 0 || p > 1 || math.IsNaN(p) {
		t.Fatalf("random longest run out of range: %v", p)
	}
}

func TestApproximateEntropy(t *testing.T) {
	if p := NISTApproximateEntropy(ToBits(allZeros(10000)), 5); p > 1e-3 {
		t.Fatalf("all-zeros approx entropy should fail, got %v", p)
	}
	if p := NISTApproximateEntropy(ToBits(pseudoRandBytes(10000, 5)), 5); p < 0 || p > 1 || math.IsNaN(p) {
		t.Fatalf("random approx entropy out of range: %v", p)
	}
}

func TestSerial(t *testing.T) {
	p1, p2, m := NISTSerial(ToBits(pseudoRandBytes(8192, 11)))
	if m < 3 || m > 16 {
		t.Fatalf("adaptive m out of range: %d", m)
	}
	for _, p := range []float64{p1, p2} {
		if p < 0 || p > 1 || math.IsNaN(p) {
			t.Fatalf("random serial p out of range: %v", p)
		}
	}
	p1z, p2z, mz := NISTSerial(ToBits(allZeros(8192)))
	if mz < 3 {
		t.Fatalf("adaptive m too small: %d", mz)
	}
	if p1z > 1e-3 || p2z > 1e-3 {
		t.Fatalf("all-zeros serial should fail, got p1=%v p2=%v", p1z, p2z)
	}
}

func TestCumulativeSums(t *testing.T) {
	pf, pr := NISTCumulativeSums(ToBits(allZeros(4096)))
	if pf != 0 || pr != 0 {
		t.Fatalf("all-zeros cumulative sums should be 0, got fwd=%v rev=%v", pf, pr)
	}
	fwd, rev := NISTCumulativeSums(ToBits(pseudoRandBytes(8192, 21)))
	for _, p := range []float64{fwd, rev} {
		if p < 0 || p > 1 || math.IsNaN(p) {
			t.Fatalf("random cumulative sums out of range: %v", p)
		}
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

	rnd := pseudoRandBytes(1 << 16, 8)
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