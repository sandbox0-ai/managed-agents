package e2e

import (
	"os"
	"strings"
	"testing"
	"time"
)

type testConfig struct {
	BaseURL string
	Token   string
	Beta    string
	Suffix  string
}

func loadConfig(t *testing.T) testConfig {
	t.Helper()

	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("MANAGED_AGENTS_E2E_BASE_URL")), "/")
	token := strings.TrimSpace(os.Getenv("MANAGED_AGENTS_E2E_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("SANDBOX0_API_KEY"))
	}
	if baseURL == "" || token == "" {
		t.Skip("set MANAGED_AGENTS_E2E_BASE_URL and MANAGED_AGENTS_E2E_TOKEN to run managed-agents e2e tests")
	}
	beta := strings.TrimSpace(os.Getenv("MANAGED_AGENTS_E2E_BETA"))
	if beta == "" {
		beta = "managed-agents-2026-04-01"
	}
	return testConfig{
		BaseURL: baseURL,
		Token:   token,
		Beta:    beta,
		Suffix:  strings.ReplaceAll(time.Now().UTC().Format("20060102T150405.000000000"), ".", ""),
	}
}
