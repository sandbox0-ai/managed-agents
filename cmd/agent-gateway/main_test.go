package main

import "testing"

func TestLoadConfigDefaultsSandbox0BaseURLToGlobal(t *testing.T) {
	t.Setenv("MANAGED_AGENT_DATABASE_URL", "postgres://example")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Sandbox0BaseURL != defaultSandbox0BaseURL {
		t.Fatalf("Sandbox0BaseURL = %q, want %q", cfg.Sandbox0BaseURL, defaultSandbox0BaseURL)
	}
}

func TestLoadConfigTrimsSandbox0BaseURL(t *testing.T) {
	t.Setenv("MANAGED_AGENT_DATABASE_URL", "postgres://example")
	t.Setenv("MANAGED_AGENT_SANDBOX0_BASE_URL", "https://api.sandbox0.ai/")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Sandbox0BaseURL != "https://api.sandbox0.ai" {
		t.Fatalf("Sandbox0BaseURL = %q, want trimmed URL", cfg.Sandbox0BaseURL)
	}
}
