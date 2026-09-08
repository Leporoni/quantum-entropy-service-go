package validators

import "math"

// EstimateMinEntropyMCV estimates the min-entropy of 8-bit symbols using the
// Most Common Value (MCV) counting estimate of NIST SP 800-90B section 6.3.1.
// Returns the min-entropy in bits/byte. The counting estimate is the
// maximum-likelihood p_hat = c/n; below the 10^6-sample regime the result is
// indicative only (bias correction from the standard is negligible there).
func EstimateMinEntropyMCV(data []byte) float64 {
	if len(data) == 0 {
		return 0
	}
	c := MostCommonValue(data)
	return -math.Log2(float64(c) / float64(len(data)))
}

// EstimateMinEntropyBits estimates the min-entropy of the bit stream by the
// binary MCV counting estimate (2-symbol alphabet). Returns bits/bit.
func EstimateMinEntropyBits(data []byte) float64 {
	if len(data) == 0 {
		return 0
	}
	bits := ToBits(data)
	ones := countOnes(bits)
	pmax := float64(ones) / float64(len(bits))
	if pmax < 0.5 {
		pmax = 1 - pmax
	}
	return -math.Log2(pmax)
}

// MostCommonValue returns the count of the most frequent byte value.
func MostCommonValue(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	freq := make([]int, 256)
	for _, b := range data {
		freq[b]++
	}
	c := 0
	for _, f := range freq {
		if f > c {
			c = f
		}
	}
	return c
}

// DistinctByteValues returns how many distinct byte values were observed.
func DistinctByteValues(data []byte) int {
	seen := make(map[byte]struct{}, 256)
	for _, b := range data {
		seen[b] = struct{}{}
	}
	return len(seen)
}

// ExpectedDistinctValues returns E[distinct values] for uniform iid 0..255 draws.
func ExpectedDistinctValues(n int) float64 {
	return 256.0 * (1 - math.Pow(255.0/256.0, float64(n)))
}