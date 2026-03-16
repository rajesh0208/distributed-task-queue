// Package reliability provides circuit breakers, retry logic, deduplication,
// health checks, and graceful shutdown for the distributed task queue.
//
// This file (circuitbreaker.go) implements the circuit breaker pattern — a
// protective wrapper around calls to external or unreliable services.
//
// What is a circuit breaker and why does it exist?
//   Without a circuit breaker, every request to a failing dependency (database,
//   external API, downstream service) results in a slow timeout, consuming
//   goroutines, connection slots, and memory. Under load, one slow dependency
//   can cascade and take down the entire system.
//   A circuit breaker wraps calls and "opens" (stops forwarding requests) when
//   failures exceed a threshold, giving the downstream time to recover. After a
//   timeout period, it allows a small number of test requests through to check
//   if the service is healthy again. This prevents cascading failures.
//
// Three-state machine:
//
//   CLOSED (normal operation)
//     Requests pass through normally. Failures are counted. When consecutive
//     failures exceed the threshold (default: 5), the circuit OPENS.
//
//   OPEN (failing — all requests rejected immediately)
//     No requests are forwarded; Execute() returns ErrCircuitOpen instantly.
//     After the timeout duration (default: 60 s), the circuit enters HALF-OPEN.
//     Callers that get ErrCircuitOpen should requeue the task, not fail it.
//
//   HALF-OPEN (testing recovery)
//     A limited number of test requests (maxRequests, default: 1) are allowed
//     through to probe whether the service has recovered.
//     — If the test requests succeed → circuit returns to CLOSED.
//     — If any test request fails → circuit returns to OPEN immediately.
//
// State transitions:
//   CLOSED  ──(5 consecutive failures)──► OPEN
//   OPEN    ──(timeout expires)──────────► HALF-OPEN
//   HALF-OPEN ──(test succeeds)──────────► CLOSED
//   HALF-OPEN ──(test fails)─────────────► OPEN
//
// It connects to: (standalone — no internal imports)
// Called by:
//   - cmd/worker/main.go — wraps image download and processing calls
//   - internal/processor — wraps external HTTP fetches
package reliability

import (
	"errors"
	"sync"
	"time"
)

// Sentinel errors returned by Execute and beforeRequest.
// Callers use errors.Is() to distinguish these from actual operation errors.
var (
	// ErrCircuitOpen is returned when the circuit is OPEN and the request is
	// blocked entirely. Callers should treat this as a temporary failure and
	// requeue the task rather than marking it failed permanently.
	ErrCircuitOpen = errors.New("circuit breaker is open")

	// ErrTooManyRequests is returned in HALF-OPEN state when more test requests
	// arrive than maxRequests allows. Extra requests are blocked to avoid
	// overwhelming a recovering service.
	ErrTooManyRequests = errors.New("too many requests")
)

// State represents one of the three circuit breaker states.
// Using an int (rather than a string) makes comparisons O(1).
type State int

const (
	// StateClosed means the circuit is operating normally — requests pass through.
	StateClosed State = iota

	// StateHalfOpen means the circuit is testing recovery with limited traffic.
	StateHalfOpen

	// StateOpen means the circuit is blocking all requests to protect the system.
	StateOpen
)

// String returns the human-readable state name for logging and monitoring.
func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateHalfOpen:
		return "half-open"
	case StateOpen:
		return "open"
	default:
		return "unknown"
	}
}

