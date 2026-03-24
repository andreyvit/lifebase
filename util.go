package main

import (
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

// retry runs action up to 5 times with 2s delays until it returns nil.
// For now we retry on all errors for simplicity.
func retry(action func() error) error {
	const attempts = 5
	const delay = 2 * time.Second
	var err error
	for i := range attempts {
		err = action()
		if err == nil {
			return nil
		}
		if i < attempts-1 {
			log.Printf("retrying: %v", err)
			time.Sleep(delay)
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
