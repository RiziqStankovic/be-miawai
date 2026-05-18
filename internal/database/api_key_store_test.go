package database

import (
	"strings"
	"testing"
)

func TestGenerateUserAPIKeyShapeAndHash(t *testing.T) {
	plainKey, prefix, hash, err := generateUserAPIKey()
	if err != nil {
		t.Fatalf("generateUserAPIKey() error = %v", err)
	}
	if !strings.HasPrefix(plainKey, "miaw_") {
		t.Fatalf("plain key prefix = %q, want miaw_", plainKey)
	}
	if prefix == "" || !strings.HasPrefix(plainKey, prefix) {
		t.Fatalf("key prefix %q is not a prefix of generated key", prefix)
	}
	if hash == "" || hash == plainKey {
		t.Fatalf("hash should be non-empty and should not equal plaintext")
	}
	if got := hashUserAPIKey(plainKey); got != hash {
		t.Fatalf("hashUserAPIKey() = %q, want %q", got, hash)
	}
}

func TestHashUserAPIKeyTrimsWhitespace(t *testing.T) {
	key := "miaw_test_key"
	if hashUserAPIKey("  "+key+" \n") != hashUserAPIKey(key) {
		t.Fatal("hashUserAPIKey should trim surrounding whitespace")
	}
}
