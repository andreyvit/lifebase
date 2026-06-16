package main

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"
)

func ensure(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		log.Fatal(err)
	}
	return v
}

// permanentError wraps an error to signal retry() must not retry it.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// permanent marks err as non-retryable. retry() returns it immediately.
func permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

// retry runs action with exponential backoff (1s, 2s, 4s, 8s) between up to 5 attempts.
// Errors wrapped with permanent() are returned immediately.
func retry(action func() error) error {
	return retryCtx(context.Background(), action)
}

// retryCtx is retry but also aborts on context cancellation.
func retryCtx(ctx context.Context, action func() error) error {
	const attempts = 5
	const baseDelay = 1 * time.Second
	const maxDelay = 30 * time.Second
	var err error
	for i := 0; i < attempts; i++ {
		err = action()
		if err == nil {
			return nil
		}
		var perm *permanentError
		if errors.As(err, &perm) {
			return err
		}
		if ctx.Err() != nil {
			return err
		}
		if i < attempts-1 {
			delay := baseDelay << i
			if delay > maxDelay {
				delay = maxDelay
			}
			log.Printf("retrying after %s: %v", delay, err)
			select {
			case <-ctx.Done():
				return err
			case <-time.After(delay):
			}
		}
	}
	return err
}

func Subst(s string, values map[string]string) string {
	for k, v := range values {
		s = strings.ReplaceAll(s, k, v)
	}
	return s
}
