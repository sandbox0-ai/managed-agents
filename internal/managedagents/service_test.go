package managedagents

import "testing"

func TestEnsureResolvesRequiredActionsAcceptsSubset(t *testing.T) {
	err := ensureResolvesRequiredActions([]string{"evt_1", "evt_2"}, []map[string]any{{
		"type":        "user.tool_confirmation",
		"tool_use_id": "evt_1",
		"result":      "allow",
	}})
	if err != nil {
		t.Fatalf("ensureResolvesRequiredActions() error = %v, want nil", err)
	}
}

func TestEnsureResolvesRequiredActionsRejectsUnknownActionID(t *testing.T) {
	err := ensureResolvesRequiredActions([]string{"evt_1"}, []map[string]any{{
		"type":        "user.tool_confirmation",
		"tool_use_id": "evt_2",
		"result":      "allow",
	}})
	if err == nil {
		t.Fatal("expected unknown pending action id error")
	}
}
