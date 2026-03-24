package main

import "strings"

func applyConfigDefaults(cfg *Config) {
	if cfg == nil {
		return
	}
	if strings.TrimSpace(cfg.StateFile) == "" {
		cfg.StateFile = "lifebase-state.json"
	}
	if strings.TrimSpace(cfg.SecretsFile) == "" {
		cfg.SecretsFile = "lifebase-secrets.yaml"
	}
}
