package videophash

import "math/bits"

// Hamming returns the Hamming distance between two 64-bit phashes: the number of
// differing bits. This is the ONLY correct way to compare two videophash values
// — parse both encoded hex strings to uint64 with strconv.ParseUint(...,16,64)
// and Hamming-distance them, NEVER compare the encoded strings directly. The
// unpadded FormatUint encoding (see videophash.go) makes a high-zero-nibble hash
// shorter than 16 chars, so a naive string compare is a trap; only the parsed
// uint64 distance is meaningful. stash-box matches phashes within a
// server-configured Hamming tolerance, not by exact equality.
func Hamming(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}
