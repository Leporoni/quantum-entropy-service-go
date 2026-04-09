package validators

import (
	"bytes"
	"compress/gzip"
	"math"
)

// CalculateShannonEntropy computes the Shannon entropy of a byte array.
// Perfect entropy for uniformly distributed bytes is ~8.0 bits/byte.
func CalculateShannonEntropy(data []byte) float64 {
	if len(data) == 0 {
		return 0
	}

	freq := make([]int, 256)
	for _, b := range data {
		freq[b]++
	}

	entropy := 0.0
	n := float64(len(data))
	for _, f := range freq {
		if f > 0 {
			p := float64(f) / n
			entropy -= p * math.Log2(p)
		}
	}
	return entropy
}

// CalculateChiSquare computes the Chi-Square statistic for uniformity.
// For truly random data with 256 categories, the expected value is ~255.0.
func CalculateChiSquare(data []byte) float64 {
	if len(data) == 0 {
		return 0
	}

	freq := make([]int, 256)
	for _, b := range data {
		freq[b]++
	}

	expected := float64(len(data)) / 256.0
	chiSquare := 0.0
	for _, f := range freq {
		diff := float64(f) - expected
		chiSquare += (diff * diff) / expected
	}
	return chiSquare
}

// EstimatePiMonteCarlo estimates Pi using a Monte Carlo method.
// Pairs of bytes are treated as (x, y) coordinates in a 256x256 grid.
// The ratio of points inside a quarter circle estimates Pi/4.
// True random data should yield ~3.1416.
func EstimatePiMonteCarlo(data []byte) float64 {
	if len(data) < 2 {
		return 0
	}

	inside := 0
	total := 0

	for i := 0; i+1 < len(data); i += 2 {
		x := float64(data[i]) / 256.0
		y := float64(data[i+1]) / 256.0
		if x*x+y*y <= 1.0 {
			inside++
		}
		total++
	}

	if total == 0 {
		return 0
	}
	return 4.0 * float64(inside) / float64(total)
}

// CalculateCompressionRatio computes how compressible the data is using GZIP.
// Truly random data is nearly incompressible (ratio ~1.0).
// Patterned data will compress well (ratio << 1.0).
func CalculateCompressionRatio(data []byte) float64 {
	if len(data) == 0 {
		return 0
	}

	var buf bytes.Buffer
	writer, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return 1.0
	}
	writer.Write(data)
	writer.Close()

	return float64(buf.Len()) / float64(len(data))
}

// CountRepetitions counts consecutive identical byte pairs.
// High repetition counts indicate poor randomness.
func CountRepetitions(data []byte) int {
	if len(data) < 2 {
		return 0
	}

	count := 0
	for i := 1; i < len(data); i++ {
		if data[i] == data[i-1] {
			count++
		}
	}
	return count
}
