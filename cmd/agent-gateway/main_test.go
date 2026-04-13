package main

import (
	"reflect"
	"testing"
)

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

func TestLoadConfigParsesRuntimeAllowedDomains(t *testing.T) {
	t.Setenv("MANAGED_AGENT_DATABASE_URL", "postgres://example")
	t.Setenv("MANAGED_AGENT_RUNTIME_ALLOWED_DOMAINS", "api.search.test, https://gateway.example.test/path\nextra.example.test")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	want := []string{"api.search.test", "https://gateway.example.test/path", "extra.example.test"}
	if !reflect.DeepEqual(cfg.RuntimeAllowedDomains, want) {
		t.Fatalf("RuntimeAllowedDomains = %#v, want %#v", cfg.RuntimeAllowedDomains, want)
	}
}
