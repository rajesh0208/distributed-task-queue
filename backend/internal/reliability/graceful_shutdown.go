// File: internal/reliability/graceful_shutdown.go
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

// ShutdownHandler manages graceful shutdown
type ShutdownHandler struct {
	shutdownFuncs []func(context.Context) error
	mu            sync.Mutex
	timeout       time.Duration
}

// NewShutdownHandler creates a new shutdown handler
func NewShutdownHandler(timeout time.Duration) *ShutdownHandler {
	return &ShutdownHandler{
		shutdownFuncs: make([]func(context.Context) error, 0),
		timeout:       timeout,
	}
}

// Register registers a shutdown function
func (s *ShutdownHandler) Register(fn func(context.Context) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shutdownFuncs = append(s.shutdownFuncs, fn)
}

// Shutdown executes all registered shutdown functions
func (s *ShutdownHandler) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	funcs := make([]func(context.Context) error, len(s.shutdownFuncs))
	copy(funcs, s.shutdownFuncs)
	s.mu.Unlock()

	var wg sync.WaitGroup
	errChan := make(chan error, len(funcs))

	for _, fn := range funcs {
		wg.Add(1)
		go func(f func(context.Context) error) {
			defer wg.Done()
			if err := f(ctx); err != nil {
				errChan <- err
			}
		}(fn)
	}

	wg.Wait()
	close(errChan)

	var errors []error
	for err := range errChan {
		errors = append(errors, err)
	}

	if len(errors) > 0 {
		return fmt.Errorf("shutdown errors: %v", errors)
	}

	return nil
}

// WaitForSignal waits for termination signals and triggers shutdown
func (s *ShutdownHandler) WaitForSignal() error {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)

	sig := <-sigChan
	fmt.Printf("Received signal: %v, initiating graceful shutdown...\n", sig)

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	return s.Shutdown(ctx)
}

// GracefulShutdown handles graceful shutdown with timeout
func GracefulShutdown(shutdownFuncs []func(context.Context) error, timeout time.Duration) error {
	handler := NewShutdownHandler(timeout)
	for _, fn := range shutdownFuncs {
		handler.Register(fn)
	}
	return handler.WaitForSignal()
}

