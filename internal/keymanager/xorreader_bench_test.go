package keymanager

import (
	"crypto/rand"
	"fmt"
	"io"
	"sync"
	"testing"
)

// Benchmarks for the entropy reader that seeds RSA key generation. GenerateKey
// pulls entropyPerKey bytes through this reader on every request, so its
// per-byte cost is on the request path rather than on a background batch.
//
// Run with:
//
//	go test -bench=XORReader ./internal/keymanager/
//	go test -bench=. -benchmem ./internal/keymanager/

// benchSeedLengths covers the seed lengths the caller actually uses: a full
// digest and the short seeds that entropyPerKey-sized reads can produce.
var benchSeedLengths = []int{0, 16, 32, 64}

// BenchmarkXORReaderRead measures a single sequential read per size. The
// crypto/rand read stays outside the mutex, so this is dominated by the CSPRNG
// call with the XOR loop layered on top.
func BenchmarkXORReaderRead(b *testing.B) {
	sizes := []int{128, 2048, 1 << 16}

	for _, seedLen := range benchSeedLengths {
		for _, size := range sizes {
			b.Run(sizeName(seedLen, size), func(b *testing.B) {
				seed := make([]byte, seedLen)
				if seedLen > 0 {
					_, _ = rand.Read(seed)
				}
				r := newXORReader(seed)

				buf := make([]byte, size)
				b.SetBytes(int64(size))
				b.ReportAllocs()

				for i := 0; i < b.N; i++ {
					if _, err := r.Read(buf); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkXORReaderParallel is the case the mutex was added for: several
// goroutines drawing from one reader, which is how CIRCL is expected to use it
// for ML-KEM/ML-DSA. Uncontended sequential throughput is reported by
// BenchmarkXORReaderRead, so the gap between the two is the cost of sharing.
func BenchmarkXORReaderParallel(b *testing.B) {
	sizes := []int{2048, 1 << 16}

	for _, seedLen := range benchSeedLengths {
		for _, size := range sizes {
			for _, procs := range []int{2, 8} {
				b.Run(fmt.Sprintf("%s/p%d", sizeName(seedLen, size), procs), func(b *testing.B) {
					seed := make([]byte, seedLen)
					if seedLen > 0 {
						_, _ = rand.Read(seed)
					}

					b.SetBytes(int64(size) * int64(procs))
					b.ReportAllocs()
					b.RunParallel(func(pb *testing.PB) {
						r := newXORReader(seed)
						buf := make([]byte, size)
						for pb.Next() {
							if _, err := r.Read(buf); err != nil {
								b.Fatal(err)
							}
						}
					})
				})
			}
		}
	}
}

// BenchmarkXORReaderAgainstRand is the baseline the XOR reader is read against.
// A seed length of 0 takes the early return, so this measures crypto/rand plus
// the function call and nothing else.
func BenchmarkXORReaderAgainstRand(b *testing.B) {
	size := 2048
	buf := make([]byte, size)

	b.SetBytes(int64(size))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := rand.Read(buf); err != nil {
			b.Fatal(err)
		}
	}

	b.Run("io.Reader/crypto-rand", func(b *testing.B) {
		var r io.Reader = rand.Reader
		buf := make([]byte, size)
		b.SetBytes(int64(size))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := r.Read(buf); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// TestXORReaderConcurrentKeepsSeedOffsetContiguous is a test, not a
// benchmark. Concurrent readers must neither lose nor duplicate seed offsets,
// which is the invariant the mutex protects, and only -race proves it. Kept
// next to the parallel benchmark so the two stay in step.
//
// Run with:
//
//	go test -race -run=XORReader ./internal/keymanager/
func TestXORReaderConcurrentKeepsSeedOffsetContiguous(t *testing.T) {
	const (
		seedLen = 32
		readers = 8
		reads   = 64
	)

	seed := make([]byte, seedLen)
	for i := range seed {
		seed[i] = byte(i + 1)
	}

	x := newXORReader(seed).(*xorReader)

	var mu sync.Mutex
	var consumed int64

	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 17)
			for j := 0; j < reads; j++ {
				n, err := x.Read(buf)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				consumed += int64(n)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// The offset advances once per byte actually XOR-ed, so it must equal the
	// total returned by the reads. A lost update under the mutex would leave it
	// short; a duplicated one would leave it long.
	if int64(x.offset) != consumed {
		t.Fatalf("seed offset = %d, want %d (total bytes returned)", x.offset, consumed)
	}
	if want := int64(readers * reads * 17); consumed != want {
		t.Fatalf("bytes consumed = %d, want %d", consumed, want)
	}
}

func sizeName(seedLen, size int) string {
	return fmt.Sprintf("seed%d/read%d", seedLen, size)
}