// CircuitBreaker is the core state machine. It is safe for concurrent use by
// multiple goroutines (all state mutations are guarded by mutex).
//
// Fields explained:
//
//	maxRequests   — maximum number of test requests allowed in HALF-OPEN state.
//	                Once this many requests succeed consecutively, the circuit
//	                closes. Default: 1 (conservative — one success is enough).
//	interval      — when > 0, the counts window resets after this duration in
//	                CLOSED state. Useful for "forget old failures after N seconds".
//	                When 0, counts accumulate until a state change.
//	timeout       — how long the circuit stays OPEN before trying HALF-OPEN.
//	                Default: 60 s. Should be long enough for the dependency to
//	                recover, but not so long that healthy callers are blocked.
//	readyToTrip   — pluggable predicate: given current Counts, returns true when
//	                the circuit should OPEN. Default: ConsecutiveFailures > 5.
//	onStateChange — optional callback invoked on every state transition; used
//	                for Prometheus metrics and alerting.
//	mutex         — protects all fields below from concurrent mutation.
//	state         — current state: StateClosed, StateHalfOpen, or StateOpen.
//	generation    — monotonically increasing counter; bumped on every state
//	                change. afterRequest uses it to discard results from a
//	                previous generation (stale goroutine reporting results from
//	                before the last state change).
//	counts        — rolling request/success/failure counters; reset on each
//	                new generation.
//	expiry        — the wall-clock time when the current state expires:
//	                CLOSED   → expiry of the counts interval window (0 = never)
//	                OPEN     → time to attempt HALF-OPEN
//	                HALF-OPEN → no expiry (counts determine the next transition)
type CircuitBreaker struct {
	maxRequests   uint32
	interval      time.Duration
	timeout       time.Duration
	readyToTrip   func(counts Counts) bool
	onStateChange func(name string, from State, to State)

	mutex      sync.Mutex
	state      State
	generation uint64
	counts     Counts
	expiry     time.Time
}

// Counts tracks request outcomes for the current generation window.
//
// Fields explained:
//
//	Requests             — total requests attempted (used in HALF-OPEN for maxRequests check).
//	TotalSuccesses       — cumulative successes since last reset.
//	TotalFailures        — cumulative failures since last reset.
//	ConsecutiveSuccesses — unbroken run of successes; drives HALF-OPEN → CLOSED.
//	ConsecutiveFailures  — unbroken run of failures; drives CLOSED → OPEN via readyToTrip.
type Counts struct {
	Requests             uint32
	TotalSuccesses       uint32
	TotalFailures        uint32
	ConsecutiveSuccesses uint32
	ConsecutiveFailures  uint32
}

// Settings is the constructor configuration for CircuitBreaker.
// All fields have defaults applied in NewCircuitBreaker if left zero.
//
// Fields explained:
//
//	Name          — identifier for logging/metrics (e.g. "image-downloader").
//	MaxRequests   — max test probes in HALF-OPEN; 0 → defaults to 1.
//	Interval      — window duration for counting in CLOSED state; 0 → no reset.
//	Timeout       — how long to stay OPEN before testing recovery; 0 → 60 s.
//	ReadyToTrip   — custom trip predicate; nil → trip on > 5 consecutive failures.
//	OnStateChange — callback fired on CLOSED↔OPEN↔HALF-OPEN transitions.
type Settings struct {
	Name          string
	MaxRequests   uint32
	Interval      time.Duration
	Timeout       time.Duration
	ReadyToTrip   func(counts Counts) bool
	OnStateChange func(name string, from State, to State)
}

// NewCircuitBreaker creates a CircuitBreaker with the given settings.
// Applies safe defaults for any zero/nil setting fields.
//
// Why 5 consecutive failures as the default trip threshold?
//   5 failures in a row is a strong signal of a real outage (not transient blips).
//   A lower threshold (e.g. 1) would trip on normal request variability;
//   a higher threshold (e.g. 20) would let the system suffer longer before protection kicks in.
//
// Why 60 s as the default OPEN timeout?
//   Most transient failures (restart, brief network issue) resolve within seconds.
//   60 s gives the dependency time to recover while keeping the window short
//   enough to not block legitimate traffic for too long.
//
// Parameters:
//   settings — configuration; use zero values to accept defaults.
//
// Called by: cmd/worker/main.go, internal/processor/image_processor.go.
func NewCircuitBreaker(settings Settings) *CircuitBreaker {
	cb := &CircuitBreaker{
		maxRequests:   settings.MaxRequests,
		interval:      settings.Interval,
		timeout:       settings.Timeout,
		readyToTrip:   settings.ReadyToTrip,
		onStateChange: settings.OnStateChange,
	}

	// Default: allow 1 test request in HALF-OPEN. A single success is enough
	// to confirm recovery; multiple probes risk hammering a fragile service.
	if cb.maxRequests == 0 {
		cb.maxRequests = 1
	}

	// Default: no rolling window in CLOSED state. Failures accumulate until
	// a state change resets them.
	if cb.interval == 0 {
		cb.interval = time.Duration(0) * time.Second
	}

	// Default: 60 s OPEN timeout before trying HALF-OPEN.
	if cb.timeout == 0 {
		cb.timeout = 60 * time.Second
	}

	// Default trip predicate: open the circuit after 5 consecutive failures.
	// This can be customised for different dependency types — e.g. a database
	// might use a higher threshold than a rate-limited external API.
	if cb.readyToTrip == nil {
		cb.readyToTrip = func(counts Counts) bool {
			return counts.ConsecutiveFailures > 5
		}
	}

	// Initialise the first generation with zero counts and set the initial expiry.
	cb.toNewGeneration(time.Now())

	return cb
}

