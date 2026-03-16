// Package reliability — see circuitbreaker.go for package-level documentation.
//
// This file (graceful_shutdown.go) handles operating-system termination signals
// and cleanly drains in-flight operations before the process exits.
//
// What is graceful shutdown and why not just kill the process?
//   A hard kill (SIGKILL or os.Exit) stops the process immediately. Any tasks
//   currently being processed are abandoned mid-flight:
//     - The database row stays in "processing" status forever (a stuck task).
//     - Open HTTP connections are abruptly closed (clients get a TCP RST).
//     - Redis connection pools leak server-side resources.
//   Graceful shutdown:
//     1. Stops accepting new work (cancel the context).
//     2. Lets in-flight operations complete within a timeout.
//     3. Closes connections (DB pool, Redis, HTTP server) in order.
//     4. Only then exits.
//
// SIGTERM vs SIGKILL:
//   SIGTERM — "please stop soon". The OS sends this when `docker stop`, `kubectl
//              delete pod`, or `systemctl stop` is invoked. Programs can catch
//              SIGTERM and run cleanup. This is what we handle here.
//   SIGKILL — "stop NOW, no cleanup". Cannot be caught or ignored. The OS sends
//              this if SIGTERM is not handled within the grace period.
//              Docker sends SIGTERM, waits 10 s, then sends SIGKILL.
//
// Shutdown timeout:
//   If registered shutdown functions take longer than timeout, the context is
//   cancelled and they receive ctx.Err() == context.DeadlineExceeded.
//   Set timeout to the maximum acceptable in-flight task duration. If a task
//   might take 60 s and timeout is 10 s, that task will be interrupted.
//   The recommendation is: timeout = max_task_duration + 5 s buffer.
//
// Order of shutdown (recommended registration order in main.go):
//   1. Stop accepting new HTTP requests (Fiber app.Shutdown).
//   2. Cancel the worker context (stop pulling new tasks from the broker).
//   3. Wait for in-flight tasks to finish (worker WaitGroup).
//   4. Close the broker connection (Redis streams).
//   5. Close the storage layer (DB pool + cache).
//   Note: Register() appends to a slice; shutdown runs all in parallel.
//         If sequential ordering is needed, register a single function that
//         calls them in order.
//
// It connects to: (standalone — no internal imports)
// Called by:
//   - cmd/api/main.go    — WaitForSignal blocks main until SIGTERM/SIGINT
//   - cmd/worker/main.go — WaitForSignal blocks main until SIGTERM/SIGINT
package reliability

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// ShutdownHandler manages a list of cleanup functions and executes them
// concurrently when a termination signal is received.
//
// Fields explained:
//
//	shutdownFuncs — ordered list of cleanup functions registered by the
//	                application. All are run concurrently when Shutdown() is called.
//	mu            — mutex protecting shutdownFuncs from concurrent Register calls.
//	timeout       — maximum total time allowed for all shutdown functions to complete.
//	                After this duration the context is cancelled, signalling
//	                functions to abort. The process exits after Shutdown() returns.
type ShutdownHandler struct {
	shutdownFuncs []func(context.Context) error
	mu            sync.Mutex
	timeout       time.Duration
}

// NewShutdownHandler creates a ShutdownHandler with the given total timeout.
//
// Parameters:
//   timeout — how long to wait for all shutdown functions to complete before
//             the context is cancelled. Recommendation: max_task_duration + 5 s.
//
// Returns:
//   *ShutdownHandler — ready to have functions registered.
//
// Called by: cmd/api/main.go, cmd/worker/main.go.
func NewShutdownHandler(timeout time.Duration) *ShutdownHandler {
	return &ShutdownHandler{
		shutdownFuncs: make([]func(context.Context) error, 0),
		timeout:       timeout,
	}
}

// Register adds a cleanup function to run during shutdown.
// Functions are called concurrently in Shutdown() — if order matters,
// register a single function that calls them sequentially.
//
// Parameters:
//   fn — cleanup function; receives a context with the shutdown timeout;
//         should return promptly when ctx is cancelled.
//         Examples: app.Shutdown, broker.Close, db.Close.
//
// Called by: cmd/api/main.go, cmd/worker/main.go (once per dependency).
func (s *ShutdownHandler) Register(fn func(context.Context) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shutdownFuncs = append(s.shutdownFuncs, fn)
}

