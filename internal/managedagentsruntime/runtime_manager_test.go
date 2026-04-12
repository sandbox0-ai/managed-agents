package managedagentsruntime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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
	if cfg.TemplateMainImage == "" {
		t.Fatal("TemplateMainImage should not be empty")
	}
}

func TestCanonicalWrapperURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{
			name: "host only",
			raw:  "rs-example--p8080.aws-us-east-1.sandbox0.app",
			want: "https://rs-example--p8080.aws-us-east-1.sandbox0.app",
		},
		{
			name: "canonicalizes full url",
			raw:  " HTTPS://Wrapper.EXAMPLE.TEST/ ",
			want: "https://wrapper.example.test",
		},
		{
			name:    "rejects empty",
			raw:     "   ",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := canonicalWrapperURL(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("canonicalWrapperURL(%q) error = nil, want non-nil", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("canonicalWrapperURL(%q) error = %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("canonicalWrapperURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestWrapperRequestTargetUsesSandboxBaseURLAndHostHeader(t *testing.T) {
	mgr := &SDKRuntimeManager{cfg: Config{SandboxBaseURL: "http://127.0.0.1:18080"}}
	requestURL, hostHeader, err := mgr.wrapperRequestTarget("rs-example--p8080.aws-us-east-1.sandbox0.app", "/v1/runtime/session")
	if err != nil {
		t.Fatalf("wrapperRequestTarget returned error: %v", err)
	}
	if requestURL != "http://127.0.0.1:18080/v1/runtime/session" {
		t.Fatalf("requestURL = %q, want %q", requestURL, "http://127.0.0.1:18080/v1/runtime/session")
	}
	if hostHeader != "rs-example--p8080.aws-us-east-1.sandbox0.app" {
		t.Fatalf("hostHeader = %q, want %q", hostHeader, "rs-example--p8080.aws-us-east-1.sandbox0.app")
	}
}

func TestWrapperRequestTargetPreservesBasePathPrefix(t *testing.T) {
	mgr := &SDKRuntimeManager{cfg: Config{SandboxBaseURL: "http://gateway.internal/base"}}
	requestURL, hostHeader, err := mgr.wrapperRequestTarget("https://wrapper.example.test", "/v1/runs")
	if err != nil {
		t.Fatalf("wrapperRequestTarget returned error: %v", err)
	}
	if requestURL != "http://gateway.internal/base/v1/runs" {
		t.Fatalf("requestURL = %q, want %q", requestURL, "http://gateway.internal/base/v1/runs")
	}
	if hostHeader != "wrapper.example.test" {
		t.Fatalf("hostHeader = %q, want %q", hostHeader, "wrapper.example.test")
	}
}

func TestRuntimeWebhookURLPrefersConfiguredCallbackBaseURL(t *testing.T) {
	mgr := &SDKRuntimeManager{cfg: Config{RuntimeCallbackBaseURL: "http://172.18.0.1:8088"}}
	got := mgr.runtimeWebhookURL("http://127.0.0.1:18088")
	want := "http://172.18.0.1:8088/internal/managed-agents/runtime/webhooks"
	if got != want {
		t.Fatalf("runtimeWebhookURL() = %q, want %q", got, want)
	}
}

func TestRuntimeWebhookURLFallsBackToRequestBaseURL(t *testing.T) {
	mgr := &SDKRuntimeManager{}
	got := mgr.runtimeWebhookURL("http://127.0.0.1:18088")
	want := "http://127.0.0.1:18088/internal/managed-agents/runtime/webhooks"
	if got != want {
		t.Fatalf("runtimeWebhookURL() = %q, want %q", got, want)
	}
}

func TestNewSandboxClientAddsTeamHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/sandboxvolumes" {
			t.Fatalf("path = %q, want %q", r.URL.Path, "/api/v1/sandboxvolumes")
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token_123" {
			t.Fatalf("Authorization = %q, want %q", got, "Bearer token_123")
		}
		if got := r.Header.Get("X-Team-ID"); got != "team_123" {
			t.Fatalf("X-Team-ID = %q, want %q", got, "team_123")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	}))
	t.Cleanup(server.Close)

	mgr := &SDKRuntimeManager{cfg: Config{SandboxBaseURL: server.URL, SandboxRequestTimeout: 5 * time.Second}}
	client, err := mgr.newSandboxClient("token_123", " team_123 ")
	if err != nil {
		t.Fatalf("newSandboxClient returned error: %v", err)
	}
	if _, err := client.ListVolume(context.Background()); err != nil {
		t.Fatalf("ListVolume returned error: %v", err)
	}
}
