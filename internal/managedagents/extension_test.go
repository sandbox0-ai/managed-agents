package managedagents

import "testing"

func TestMatchesPathUsesClaudePrefix(t *testing.T) {
	paths := []string{
		"/v1/sessions",
		"/v1/agents",
		"/internal/managed-agents/runtime/webhooks",
	}
	for _, path := range paths {
		if !MatchesPath(path) {
			t.Fatalf("MatchesPath(%q) = false, want true", path)
		}
	}
	if MatchesPath("/claude/v1/sessions") {
		t.Fatal("MatchesPath unexpectedly accepted sandbox0-prefixed path")
	}
}

func TestInternalSandboxWebhookURLIncludesClaudePrefix(t *testing.T) {
	got := InternalSandboxWebhookURL("https://gw.example.com/")
	want := "https://gw.example.com/internal/managed-agents/runtime/webhooks"
	if got != want {
		t.Fatalf("InternalSandboxWebhookURL() = %q, want %q", got, want)
	}
}