// Execute runs fn inside the circuit breaker protection.
//
// Flow:
//  1. beforeRequest: check state; reject immediately if OPEN or HALF-OPEN + saturated.
//  2. Run fn.
//  3. afterRequest: record success or failure; potentially trigger a state transition.
//  4. If fn panics, record failure and re-panic (so the caller's deferred cleanup runs).
//
// Parameters:
//   fn — the fallible operation to protect (e.g. HTTP fetch, DB write).
//
// Returns:
//   error — ErrCircuitOpen if OPEN, ErrTooManyRequests if HALF-OPEN saturated,
//           or fn's own error. nil on success.
//
// Called by: processor/image_processor.go when downloading images.
func (cb *CircuitBreaker) Execute(fn func() error) error {
	// Capture the generation BEFORE running fn. If the state changes while fn
	// is running (another goroutine trips the breaker), afterRequest will detect
	// the generation mismatch and discard the result.
	generation, err := cb.beforeRequest()
	if err != nil {
		// Circuit is OPEN or HALF-OPEN is saturated — do not run fn at all.
		return err
	}

	// Catch panics so we still record a failure if fn panics.
	// Re-panic so the caller's defer/recover chain is not broken.
	defer func() {
		if r := recover(); r != nil {
			cb.afterRequest(generation, false) // count as failure
			panic(r)                           // re-panic so callers can handle it
		}
	}()

	err = fn()
	cb.afterRequest(generation, err == nil) // true = success, false = failure
	return err
}

// beforeRequest checks whether the current state allows a new request.
// Called under mutex (acquired inside this function).
//
// Returns:
//   uint64 — the current generation (used by afterRequest to detect stale results).
//   error  — ErrCircuitOpen or ErrTooManyRequests if the request is blocked.
func (cb *CircuitBreaker) beforeRequest() (uint64, error) {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	now := time.Now()
	state, generation := cb.currentState(now) // may transition OPEN→HALF-OPEN

	if state == StateOpen {
		// Circuit is OPEN — all requests are blocked to protect the downstream.
		return generation, ErrCircuitOpen
	} else if state == StateHalfOpen && cb.counts.Requests >= cb.maxRequests {
		// HALF-OPEN but we've already sent the maximum number of test probes.
		// Extra requests are blocked until the probes complete and a decision is made.
		return generation, ErrTooManyRequests
	}

	// Increment the request counter — used by HALF-OPEN to enforce maxRequests.
	cb.counts.Requests++
	return generation, nil
}

// afterRequest records the result of a completed request and drives state
// transitions. If the generation has changed since beforeRequest, the result
// is discarded (it belongs to a previous state window).
//
// Parameters:
//   before  — the generation value captured in beforeRequest.
//   success — true if fn returned nil, false otherwise.
func (cb *CircuitBreaker) afterRequest(before uint64, success bool) {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	now := time.Now()
	state, generation := cb.currentState(now)
	if generation != before {
		// The circuit changed state while fn was running (another goroutine tripped
		// or reset it). Discard this result — it belongs to the old generation.
		return
	}

	if success {
		cb.onSuccess(state, now)
	} else {
		cb.onFailure(state, now)
	}
}

