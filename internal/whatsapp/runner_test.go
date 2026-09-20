package whatsapp

import (
	"context"
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

func TestConnectWhatsAppStopsWhenContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	errExpected := errors.New("tls handshake failure")

	err := connectWhatsAppContext(ctx, func() error {
		attempts++
		cancel()
		return errExpected
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("connectWhatsAppContext() error = %v, want context canceled", err)
	}
	if attempts != 1 {
		t.Fatalf("connect attempts = %d, want 1", attempts)
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
