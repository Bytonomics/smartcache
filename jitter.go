package smartcache

import (
	"math/rand/v2"
	"time"
)

// defaultJitterFraction is the fraction of the base TTL that jitter may shave
// off when a cache does not override it. 0.10 => an effective TTL in
// [0.9*base, base].
const defaultJitterFraction = 0.10

// jitterRand is the package-level randomness seam. It returns a float64 in
// [0.0, 1.0). Production uses math/rand/v2's global source; unit tests override
// it (via SetJitterRandForTest in export_test.go) for deterministic expiry
// assertions.
var jitterRand = rand.Float64 //nolint:gosec // non-cryptographic TTL jitter; math/rand/v2 is intentional

// applyJitter returns a downward-jittered TTL: base - jitterRand()*fraction*base.
// It returns base unchanged when base <= 0 (infinite/none) or fraction <= 0
// (jitter disabled). The result is always in [(1-fraction)*base, base] and is
// never negative, since jitterRand() is in [0,1) and fraction is in [0,1).
func applyJitter(base time.Duration, fraction float64) time.Duration {
	if base <= 0 || fraction <= 0 {
		return base
	}
	delta := time.Duration(jitterRand() * fraction * float64(base))
	return base - delta
}
