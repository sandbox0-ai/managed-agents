package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestCatalogLifecycle(t *testing.T) {
	cfg := loadConfig(t)
	client := newClient(cfg)

	env := createEnvironment(t, client, cfg.Suffix)
	agent := createAgent(t, client, cfg.Suffix)
	vault := createLLMVault(t, client, cfg.Suffix)
	createStaticBearerCredential(t, client, requireString(t, vault, "id"))
	envID := requireString(t, env, "id")
	agentID := requireString(t, agent, "id")
	vaultID := requireString(t, vault, "id")
	t.Cleanup(func() {
		_, _, _ = client.post(t.Context(), "/v1/agents/"+agentID+"/archive", map[string]any{})
		_, _, _ = client.delete(t.Context(), "/v1/vaults/"+vaultID)
		_, _, _ = client.delete(t.Context(), "/v1/environments/"+envID)
	})

	if got, status, err := client.get(t.Context(), "/v1/environments/"+envID); err != nil || status != http.StatusOK {
		t.Fatalf("get environment status=%d err=%v", status, err)
	} else if got["id"] != env["id"] {
		t.Fatalf("get environment id=%v, want %v", got["id"], env["id"])
	}
	if got, status, err := client.get(t.Context(), "/v1/agents/"+agentID); err != nil || status != http.StatusOK {
		t.Fatalf("get agent status=%d err=%v", status, err)
	} else if got["id"] != agent["id"] {
		t.Fatalf("get agent id=%v, want %v", got["id"], agent["id"])
	}
	if got, status, err := client.get(t.Context(), "/v1/vaults/"+vaultID); err != nil || status != http.StatusOK {
		t.Fatalf("get vault status=%d err=%v", status, err)
	} else if got["id"] != vault["id"] {
		t.Fatalf("get vault id=%v, want %v", got["id"], vault["id"])
	}

	if _, status, err := client.get(t.Context(), "/v1/environments?limit=10"); err != nil || status != http.StatusOK {
		t.Fatalf("list environments status=%d err=%v", status, err)
	}
	if _, status, err := client.get(t.Context(), "/v1/agents?limit=10"); err != nil || status != http.StatusOK {
		t.Fatalf("list agents status=%d err=%v", status, err)
	}
	if _, status, err := client.get(t.Context(), "/v1/vaults?limit=10"); err != nil || status != http.StatusOK {
		t.Fatalf("list vaults status=%d err=%v", status, err)
	}
}

func TestCreateEnvironmentBuildsPackagesSynchronously(t *testing.T) {
	cfg := loadConfig(t)
	client := newClient(cfg)

	env := createEnvironmentWithPackages(t, client, cfg.Suffix, map[string]any{
		"pip": []string{"six==1.17.0"},
	})
	envID := requireString(t, env, "id")
	t.Cleanup(func() {
		_, _, _ = client.delete(t.Context(), "/v1/environments/"+envID)
	})

	config, _ := env["config"].(map[string]any)
	packages, _ := config["packages"].(map[string]any)
	if !containsStringValue(packages["pip"], "six==1.17.0") {
		t.Fatalf("environment packages.pip = %#v, want six==1.17.0", packages["pip"])
	}
}

func TestCreateEnvironmentPackageBuildFailureDoesNotPersist(t *testing.T) {
	cfg := loadConfig(t)
	client := newClient(cfg)

	name := fmt.Sprintf("e2e-env-failing-packages-%s", cfg.Suffix)
	body := map[string]any{
		"name": name,
		"config": map[string]any{
			"type":       "cloud",
			"networking": map[string]any{"type": "unrestricted"},
			"packages": map[string]any{
				"type": "packages",
				"pip":  []string{"not a valid requirement ;"},
			},
		},
		"metadata": map[string]string{"e2e": "managed-agents"},
	}
	if _, status, err := client.post(t.Context(), "/v1/environments", body); err == nil || status != http.StatusBadRequest {
		t.Fatalf("create failing package environment status=%d err=%v, want 400", status, err)
	}

	resp, status, err := client.get(t.Context(), "/v1/environments?limit=100")
	if err != nil || status != http.StatusOK {
		t.Fatalf("list environments status=%d err=%v", status, err)
	}
	for _, item := range listData(resp) {
		environment, _ := item.(map[string]any)
		if environment["name"] == name {
			t.Fatalf("failed package environment %q was persisted: %#v", name, environment)
		}
	}
}

