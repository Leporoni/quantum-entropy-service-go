package validators

import "math"

// StructureBitBias returns the max |z|-score of the fraction of ones across the
// 8 bit positions within a byte. For random data each position has p ~ 0.5 with
// standard error 0.5/sqrt(n).
func StructureBitBias(data []byte) float64 {
	if len(data) == 0 {
		return 0
	}
	bits := ToBits(data)
	counts := make([]int, 8)
	for i, b := range bits {
		counts[i%8] += int(b)
	}
	total := float64(len(data))
	maxZ := 0.0
	for pos := 0; pos < 8; pos++ {
		p := float64(counts[pos]) / total
		z := math.Abs(p-0.5) * 2 * math.Sqrt(total)
		if z > maxZ {
			maxZ = z
		}
	}
	return maxZ
}

// StructureAutocorrelation computes bit-level lag-k agreement z-scores for
// lags 1..maxLag. Returns the max |z|, how many lags exceed |z|>2, and the lag
// of the worst value.
func StructureAutocorrelation(data []byte, maxLag int) (maxZ float64, outOfRange int, worstLag int) {
	bits := ToBits(data)
	n := len(bits)
	if n < 2 {
		return 0, 0, 0
	}

	for lag := 1; lag <= maxLag && lag < n; lag++ {
		agree := 0
		pairs := n - lag
		for i := 0; i < pairs; i++ {
			if bits[i] == bits[i+lag] {
				agree++
			}
		}
		p := float64(agree) / float64(pairs)
		rho := 2*p - 1
		z := math.Abs(rho) * math.Sqrt(float64(pairs))
		if z > maxZ {
			maxZ = z
			worstLag = lag
		}
		if z > 2 {
			outOfRange++
		}
	}
	return maxZ, outOfRange, worstLag
}

// StructureRunsZ returns the Wald-Wolfowitz runs test z-score for the bit stream.
func StructureRunsZ(bits []uint8) float64 {
	n := len(bits)
	if n == 0 {
		return 0
	}
	ones := float64(countOnes(bits))
	zeros := float64(n) - ones

	runs := 1.0
	for i := 1; i < n; i++ {
		if bits[i] != bits[i-1] {
			runs++
		}
	}

	if ones == 0 || zeros == 0 {
		return math.Inf(1)
	}
	expected := 1 + 2*ones*zeros/float64(n)
	variance := (2 * ones * zeros * (2*ones*zeros - float64(n))) / (float64(n) * float64(n) * (float64(n) - 1))
	if variance <= 0 {
		return math.MaxFloat64
	}
	return math.Abs(runs-expected) / math.Sqrt(variance)
}

// StructureSerialCorrelation returns Pearson's correlation coefficient between
// consecutive bytes (lag 1) and its z-score (r*sqrt(n)).
func StructureSerialCorrelation(data []byte) (r, z float64) {
	n := len(data)
	if n < 3 {
		return 0, 0
	}

	sum := 0.0
	for _, b := range data {
		sum += float64(b)
	}
	mean := sum / float64(n)

	var num, denX, denY float64
	for i := 1; i < n; i++ {
		dx := float64(data[i-1]) - mean
		dy := float64(data[i]) - mean
		num += dx * dy
		denX += dx * dx
		denY += dy * dy
	}
	if denX == 0 || denY == 0 {
		return 0, 0
	}
	r = num / math.Sqrt(denX*denY)
	z = math.Abs(r) * math.Sqrt(float64(n))
	return r, z
}