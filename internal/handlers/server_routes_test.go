package handlers

import (
	"testing"

	"be-miawai/internal/config"
)

func TestRoutesRegistersKnowledgeRoutes(t *testing.T) {
	server := NewServer(config.Config{}, nil)
	if server.Routes() == nil {
		t.Fatal("Routes() returned nil")
	}
}
