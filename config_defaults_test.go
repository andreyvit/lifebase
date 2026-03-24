package main

import "testing"

func TestDefaultConfigIncludesStateAndSecretsFiles(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.StateFile != "lifebase-state.json" {
		t.Fatalf("StateFile = %q, want lifebase-state.json", cfg.StateFile)
	}
	if cfg.SecretsFile != "lifebase-secrets.yaml" {
		t.Fatalf("SecretsFile = %q, want lifebase-secrets.yaml", cfg.SecretsFile)
	}
}
