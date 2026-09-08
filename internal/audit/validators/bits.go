package validators

// ToBits expands a byte slice into a slice of bits (0 or 1), MSB first.
func ToBits(data []byte) []uint8 {
	bits := make([]uint8, 0, len(data)*8)
	for _, b := range data {
		for i := 7; i >= 0; i-- {
			bits = append(bits, uint8((b>>uint(i))&1))
		}
	}
	return bits
}

// countOnes counts the number of set bits.
func countOnes(bits []uint8) int {
	n := 0
	for _, b := range bits {
		n += int(b)
	}
	return n
}