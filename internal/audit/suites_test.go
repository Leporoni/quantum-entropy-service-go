package audit

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// deterministicSample returns reproducible bytes (LCG) so suite results are
// stable across runs and comparable against the expected metric order.
func deterministicSample(n int, seed uint32) []byte {
	out := make([]byte, n)
	x := seed
	for i := range out {
		x = x*1664525 + 1013904223
		out[i] = byte(x >> 24)
	}
	return out
}

func metricNames(metrics []Metric) []string {
	names := make([]string, 0, len(metrics))
	for _, m := range metrics {
		names = append(names, m.Name)
	}
	return names
}

// assertOrder fails unless metrics match wantNames exactly, count and order.
func assertOrder(t *testing.T, label string, metrics []Metric, wantNames []string) {
	t.Helper()
	got := metricNames(metrics)
	if len(got) != len(wantNames) {
		t.Fatalf("%s: got %d metrics %v, want %d", label, len(got), got, len(wantNames))
	}
	for i := range wantNames {
		if got[i] != wantNames[i] {
			t.Errorf("%s: metric %d = %q, want %q (full: %v)", label, i, got[i], wantNames[i], got)
		}
	}
}

func TestRunFanInPreservesInputOrder(t *testing.T) {
	// Descending delays: the first closure finishes last, so completion order
	// is the reverse of input order. Output must still follow the input order.
	delays := []time.Duration{60 * time.Millisecond, 50, 40, 30, 20, 10}

	fns := make([]func() []Metric, 0, len(delays))
	want := make([]string, 0, len(delays))
	for i, d := range delays {
		fns = append(fns, func() []Metric {
			time.Sleep(d)
			return []Metric{{Name: fmt.Sprintf("m%d", i)}}
		})
		want = append(want, fmt.Sprintf("m%d", i))
	}

	assertOrder(t, "runFanIn", runFanIn(fns), want)
}

func TestRunFanInFlattensMultiMetricGroups(t *testing.T) {
	// A closure may return several metrics; they stay contiguous and in order.
	fns := []func() []Metric{
		func() []Metric {
			time.Sleep(30 * time.Millisecond)
			return []Metric{{Name: "a1"}, {Name: "a2"}}
		},
		func() []Metric {
			return []Metric{{Name: "b1"}}
		},
		func() []Metric {
			time.Sleep(10 * time.Millisecond)
			return []Metric{{Name: "c1"}, {Name: "c2"}, {Name: "c3"}}
		},
	}

	assertOrder(t, "runFanIn/multi", runFanIn(fns), []string{"a1", "a2", "b1", "c1", "c2", "c3"})
}

func TestRunFanInEmpty(t *testing.T) {
	if got := runFanIn(nil); len(got) != 0 {
		t.Errorf("runFanIn(nil) = %v, want empty", got)
	}
}

func TestRunFanInRunsConcurrently(t *testing.T) {
	// Every closure blocks until all of them are in flight at the same time, so a
	// sequential implementation cannot reach the barrier and the test fails
	// instead of merely being slow. Every closure must observe the barrier, so the
	// hit count is asserted rather than a single flag: the closure that closes the
	// channel would also observe it, masking a sequential run. Counting hits keeps
	// the assertion deterministic under -race, with no wall-clock threshold.
	const workers = 5
	var inFlight atomic.Int32
	var barrierHits atomic.Int32
	allStarted := make(chan struct{})

	fns := make([]func() []Metric, workers)
	want := make([]string, 0, workers)
	for i := range fns {
		fns[i] = func() []Metric {
			if inFlight.Add(1) == workers {
				close(allStarted)
			}
			select {
			case <-allStarted:
				barrierHits.Add(1)
			case <-time.After(time.Second):
			}
			return []Metric{{Name: fmt.Sprintf("m%d", i)}}
		}
		want = append(want, fmt.Sprintf("m%d", i))
	}

	assertOrder(t, "runFanIn/concurrent", runFanIn(fns), want)
	if got := barrierHits.Load(); got != workers {
		t.Errorf("only %d/%d closures overlapped: runFanIn appears to run them sequentially", got, workers)
	}
}

