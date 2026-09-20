package whatsapp

import (
	"errors"
	"testing"
	"time"
)

func TestConnectWhatsAppRetriesTransientFailures(t *testing.T) {
	attempts := 0
	sleeps := 0
	errExpected := errors.New("tls handshake failure")

	err := connectWhatsApp(func() error {
		attempts++
		if attempts < 3 {
			return errExpected
		}
		return nil
	}, func(time.Duration) {
		sleeps++
	})

	if err != nil {
		t.Fatalf("connectWhatsApp() error = %v", err)
	}
	if attempts != 3 {
		t.Fatalf("connect attempts = %d, want 3", attempts)
	}
	if sleeps != 2 {
		t.Fatalf("retry sleeps = %d, want 2", sleeps)
	}
}

func TestConnectWhatsAppReturnsLastError(t *testing.T) {
	attempts := 0
	errExpected := errors.New("tls handshake failure")

	err := connectWhatsApp(func() error {
		attempts++
		return errExpected
	}, func(time.Duration) {})

	if !errors.Is(err, errExpected) {
		t.Fatalf("connectWhatsApp() error = %v, want %v", err, errExpected)
	}
	if attempts != connectAttempts {
		t.Fatalf("connect attempts = %d, want %d", attempts, connectAttempts)
	}
}
