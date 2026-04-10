package managedagentsruntime

import "testing"

func TestConfigWithDefaults(t *testing.T) {
	cfg := (Config{}).WithDefaults(8443)
	if cfg.ClaudeTemplate != "managed-agent-claude" {
		t.Fatalf("ClaudeTemplate = %q", cfg.ClaudeTemplate)
	}
	if cfg.SandboxBaseURL != "http://127.0.0.1:8443" {
		t.Fatalf("SandboxBaseURL = %q", cfg.SandboxBaseURL)
	}
	if cfg.RegionID != "" {
		t.Fatalf("RegionID = %q, want empty", cfg.RegionID)
	}
	if cfg.WrapperPort != 8080 {
		t.Fatalf("WrapperPort = %d, want 8080", cfg.WrapperPort)
	}
}
