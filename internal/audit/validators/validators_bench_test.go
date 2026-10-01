package validators

import (
	"fmt"
	"testing"
)

// Benchmarks for the validators that run on the hot path. The collector
// re-audits the whole pool on every refill, so these functions see input far
// larger than the small vectors the unit tests use, and their cost decides
// whether a refill fits inside its deadline.
//
// Run with:
//
//	go test -bench=. ./internal/audit/validators/
//	go test -bench=. -benchmem ./internal/audit/validators/
//	go test -bench=NIST -benchtime=100x ./internal/audit/validators/
//
// Inputs are built once, outside the timed loop. Allocating them inside b.N
// would measure pseudoRandBytes and the ToBits expansion instead of the
// validator, which is the opposite of what these numbers are for.

// benchSizes are the input lengths worth tracking. 1<<12 keeps a run short
// enough to iterate on; 1<<20 matches the pool order of magnitude the service
// actually refills to.
var benchSizes = []int{1 << 12, 1 << 16, 1 << 20}

func sizeName(n int) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%dMi", n/(1<<20))
	case n >= 1<<10 && n%(1<<10) == 0:
		return fmt.Sprintf("%dKi", n/(1<<10))
	default:
		return fmt.Sprintf("%d", n)
	}
}

// benchOnBytes benchmarks a validator that takes the raw bytes.
func benchOnBytes(b *testing.B, seed uint32, fn func([]byte) float64) {
	for _, n := range benchSizes {
		data := pseudoRandBytes(n, seed)
		b.Run(sizeName(n), func(b *testing.B) {
			b.SetBytes(int64(n))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = fn(data)
			}
		})
	}
}

// benchOnBits benchmarks a validator that takes the expanded bit stream.
func benchOnBits(b *testing.B, seed uint32, fn func([]uint8) float64) {
	for _, n := range benchSizes {
		bits := ToBits(pseudoRandBytes(n, seed))
		b.Run(sizeName(n), func(b *testing.B) {
			b.SetBytes(int64(len(bits)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = fn(bits)
			}
		})
	}
}

// BenchmarkToBits measures the expansion every other benchmark is read
// against: anything much faster than this per byte is not doing real work.
func BenchmarkToBits(b *testing.B) {
	for _, n := range benchSizes {
		data := pseudoRandBytes(n, 1)
		b.Run(sizeName(n), func(b *testing.B) {
			b.SetBytes(int64(n))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = ToBits(data)
			}
		})
	}
}

// BenchmarkEstimateMinEntropyMCV is the 8-bit-symbol counting estimate: one
// full pass plus a 256-entry histogram.
func BenchmarkEstimateMinEntropyMCV(b *testing.B) {
	benchOnBytes(b, 2, EstimateMinEntropyMCV)
}

// BenchmarkEstimateMinEntropyBits allocates the expanded bit stream on every
// call, so its allocation profile differs from the MCV estimate above.
func BenchmarkEstimateMinEntropyBits(b *testing.B) {
	benchOnBytes(b, 2, EstimateMinEntropyBits)
}

// BenchmarkMostCommonValue isolates the 256-entry histogram from the log2
// wrapper in EstimateMinEntropyMCV.
func BenchmarkMostCommonValue(b *testing.B) {
	benchOnBytes(b, 2, func(d []byte) float64 { return float64(MostCommonValue(d)) })
}

// BenchmarkDistinctByteValues uses a map where a fixed 256-entry array would do.
// Three allocations per call and roughly 34 MB/s against MostCommonValue's
// 2300 MB/s on the same input, so the map is the cost, not the traversal.
func BenchmarkDistinctByteValues(b *testing.B) {
	benchOnBytes(b, 2, func(d []byte) float64 { return float64(DistinctByteValues(d)) })
}

// BenchmarkNISTMonobit is the cheapest full-pass validator and the reference
// point the others are read against.
func BenchmarkNISTMonobit(b *testing.B) {
	benchOnBits(b, 3, NISTMonobit)
}

