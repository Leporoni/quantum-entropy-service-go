package validators

import "math"

// NISTMonobit returns the p-value of the Frequency (Monobit) test.
// For random bits with n samples, S = (#1 - #0)/sqrt(n) is ~N(0,1).
func NISTMonobit(bits []uint8) float64 {
	n := len(bits)
	if n == 0 {
		return 0
	}
	ones := countOnes(bits)
	s := math.Abs(float64(2*ones-n)) / math.Sqrt(float64(n))
	return math.Erfc(s / math.Sqrt(2))
}

// NISTBlockFrequency returns the p-value of the Frequency Within a Block test (M-bit blocks).
func NISTBlockFrequency(bits []uint8, m int) float64 {
	n := len(bits)
	if n == 0 {
		return 0
	}
	if m < 1 || n < m {
		return math.NaN()
	}
	blocks := n / m
	x := 0.0
	for i := 0; i < blocks*m; i += m {
		ones := 0
		for j := 0; j < m; j++ {
			ones += int(bits[i+j])
		}
		p := float64(ones) / float64(m)
		x += (p - 0.5) * (p - 0.5)
	}
	chi := 4 * float64(m) * x
	return igamc(float64(blocks)/2, chi/2)
}

// NISTRuns returns the p-value of the Runs test for the maximal excursion between runs.
func NISTRuns(bits []uint8) float64 {
	n := len(bits)
	if n == 0 {
		return 0
	}
	ones := countOnes(bits)
	pi := float64(ones) / float64(n)
	if math.Abs(pi-0.5) >= 2/math.Sqrt(float64(n)) {
		return 0
	}
	runs := 1.0
	for i := 1; i < n; i++ {
		if bits[i] != bits[i-1] {
			runs++
		}
	}
	num := runs - 2*float64(n)*pi*(1-pi)
	den := 2 * math.Sqrt(float64(n)) * pi * (1 - pi)
	return math.Erfc(math.Abs(num) / (den * math.Sqrt(2)))
}

// NISTLongestRunOfOnes returns the p-value of the Longest Run of Ones in a Block test.
// Block size is selected by the sample length tier (M=8 or M=128); samples above
// the official 750k-tier are evaluated indicatively with M=128.
func NISTLongestRunOfOnes(bits []uint8) float64 {
	n := len(bits)
	if n < 128 {
		return math.NaN()
	}

	type tier struct {
		m, k, numberOfBlocks int
		v                    []int
		p                    []float64
	}

	var t tier
	if n < 6272 {
		t = tier{8, 3, 16, []int{1, 2, 3, 4}, []float64{0.2148, 0.3672, 0.2305, 0.1875}}
	} else {
		t = tier{128, 5, 49, []int{4, 5, 6, 7, 8, 9}, []float64{0.1174, 0.2430, 0.2493, 0.1752, 0.1027, 0.1124}}
	}

	v := make([]int, t.k+1)
	blocks := t.numberOfBlocks
	for i := 0; i < blocks; i++ {
		start := i * t.m
		runs := 0
		cur := 0
		for j := 0; j < t.m; j++ {
			if bits[start+j] == 1 {
				cur++
				if cur > runs {
					runs = cur
				}
			} else {
				cur = 0
			}
		}
		// NIST bins: index 0 = run <= V[0]; index j (1..K-1) = run == V[j];
		// index K = run >= V[K].
		idx := 0
		if runs > t.v[0] {
			idx = t.k
			for j := 1; j < t.k; j++ {
				if runs == t.v[j] {
					idx = j
					break
				}
			}
		}
		v[idx]++
	}

	chi := 0.0
	for k := 0; k <= t.k; k++ {
		expected := float64(t.numberOfBlocks) * t.p[k]
		chi += (float64(v[k]) - expected) * (float64(v[k]) - expected) / expected
	}
	return igamc(float64(t.k)/2, chi/2)
}

// NISTApproximateEntropy returns the p-value of the Approximate Entropy test (m-bit blocks).
func NISTApproximateEntropy(bits []uint8, m int) float64 {
	n := len(bits)
	if n == 0 || m < 1 {
		return math.NaN()
	}
	if n < 1<<uint(m) {
		return math.NaN()
	}
	phiM := apEnPhi(bits, m)
	phiM1 := apEnPhi(bits, m+1)
	chi := 2 * float64(n) * (math.Ln2 - (phiM - phiM1))
	one := int64(1)
	return igamc(float64(one<<uint(m-1)), chi/2)
}