// onSuccess updates counters after a successful call and potentially closes
// the circuit when HALF-OPEN test requests all succeed.
func (cb *CircuitBreaker) onSuccess(state State, now time.Time) {
	switch state {
	case StateClosed:
		// Normal success in closed state — reset consecutive failure streak.
		cb.counts.TotalSuccesses++
		cb.counts.ConsecutiveSuccesses++
		cb.counts.ConsecutiveFailures = 0 // break the failure run

	case StateHalfOpen:
		// A test probe succeeded in HALF-OPEN state.
		cb.counts.TotalSuccesses++
		cb.counts.ConsecutiveSuccesses++
		cb.counts.ConsecutiveFailures = 0
		if cb.counts.ConsecutiveSuccesses >= cb.maxRequests {
			// All required test probes passed — the service has recovered.
			// Transition back to CLOSED and start accepting traffic normally.
			cb.setState(StateClosed, now)
		}
	}
}

// onFailure updates counters after a failed call and potentially opens the circuit.
func (cb *CircuitBreaker) onFailure(state State, now time.Time) {
	switch state {
	case StateClosed:
		// Failure in normal operation — increment and check the trip predicate.
		cb.counts.TotalFailures++
		cb.counts.ConsecutiveFailures++
		cb.counts.ConsecutiveSuccesses = 0 // break the success run
		if cb.readyToTrip(cb.counts) {
			// Threshold exceeded — open the circuit to stop the bleeding.
			cb.setState(StateOpen, now)
		}

	case StateHalfOpen:
		// Even one failure in HALF-OPEN means the service is still unhealthy.
		// Re-open immediately to avoid overwhelming a partially recovered service.
		cb.setState(StateOpen, now)
	}
}

// currentState returns the effective state at the given time, handling any
// automatic transitions (e.g. OPEN → HALF-OPEN when the timeout expires).
// Must be called with cb.mutex held.
//
// Returns:
//   State  — the current (potentially just-transitioned) state.
//   uint64 — the current generation number.
func (cb *CircuitBreaker) currentState(now time.Time) (State, uint64) {
	switch cb.state {
	case StateClosed:
		// If the counts interval is set and has elapsed, start a new generation.
		// This "forgets" old failures so a brief past outage doesn't keep the
		// circuit hair-trigger after the service recovered.
		if !cb.expiry.IsZero() && cb.expiry.Before(now) {
			cb.toNewGeneration(now)
		}

	case StateOpen:
		// If the OPEN timeout has elapsed, transition to HALF-OPEN to probe recovery.
		if cb.expiry.Before(now) {
			cb.setState(StateHalfOpen, now)
		}
	}
	return cb.state, cb.generation
}

// setState transitions the circuit to a new state, starts a new generation
// (clearing counts), sets the appropriate expiry, and fires onStateChange.
// Must be called with cb.mutex held.
func (cb *CircuitBreaker) setState(state State, now time.Time) {
	if cb.state == state {
		return // no-op if already in the target state
	}

	prev := cb.state
	cb.state = state

	cb.toNewGeneration(now) // reset counts for the new state window

	if cb.onStateChange != nil {
		// Notify observers (metrics, alerting) of the transition.
		cb.onStateChange(cb.state.String(), prev, state)
	}
}

// toNewGeneration resets counts and advances the generation counter.
// Sets the appropriate expiry for the new state:
//   CLOSED   → expiry of the interval window (0 = never reset)
//   OPEN     → expiry = now + timeout (when to try HALF-OPEN)
//   HALF-OPEN → no expiry (transitions are count-driven, not time-driven)
//
// Must be called with cb.mutex held.
func (cb *CircuitBreaker) toNewGeneration(now time.Time) {
	cb.generation++ // invalidate results from the previous generation
	cb.counts = Counts{} // reset all counters for the new window

	var zero time.Time
	switch cb.state {
	case StateClosed:
		if cb.interval == 0 {
			cb.expiry = zero // no rolling window — counts never auto-reset in CLOSED
		} else {
			cb.expiry = now.Add(cb.interval) // reset window after interval
		}
	case StateOpen:
		// Set the countdown until we attempt HALF-OPEN.
		cb.expiry = now.Add(cb.timeout)
	default: // StateHalfOpen
		cb.expiry = zero // no time-based expiry in HALF-OPEN
	}
}

// State returns the current circuit state. Thread-safe.
// Used by health checks and Prometheus metrics collectors.
//
// Called by: reliability/health.go, monitoring/metrics.go.
func (cb *CircuitBreaker) State() State {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	now := time.Now()
	state, _ := cb.currentState(now)
	return state
}