func BenchmarkNISTRuns(b *testing.B) {
	benchOnBits(b, 4, NISTRuns)
}

func BenchmarkNISTBlockFrequency(b *testing.B) {
	benchOnBits(b, 5, func(bits []uint8) float64 { return NISTBlockFrequency(bits, 128) })
}

func BenchmarkNISTLongestRunOfOnes(b *testing.B) {
	benchOnBits(b, 6, NISTLongestRunOfOnes)
}

// BenchmarkNISTApproximateEntropy holds around 40 MB/s, an order of magnitude
// below the linear validators, and allocates 18 times per call for its m-block
// histograms. Like NISTSerial it runs on every audit
// (internal/audit/suites.go:404).
func BenchmarkNISTApproximateEntropy(b *testing.B) {
	benchOnBits(b, 7, func(bits []uint8) float64 { return NISTApproximateEntropy(bits, 5) })
}

// BenchmarkNISTSerial includes the adaptive window search, which raises m from
// 3 upward until the statistic has enough samples. That is why it costs more
// than its input length suggests.
//
// It is the most expensive validator by a wide margin: measured at roughly
// 640 ms and 33 MB allocated per 1 MiB, against 7 ms for the whole CUSUM walk
// next to it. runNIST calls it on every audit (internal/audit/suites.go:410),
// so the adaptive search is the first place to look if an audit runs long.
// Every intermediate m it tries allocates its own histogram, which is where
// the allocation count grows with input size (51 -> 272 -> 950 allocs from
// 4 KiB to 1 MiB).
func BenchmarkNISTSerial(b *testing.B) {
	for _, n := range benchSizes {
		bits := ToBits(pseudoRandBytes(n, 8))
		b.Run(sizeName(n), func(b *testing.B) {
			b.SetBytes(int64(len(bits)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, _, _ = NISTSerial(bits)
			}
		})
	}
}

// BenchmarkNISTCumulativeSums is the most expensive validator on random data,
// and the one whose p-value distribution TestCumulativeSumsDistribution guards.
func BenchmarkNISTCumulativeSums(b *testing.B) {
	for _, n := range benchSizes {
		bits := ToBits(pseudoRandBytes(n, 9))
		b.Run(sizeName(n), func(b *testing.B) {
			b.SetBytes(int64(len(bits)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, _ = NISTCumulativeSums(bits)
			}
		})
	}
}

// BenchmarkCumulativeSumsP isolates the p-value arithmetic from the walk that
// produces z, so the normalCDF loop is visible on its own. z is scaled with n
// the way a random walk reaches it, otherwise the fixed z values below would
// measure a single O(1) early return.
func BenchmarkCumulativeSumsP(b *testing.B) {
	cases := []struct {
		name string
		n    int
		z    int
	}{
		{"4Ki/z8", 4096, 8},
		{"64Ki/z32", 65536, 32},
		{"1Mi/z64", 1 << 20, 64},
		{"1Mi/z256", 1 << 20, 256},
		{"1Mi/z1Ki", 1 << 20, 1024},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = cumulativeSumsP(c.n, c.z)
			}
		})
	}
}

// BenchmarkNormalCDF isolates the function the CUSUM p-value is built from.
func BenchmarkNormalCDF(b *testing.B) {
	for _, x := range []float64{0, 0.5, 1.96, 5, 20} {
		b.Run(fmt.Sprintf("x=%g", x), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = normalCDF(x)
			}
		})
	}
}

// BenchmarkStructureAutocorrelation takes the raw bytes and scans maxLag shifts,
// so its cost grows with both input length and lag depth. It is the slowest
// validator per byte here, near 9 MB/s.
func BenchmarkStructureAutocorrelation(b *testing.B) {
	for _, n := range benchSizes {
		data := pseudoRandBytes(n, 10)
		b.Run(sizeName(n), func(b *testing.B) {
			b.SetBytes(int64(n))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, _, _ = StructureAutocorrelation(data, 16)
			}
		})
	}
}