// Shutdown runs all registered cleanup functions concurrently within the timeout.
//
// How it works:
//   1. Snapshot the registered functions under lock (so Register can still be
//      called concurrently without a data race).
//   2. Launch each function in its own goroutine.
//   3. Wait for all to complete via WaitGroup.
//   4. Collect any errors and return them joined.
//
// What if a function exceeds the timeout?
//   The context passed to each function carries the deadline. Well-behaved
//   functions check ctx.Done() and abort early. Functions that ignore the
//   context will be left running as goroutines; the process will still exit
//   after WaitForSignal returns.
//
// Parameters:
//   ctx — context with the shutdown timeout deadline.
//
// Returns:
//   error — combined errors from all functions that failed; nil if all succeeded.
//
// Called by: WaitForSignal (this file) and directly by callers with their own context.
func (s *ShutdownHandler) Shutdown(ctx context.Context) error {
	// Snapshot the function list under lock to avoid a data race if Register
	// is called concurrently with Shutdown (unlikely but possible in tests).
	s.mu.Lock()
	funcs := make([]func(context.Context) error, len(s.shutdownFuncs))
	copy(funcs, s.shutdownFuncs)
	s.mu.Unlock()

	var wg sync.WaitGroup
	errChan := make(chan error, len(funcs)) // buffered so goroutines never block on send

	for _, fn := range funcs {
		wg.Add(1)
		// Run each shutdown function in its own goroutine so they proceed in
		// parallel. For example, the HTTP server can drain while the broker
		// connection is closing simultaneously.
		go func(f func(context.Context) error) {
			defer wg.Done()
			if err := f(ctx); err != nil {
				// Non-nil error: collect it for the combined return value.
				errChan <- err
			}
		}(fn)
	}

	wg.Wait()       // block until all goroutines complete or context expires
	close(errChan)  // safe to close after wg.Wait; no more writers

	// Collect all errors into a slice for reporting.
	var errors []error
	for err := range errChan {
		errors = append(errors, err)
	}

	if len(errors) > 0 {
		// Return a combined error so the caller (main.go) can log all failures.
		return fmt.Errorf("shutdown errors: %v", errors)
	}

	return nil
}

// WaitForSignal blocks until SIGTERM or SIGINT is received, then calls Shutdown
// with the configured timeout and returns any errors.
//
// Why both SIGTERM and SIGINT?
//   SIGTERM is sent by Docker, Kubernetes, and systemd when stopping a service.
//   SIGINT (Ctrl+C) is sent by the terminal during development.
//   Handling both means the same graceful shutdown code runs in both production
//   and local testing.
//
// Typical usage in main.go:
//
//	handler := reliability.NewShutdownHandler(30 * time.Second)
//	handler.Register(func(ctx context.Context) error { return app.ShutdownWithContext(ctx) })
//	handler.Register(func(ctx context.Context) error { return broker.Close() })
//	handler.Register(func(ctx context.Context) error { return storage.Close() })
//	if err := handler.WaitForSignal(); err != nil {
//	    log.Printf("shutdown error: %v", err)
//	}
//
// Returns:
//   error — from Shutdown(); nil if all cleanup functions succeeded.
//
// Called by: cmd/api/main.go, cmd/worker/main.go (blocks the main goroutine).
func (s *ShutdownHandler) WaitForSignal() error {
	// Buffered channel prevents the goroutine sending the signal from blocking
	// if this goroutine is not yet in the receive statement.
	sigChan := make(chan os.Signal, 1)

	// signal.Notify registers the channel to receive the specified signals.
	// os.Interrupt = SIGINT (Ctrl+C); syscall.SIGTERM = Docker/k8s stop signal.
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)

	// Block here until a termination signal arrives.
	sig := <-sigChan
	fmt.Printf("Received signal: %v, initiating graceful shutdown...\n", sig)

	// Create a timeout context. Shutdown functions must complete within this
	// deadline; if they don't, they receive ctx.Err() = context.DeadlineExceeded.
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel() // always release the timer goroutine

	return s.Shutdown(ctx)
}

// GracefulShutdown is a convenience wrapper that creates a ShutdownHandler,
// registers all given functions, and blocks until a signal is received.
// Use ShutdownHandler directly for more control (e.g. adding functions later).
//
// Parameters:
//   shutdownFuncs — slice of cleanup functions to run concurrently.
//   timeout       — maximum allowed shutdown duration.
//
// Returns:
//   error — from Shutdown(); nil if all cleanup functions succeeded.
//
// Called by: cmd/api/main.go (simple single-call shutdown).
func GracefulShutdown(shutdownFuncs []func(context.Context) error, timeout time.Duration) error {
	handler := NewShutdownHandler(timeout)
	for _, fn := range shutdownFuncs {
		handler.Register(fn)
	}
	return handler.WaitForSignal()
}