// apEnPhi computes the phi statistic for overlapping m-bit patterns on the
// bit stream extended circularly by (m-1) bits.
func apEnPhi(bits []uint8, m int) float64 {
	n := len(bits)
	ext := make([]uint8, 0, n+m-1)
	ext = append(ext, bits...)
	for i := 0; i < m-1; i++ {
		ext = append(ext, bits[i])
	}

	counts := make(map[uint32]int)
	for i := 0; i < n; i++ {
		var key uint32
		for j := 0; j < m; j++ {
			key = (key << 1) | uint32(ext[i+j])
		}
		counts[key]++
	}

	sum := 0.0
	for _, c := range counts {
		p := float64(c) / float64(n)
		sum += p * math.Log(p)
	}
	return sum
}

// NISTSerial returns the two p-values of the Serial test. The pattern length m
// is chosen adaptively as the largest value in [3,16] that fits the sample:
// 2*m*2^m <= n. Below the official minimum (m=16 requires ~2M bits) the result
// is indicative. p2 is NaN when the (m-2) statistic cannot be computed.
func NISTSerial(bits []uint8) (p1, p2 float64, m int) {
	n := len(bits)
	m = 3
	for mm := 3; mm <= 16; mm++ {
		if 2*mm*(1<<uint(mm)) <= n && (1<<uint(mm-2)) <= n {
			m = mm
		}
	}

	psiM := serialPsi2(bits, m)
	psiM1 := serialPsi2(bits, m-1)
	psiM2 := serialPsi2(bits, m-2)

	d1 := psiM - psiM1
	d2 := psiM - 2*psiM1 + psiM2
	one := int64(1)
	p1 = igamc(float64(one<<uint(m-2)), d1/2)
	p2 = igamc(float64(one<<uint(m-3)), d2/2)
	return p1, p2, m
}

// serialPsi2 computes psi^2_m = (2^m / n) * sum(c_i^2) - n over overlapping
// m-bit patterns taken from the circularly extended bit stream.
func serialPsi2(bits []uint8, m int) float64 {
	n := len(bits)
	ext := make([]uint8, 0, n+m-1)
	ext = append(ext, bits...)
	for i := 0; i < m-1; i++ {
		ext = append(ext, bits[i])
	}

	counts := make(map[uint32]int)
	for i := 0; i < n; i++ {
		var key uint32
		for j := 0; j < m; j++ {
			key = (key << 1) | uint32(ext[i+j])
		}
		counts[key]++
	}

	sum := 0.0
	for _, c := range counts {
		sum += float64(c) * float64(c)
	}
	one := int64(1)
	return (float64(one<<uint(m)) * sum / float64(n)) - float64(n)
}

// NISTCumulativeSums returns the two p-values of the Cumulative Sums test
// (forward and reverse), faithful to the NIST reference implementation.
func NISTCumulativeSums(bits []uint8) (pForward, pReverse float64) {
	n := len(bits)
	if n == 0 {
		return 0, 0
	}

	sup := 0
	inf := 0
	sum := 0
	for _, b := range bits {
		if b == 1 {
			sum++
		} else {
			sum--
		}
		if sum > sup {
			sup = sum
		}
		if sum < inf {
			inf = sum
		}
	}

	z := maxInt(sup, -inf)
	zrev := maxInt(sup-sum, sum-inf)

	pForward = cumulativeSumsP(n, z)
	pReverse = cumulativeSumsP(n, zrev)
	if pForward < 0 {
		pForward = 0
	}
	if pReverse < 0 {
		pReverse = 0
	}
	return pForward, pReverse
}

// cumulativeSumsP computes the CUSUM p-value from the reference implementation:
// p = 1 - sum1 + sum2, where k bounds use C-style truncating integer division.
func cumulativeSumsP(n, z int) float64 {
	if z <= 0 {
		return 0
	}
	nz := n / z
	sqrtN := math.Sqrt(float64(n))
	zf := float64(z)

	sum1 := 0.0
	for k := (-nz + 1) / 4; k <= (nz-1)/4; k++ {
		sum1 += normalCDF((4*float64(k)+1)*zf/sqrtN) - normalCDF((4*float64(k)-1)*zf/sqrtN)
	}
	sum2 := 0.0
	for k := (-nz - 3) / 4; k <= (nz-1)/4; k++ {
		sum2 += normalCDF((4*float64(k)+3)*zf/sqrtN) - normalCDF((4*float64(k)+1)*zf/sqrtN)
	}
	return 1 - sum1 + sum2
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}