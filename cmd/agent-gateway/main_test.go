package main

import "testing"

func TestLoadConfigDefaultsAuthBaseURLToSandbox0BaseURL(t *testing.T) {
	t.Setenv("MANAGED_AGENT_DATABASE_URL", "postgres://example")
	t.Setenv("MANAGED_AGENT_SANDBOX0_BASE_URL", "https://gcp-ue4.sandbox0.ai/")
	t.Setenv("MANAGED_AGENT_SANDBOX0_AUTH_BASE_URL", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Sandbox0AuthBaseURL != "https://gcp-ue4.sandbox0.ai" {
		t.Fatalf("Sandbox0AuthBaseURL = %q, want sandbox0 base URL", cfg.Sandbox0AuthBaseURL)
	}
}

func TestLoadConfigUsesExplicitAuthBaseURL(t *testing.T) {
	t.Setenv("MANAGED_AGENT_DATABASE_URL", "postgres://example")
	t.Setenv("MANAGED_AGENT_SANDBOX0_BASE_URL", "https://gcp-ue4.sandbox0.ai")
	t.Setenv("MANAGED_AGENT_SANDBOX0_AUTH_BASE_URL", "https://api.sandbox0.ai/")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Sandbox0AuthBaseURL != "https://api.sandbox0.ai" {
		t.Fatalf("Sandbox0AuthBaseURL = %q, want explicit auth base URL", cfg.Sandbox0AuthBaseURL)
	}
}
