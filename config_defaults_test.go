package main

import "testing"

func TestApplyConfigDefaults(t *testing.T) {
	cfg := Config{}
	applyConfigDefaults(&cfg)

	if cfg.StateFile != "lifebase-state.json" {
		t.Fatalf("StateFile = %q, want lifebase-state.json", cfg.StateFile)
	}
	if cfg.SecretsFile != "lifebase-secrets.yaml" {
		t.Fatalf("SecretsFile = %q, want lifebase-secrets.yaml", cfg.SecretsFile)
	}
}

func TestApplyConfigDefaultsKeepsExplicitValues(t *testing.T) {
	cfg := Config{
		StateFile:   "custom-state.json",
		SecretsFile: "custom-secrets.yaml",
	}
	applyConfigDefaults(&cfg)

	if cfg.StateFile != "custom-state.json" {
		t.Fatalf("StateFile = %q, want custom-state.json", cfg.StateFile)
	}
	if cfg.SecretsFile != "custom-secrets.yaml" {
		t.Fatalf("SecretsFile = %q, want custom-secrets.yaml", cfg.SecretsFile)
	}
}