func TestRunBasicMetricOrder(t *testing.T) {
	metrics := runBasic(deterministicSample(1024, 7))
	assertOrder(t, "runBasic", metrics, []string{
		"Shannon Entropy",
		"Chi-Square",
		"Pi Estimate (Monte Carlo)",
		"Compression Ratio",
		"Repetitions",
	})
	for i, m := range metrics {
		if m.Value == "" || m.Verdict == "" {
			t.Errorf("runBasic metric %d (%q) incomplete: %+v", i, m.Name, m)
		}
	}
}

func TestRunMinEntropyMetricOrder(t *testing.T) {
	metrics := runMinEntropy(deterministicSample(1024, 7))
	assertOrder(t, "runMinEntropy", metrics, []string{
		"Min-Entropy (8-bit MCV)",
		"Min-Entropy (bit-level)",
		"Most Common Value",
		"Distinct byte values",
	})
}

func TestRunNISTMetricOrder(t *testing.T) {
	metrics := runNIST(deterministicSample(1024, 7))
	if len(metrics) != 9 {
		t.Fatalf("runNIST: got %d metrics %v, want 9", len(metrics), metricNames(metrics))
	}

	wantPrefixes := []string{
		"Monobit (Frequency)",
		"Block Frequency (M=",
		"Runs",
		"Longest Run of Ones",
		"Approximate Entropy (m=5)",
		"Serial (m=",
		"Serial (m=",
		"Cumulative Sums (forward)",
		"Cumulative Sums (reverse)",
	}
	for i, prefix := range wantPrefixes {
		if metrics[i].Name != prefix && !strings.HasPrefix(metrics[i].Name, prefix) {
			t.Errorf("runNIST metric %d = %q, want %q", i, metrics[i].Name, prefix)
		}
	}
	// p1 and p2 must come from the same serial call: same m in both names.
	if serialM(metrics[5].Name) != serialM(metrics[6].Name) {
		t.Errorf("serial p1/p2 disagree on m: %q vs %q", metrics[5].Name, metrics[6].Name)
	}
	if !strings.HasSuffix(metrics[6].Name, "p2)") {
		t.Errorf("runNIST metric 6 = %q, want serial p2 second", metrics[6].Name)
	}
}

func TestRunStructureMetricOrder(t *testing.T) {
	metrics := runStructure(deterministicSample(1024, 7))
	if len(metrics) != 5 {
		t.Fatalf("runStructure: got %d metrics %v, want 5", len(metrics), metricNames(metrics))
	}

	want := []string{
		"Bit bias (max |z| over 8 positions)",
		"Autocorrelation lags 1-16 (max |z| @lag ",
		"Autocorrelation lags with |z| > 2",
		"Runs z-score",
		"Serial correlation (bytes)",
	}
	for i, prefix := range want {
		if i == 1 {
			if !strings.HasPrefix(metrics[i].Name, prefix) || !strings.HasSuffix(metrics[i].Name, ")") {
				t.Errorf("runStructure metric %d = %q, want prefix %q", i, metrics[i].Name, prefix)
			}
			continue
		}
		if metrics[i].Name != prefix {
			t.Errorf("runStructure metric %d = %q, want %q", i, metrics[i].Name, prefix)
		}
	}
}

func TestSuitesAreDeterministic(t *testing.T) {
	sample := deterministicSample(1024, 7)

	cases := []struct {
		name string
		run  func([]byte) []Metric
	}{
		{"runBasic", runBasic},
		{"runMinEntropy", runMinEntropy},
		{"runNIST", runNIST},
		{"runStructure", runStructure},
	}

	for _, c := range cases {
		first := c.run(sample)
		second := c.run(sample)
		if len(first) != len(second) {
			t.Fatalf("%s: run counts differ: %d vs %d", c.name, len(first), len(second))
		}
		for i := range first {
			if first[i] != second[i] {
				t.Errorf("%s: metric %d differs between runs: %+v vs %+v", c.name, i, first[i], second[i])
			}
		}
	}
}

// serialM extracts the m=<n> token from a serial metric name, returning "" when absent.
func serialM(name string) string {
	rest, ok := strings.CutPrefix(name, "Serial (m=")
	if !ok {
		return ""
	}
	m, _, _ := strings.Cut(rest, ",")
	return m
}