func createEnvironment(t *testing.T, client *apiClient, suffix string) map[string]any {
	t.Helper()
	body := map[string]any{
		"name": fmt.Sprintf("e2e-env-%s", suffix),
		"config": map[string]any{
			"type":       "cloud",
			"networking": map[string]any{"type": "unrestricted"},
			"packages":   map[string]any{"type": "packages"},
		},
		"metadata": map[string]string{"e2e": "managed-agents"},
	}
	resp, status, err := client.post(t.Context(), "/v1/environments", body)
	if err != nil || status != http.StatusOK {
		t.Fatalf("create environment status=%d err=%v", status, err)
	}
	requireString(t, resp, "id")
	return resp
}

func createEnvironmentWithPackages(t *testing.T, client *apiClient, suffix string, packages map[string]any) map[string]any {
	t.Helper()
	body := map[string]any{
		"name": fmt.Sprintf("e2e-env-packages-%s", suffix),
		"config": map[string]any{
			"type":       "cloud",
			"networking": map[string]any{"type": "unrestricted"},
			"packages": mergeStringAnyMap(map[string]any{
				"type": "packages",
			}, packages),
		},
		"metadata": map[string]string{"e2e": "managed-agents"},
	}
	resp, status, err := client.post(t.Context(), "/v1/environments", body)
	if err != nil || status != http.StatusOK {
		t.Fatalf("create package environment status=%d err=%v", status, err)
	}
	requireString(t, resp, "id")
	return resp
}

func createAgent(t *testing.T, client *apiClient, suffix string) map[string]any {
	t.Helper()
	body := map[string]any{
		"name":   fmt.Sprintf("e2e-agent-%s", suffix),
		"model":  map[string]any{"id": "claude-sonnet-4-20250514"},
		"system": "Reply with one short sentence.",
		"tools": []map[string]any{{
			"type": "agent_toolset_20260401",
			"default_config": map[string]any{
				"enabled":           true,
				"permission_policy": map[string]any{"type": "always_allow"},
			},
		}},
		"metadata": map[string]string{"e2e": "managed-agents"},
	}
	resp, status, err := client.post(t.Context(), "/v1/agents", body)
	if err != nil || status != http.StatusOK {
		t.Fatalf("create agent status=%d err=%v", status, err)
	}
	requireString(t, resp, "id")
	return resp
}

func mergeStringAnyMap(base, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range extra {
		out[key] = value
	}
	return out
}

func containsStringValue(value any, expected string) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		if strings.TrimSpace(fmt.Sprint(item)) == expected {
			return true
		}
	}
	return false
}

func createAgentWithSkills(t *testing.T, client *apiClient, suffix string, skills []map[string]any) map[string]any {
	t.Helper()
	body := map[string]any{
		"name":   fmt.Sprintf("e2e-agent-%s", suffix),
		"model":  map[string]any{"id": "claude-sonnet-4-20250514"},
		"system": "Use attached skills when they match the request.",
		"tools": []map[string]any{{
			"type": "agent_toolset_20260401",
			"default_config": map[string]any{
				"enabled":           true,
				"permission_policy": map[string]any{"type": "always_allow"},
			},
		}},
		"skills":   skills,
		"metadata": map[string]string{"e2e": "managed-agents"},
	}
	resp, status, err := client.post(t.Context(), "/v1/agents", body)
	if err != nil || status != http.StatusOK {
		t.Fatalf("create agent with skills status=%d err=%v", status, err)
	}
	requireString(t, resp, "id")
	return resp
}

func createLLMVault(t *testing.T, client *apiClient, suffix string) map[string]any {
	t.Helper()
	body := map[string]any{
		"display_name": fmt.Sprintf("e2e-llm-%s", suffix),
		"metadata": map[string]string{
			"sandbox0.managed_agents.role":         "llm",
			"sandbox0.managed_agents.engine":       "claude",
			"sandbox0.managed_agents.llm_base_url": "https://api.anthropic.com",
			"e2e":                                  "managed-agents",
		},
	}
	resp, status, err := client.post(t.Context(), "/v1/vaults", body)
	if err != nil || status != http.StatusOK {
		t.Fatalf("create vault status=%d err=%v", status, err)
	}
	requireString(t, resp, "id")
	return resp
}

func createStaticBearerCredential(t *testing.T, client *apiClient, vaultID string) map[string]any {
	t.Helper()
	body := map[string]any{
		"display_name": "e2e fake model token",
		"auth": map[string]any{
			"type":  "static_bearer",
			"token": "fake-model-token",
		},
		"metadata": map[string]string{"e2e": "managed-agents"},
	}
	resp, status, err := client.post(t.Context(), "/v1/vaults/"+vaultID+"/credentials", body)
	if err != nil || status != http.StatusOK {
		t.Fatalf("create credential status=%d err=%v", status, err)
	}
	requireString(t, resp, "id")
	return resp
}
