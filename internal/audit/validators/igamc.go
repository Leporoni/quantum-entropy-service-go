package validators

import "math"

const (
	igamcEps   = 3e-7
	igamcFpMin = 1e-300
)

// igamc returns the regularized upper incomplete gamma function Q(a, x),
// used by the NIST SP 800-22 tests to convert chi-square statistics to p-values.
// Implemented via the series and continued-fraction expansions (Numerical Recipes gammq).
func igamc(a, x float64) float64 {
	if x < 0 || a <= 0 {
		return math.NaN()
	}
	if x == 0 {
		return 1
	}

	gln, _ := math.Lgamma(a)

	if x < a+1 {
		// Series representation for the lower incomplete gamma P(a, x).
		ap := a
		sum := 1 / a
		del := sum
		for i := 0; i < 100000; i++ {
			ap++
			del *= x / ap
			sum += del
			if math.Abs(del) < math.Abs(sum)*igamcEps {
				break
			}
		}
		p := sum * math.Exp(-x+a*math.Log(x)-gln)
		return 1 - p
	}

	// Continued fraction representation for Q(a, x).
	b := x + 1 - a
	c := 1 / igamcFpMin
	d := 1 / b
	h := d
	for i := 1; i < 100000; i++ {
		an := -float64(i) * (float64(i) - a)
		b += 2
		d = an*d + b
		if math.Abs(d) < igamcFpMin {
			d = igamcFpMin
		}
		c = b + an/c
		if math.Abs(c) < igamcFpMin {
			c = igamcFpMin
		}
		d = 1 / d
		del := d * c
		h *= del
		if math.Abs(del-1) < igamcEps {
			break
		}
	}
	return math.Exp(-x+a*math.Log(x)-gln) * h
}

// normalCDF returns the standard normal cumulative distribution function Phi(x).
func normalCDF(x float64) float64 {
	return 0.5 * math.Erfc(-x/math.Sqrt(2))
}