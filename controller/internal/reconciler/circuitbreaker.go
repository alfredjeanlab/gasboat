package reconciler

import (
	"sync"
	"time"
)

// CircuitBreaker halts pod creation when an anomalous creation rate is
// detected over a rolling window. Unlike the rate limiter (which throttles
// individual creates), the circuit breaker trips and stays open until either
// a cooldown period elapses or it is manually reset.
//
// States:
//   - Closed (normal): pod creation is allowed.
//   - Open (tripped): pod creation is blocked.
type CircuitBreaker struct {
	threshold int           // max creations in window before tripping
	window    time.Duration // rolling window for counting creations
	cooldown  time.Duration // how long the breaker stays open after tripping

	mu         sync.Mutex
	timestamps []time.Time // creation timestamps within the window
	trippedAt  time.Time   // zero value means not tripped
	nowFunc    func() time.Time
}

// NewCircuitBreaker creates a circuit breaker. If threshold <= 0, the breaker
// is disabled and Allow() always returns true.
func NewCircuitBreaker(threshold int, window, cooldown time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		threshold: threshold,
		window:    window,
		cooldown:  cooldown,
		nowFunc:   time.Now,
	}
}

// Allow returns true if pod creation is permitted. Returns false when the
// breaker is open (tripped and cooldown has not elapsed).
func (cb *CircuitBreaker) Allow() bool {
	if cb.threshold <= 0 {
		return true
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := cb.nowFunc()

	// Check if currently tripped.
	if !cb.trippedAt.IsZero() {
		if now.Sub(cb.trippedAt) < cb.cooldown {
			return false // still in cooldown
		}
		// Cooldown elapsed — auto-reset.
		cb.trippedAt = time.Time{}
		cb.timestamps = nil
	}

	return true
}

// Record marks a pod creation. If the number of creations in the window
// exceeds the threshold, the breaker trips.
func (cb *CircuitBreaker) Record() {
	if cb.threshold <= 0 {
		return
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := cb.nowFunc()
	cb.timestamps = append(cb.timestamps, now)
	cb.evictExpired(now)

	if len(cb.timestamps) >= cb.threshold {
		cb.trippedAt = now
	}
}

// IsOpen returns true if the circuit breaker is currently tripped.
func (cb *CircuitBreaker) IsOpen() bool {
	if cb.threshold <= 0 {
		return false
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.trippedAt.IsZero() {
		return false
	}
	if cb.nowFunc().Sub(cb.trippedAt) >= cb.cooldown {
		// Auto-reset on read.
		cb.trippedAt = time.Time{}
		cb.timestamps = nil
		return false
	}
	return true
}

// Reset manually clears the tripped state, allowing creation to resume
// immediately without waiting for cooldown.
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.trippedAt = time.Time{}
	cb.timestamps = nil
}

// evictExpired removes timestamps older than the window. Must hold mu.
func (cb *CircuitBreaker) evictExpired(now time.Time) {
	cutoff := now.Add(-cb.window)
	i := 0
	for i < len(cb.timestamps) && cb.timestamps[i].Before(cutoff) {
		i++
	}
	if i > 0 {
		cb.timestamps = cb.timestamps[i:]
	}
}
