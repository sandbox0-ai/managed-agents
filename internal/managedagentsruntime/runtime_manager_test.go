package managedagentsruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
	sandbox0sdk "github.com/sandbox0-ai/sdk-go"
	apispec "github.com/sandbox0-ai/sdk-go/pkg/apispec"
	"go.uber.org/zap"
)

func TestConfigWithDefaults(t *testing.T) {
	cfg := (Config{}).WithDefaults(8443)
	if cfg.TemplateID != "managed-agents" {
		t.Fatalf("TemplateID = %q", cfg.TemplateID)
	}
	if cfg.SandboxBaseURL != "http://127.0.0.1:8443" {
		t.Fatalf("SandboxBaseURL = %q", cfg.SandboxBaseURL)
	}
	if cfg.WrapperPort != 8080 {
		t.Fatalf("WrapperPort = %d, want 8080", cfg.WrapperPort)
	}
	if cfg.WorkspaceMountPath != "/workspace" {
		t.Fatalf("WorkspaceMountPath = %q, want /workspace", cfg.WorkspaceMountPath)
	}
	if cfg.TemplateMainImage == "" {
		t.Fatal("TemplateMainImage should not be empty")
	}
	if cfg.SandboxTTLSeconds != DefaultSandboxTTLSeconds {
		t.Fatalf("SandboxTTLSeconds = %d, want %d", cfg.SandboxTTLSeconds, DefaultSandboxTTLSeconds)
	}
}

func TestRuntimeStateMountPathUsesWorkspace(t *testing.T) {
	tests := []struct {
		name      string
		workspace string
		want      string
	}{
		{
			name:      "default workspace",
			workspace: "/workspace",
			want:      "/workspace/.sandbox0/agent-wrapper",
		},
		{
			name:      "custom workspace",
			workspace: "/home/agent/work",
			want:      "/home/agent/work/.sandbox0/agent-wrapper",
		},
		{
			name:      "empty workspace falls back",
			workspace: "",
			want:      "/workspace/.sandbox0/agent-wrapper",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runtimeStateMountPath(tt.workspace); got != tt.want {
				t.Fatalf("runtimeStateMountPath(%q) = %q, want %q", tt.workspace, got, tt.want)
			}
		})
	}
}

func TestSandboxTTLSecondsUsesRuntimeDefault(t *testing.T) {
	mgr := &SDKRuntimeManager{cfg: Config{}}
	if got := mgr.sandboxTTLSeconds(); got != DefaultSandboxTTLSeconds {
		t.Fatalf("sandboxTTLSeconds = %d, want %d", got, DefaultSandboxTTLSeconds)
	}

	mgr.cfg.SandboxTTLSeconds = 60
	if got := mgr.sandboxTTLSeconds(); got != 60 {
		t.Fatalf("sandboxTTLSeconds = %d, want 60", got)
	}
}

func TestManagedEnvironmentRuntimeEnvVarsAddsConfiguredPackageManagers(t *testing.T) {
	environment := &gatewaymanagedagents.Environment{Config: gatewaymanagedagents.CloudConfig{
		Packages: gatewaymanagedagents.EnvironmentPackages{
			Type: "packages",
			Go:   []string{"github.com/acme/tool"},
			NPM:  []string{"tsx"},
			Pip:  []string{"ruff"},
		},
	}}
	env := managedEnvironmentRuntimeEnvVars("ctl_123", environment)
	if env["AGENT_WRAPPER_CONTROL_TOKEN"] != "ctl_123" {
		t.Fatalf("AGENT_WRAPPER_CONTROL_TOKEN = %q, want ctl_123", env["AGENT_WRAPPER_CONTROL_TOKEN"])
	}
	for _, want := range []string{
		gatewaymanagedagents.ManagedEnvironmentNPMMountPath + "/bin",
		gatewaymanagedagents.ManagedEnvironmentPipMountPath + "/venv/bin",
		gatewaymanagedagents.ManagedEnvironmentGoMountPath + "/bin",
		"/usr/bin",
	} {
		if !containsPathEntry(env["PATH"], want) {
			t.Fatalf("PATH = %q, missing %s", env["PATH"], want)
		}
	}
	if env["NODE_PATH"] != gatewaymanagedagents.ManagedEnvironmentNPMMountPath+"/lib/node_modules" {
		t.Fatalf("NODE_PATH = %q", env["NODE_PATH"])
	}
	if env["VIRTUAL_ENV"] != gatewaymanagedagents.ManagedEnvironmentPipMountPath+"/venv" {
		t.Fatalf("VIRTUAL_ENV = %q", env["VIRTUAL_ENV"])
	}
	if _, ok := env["GEM_HOME"]; ok {
		t.Fatalf("GEM_HOME should be unset when gem packages are not configured: %#v", env)
	}
}

func TestManagedEnvironmentRuntimeEnvVarsKeepsBaseEnvForEmptyPackages(t *testing.T) {
	env := managedEnvironmentRuntimeEnvVars("ctl_123", &gatewaymanagedagents.Environment{Config: gatewaymanagedagents.CloudConfig{
		Packages: gatewaymanagedagents.EnvironmentPackages{Type: "packages"},
	}})
	if env["AGENT_WRAPPER_CONTROL_TOKEN"] != "ctl_123" {
		t.Fatalf("AGENT_WRAPPER_CONTROL_TOKEN = %q, want ctl_123", env["AGENT_WRAPPER_CONTROL_TOKEN"])
	}
	if _, ok := env["PATH"]; ok {
		t.Fatalf("PATH should be unset without configured package managers: %#v", env)
	}
}

func containsPathEntry(pathValue, want string) bool {
	for _, item := range strings.Split(pathValue, ":") {
		if item == want {
			return true
		}
	}
	return false
}

func TestSandboxNotFoundRecognizesAPIError(t *testing.T) {
	if !isSandboxNotFound(&sandbox0sdk.APIError{StatusCode: http.StatusNotFound}) {
		t.Fatal("isSandboxNotFound returned false for sandbox0 404")
	}
	if isSandboxNotFound(&sandbox0sdk.APIError{StatusCode: http.StatusInternalServerError}) {
		t.Fatal("isSandboxNotFound returned true for sandbox0 500")
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

func TestWrapperRequestTargetUsesDirectWrapperURLByDefault(t *testing.T) {
	mgr := &SDKRuntimeManager{cfg: Config{SandboxBaseURL: "http://127.0.0.1:18080"}}
	requestURL, hostHeader, err := mgr.wrapperRequestTarget("rs-example--p8080.aws-us-east-1.sandbox0.app", "/v1/runtime/session")
	if err != nil {
		t.Fatalf("wrapperRequestTarget returned error: %v", err)
	}
	if requestURL != "https://rs-example--p8080.aws-us-east-1.sandbox0.app/v1/runtime/session" {
		t.Fatalf("requestURL = %q, want %q", requestURL, "https://rs-example--p8080.aws-us-east-1.sandbox0.app/v1/runtime/session")
	}
	if hostHeader != "" {
		t.Fatalf("hostHeader = %q, want empty direct request host", hostHeader)
	}
}

func TestWrapperRequestTargetUsesExposureBaseURL(t *testing.T) {
	mgr := &SDKRuntimeManager{cfg: Config{SandboxExposureBaseURL: "http://fullmode-cluster-gateway.sandbox0-system.svc.cluster.local:30080"}}
	requestURL, hostHeader, err := mgr.wrapperRequestTarget("https://rs-example--p8080.aws-us-east-1.sandbox0.app", "/v1/runtime/session")
	if err != nil {
		t.Fatalf("wrapperRequestTarget returned error: %v", err)
	}
	if requestURL != "http://fullmode-cluster-gateway.sandbox0-system.svc.cluster.local:30080/v1/runtime/session" {
		t.Fatalf("requestURL = %q, want cluster gateway target", requestURL)
	}
	if hostHeader != "rs-example--p8080.aws-us-east-1.sandbox0.app" {
		t.Fatalf("hostHeader = %q, want public exposure host", hostHeader)
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

func TestTemplateClientUsesAdminKeyWithoutTeamHeader(t *testing.T) {
	seen := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = true
		if r.URL.Path != "/api/v1/templates/managed-agents" {
			t.Fatalf("path = %q, want template lookup", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer admin_123" {
			t.Fatalf("Authorization = %q, want admin token", got)
		}
		if got := r.Header.Get("X-Team-ID"); got != "" {
			t.Fatalf("X-Team-ID = %q, want no team header for public template", got)
		}
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	mgr := &SDKRuntimeManager{cfg: Config{SandboxBaseURL: server.URL, SandboxRequestTimeout: 5 * time.Second, SandboxAdminAPIKey: " admin_123 "}}
	client, err := mgr.templateClient(context.Background(), gatewayCredential("user_123"), "team_123")
	if err != nil {
		t.Fatalf("templateClient returned error: %v", err)
	}
	_, _ = client.GetTemplateSpec(context.Background(), "managed-agents")
	if !seen {
		t.Fatal("expected template client to issue a request")
	}
}

func TestTemplateClientRequiresRuntimeAPIKey(t *testing.T) {
	mgr := &SDKRuntimeManager{cfg: Config{SandboxBaseURL: "http://127.0.0.1:1", SandboxRequestTimeout: 5 * time.Second}}
	_, err := mgr.templateClient(context.Background(), gatewayCredential("user_123"), "team_123")
	if err == nil || !strings.Contains(err.Error(), "runtime api key") {
		t.Fatalf("templateClient error = %v, want missing runtime api key", err)
	}
}

func TestSandboxClientForRuntimeUsesRuntimeKeyWithoutTeamHeader(t *testing.T) {
	seen := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = true
		if got := r.Header.Get("Authorization"); got != "Bearer runtime_123" {
			t.Fatalf("Authorization = %q, want runtime token", got)
		}
		if got := r.Header.Get("X-Team-ID"); got != "" {
			t.Fatalf("X-Team-ID = %q, want no caller team header", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	}))
	t.Cleanup(server.Close)

	mgr := &SDKRuntimeManager{cfg: Config{SandboxBaseURL: server.URL, SandboxRequestTimeout: 5 * time.Second, SandboxAdminAPIKey: " runtime_123 "}}
	client, err := mgr.sandboxClientForRuntime(context.Background(), &gatewaymanagedagents.RuntimeRecord{SessionID: "sesn_123"})
	if err != nil {
		t.Fatalf("sandboxClientForRuntime returned error: %v", err)
	}
	if _, err := client.ListVolume(context.Background()); err != nil {
		t.Fatalf("ListVolume returned error: %v", err)
	}
	if !seen {
		t.Fatal("expected sandbox client to issue a request")
	}
}

func TestCleanupRuntimeSandboxResourcesClearsCredentialReferencesBeforeDeletingSources(t *testing.T) {
	sessionID := "sesn_123"
	sandboxID := "sbx_123"
	sourceName := managedCredentialSourceName(sessionID, "vcrd_123")
	calls := make([]string, 0)
	policyCleared := false
	initialPolicy := apispec.SandboxNetworkPolicy{
		Mode: apispec.SandboxNetworkPolicyModeAllowAll,
		CredentialBindings: []apispec.CredentialBinding{
			testCredentialBinding("existing-bind", "existing-source"),
			testCredentialBinding(managedCredentialBindingRef(sessionID, "vcrd_123"), sourceName),
		},
		Egress: apispec.NewOptNetworkEgressPolicy(apispec.NetworkEgressPolicy{
			CredentialRules: []apispec.EgressCredentialRule{
				{
					Name:          apispec.NewOptString("existing-rule"),
					CredentialRef: "existing-bind",
					Domains:       []string{"example.com"},
				},
				{
					Name:          apispec.NewOptString(managedCredentialRuleName(sessionID, "vcrd_123")),
					CredentialRef: managedCredentialBindingRef(sessionID, "vcrd_123"),
					Domains:       []string{"api.example.com"},
				},
			},
		}),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sandboxes/"+sandboxID+"/network":
			calls = append(calls, "get-network")
			writeTestJSON(t, w, map[string]any{"success": true, "data": initialPolicy})
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/sandboxes/"+sandboxID+"/network":
			calls = append(calls, "put-network")
			var updated apispec.SandboxNetworkPolicy
			if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
				t.Fatalf("decode updated network policy: %v", err)
			}
			if len(updated.CredentialBindings) != 1 || updated.CredentialBindings[0].Ref != "existing-bind" {
				t.Fatalf("updated credential bindings = %#v, want only existing binding", updated.CredentialBindings)
			}
			egress, ok := updated.Egress.Get()
			if !ok {
				t.Fatal("updated egress not set")
			}
			if len(egress.CredentialRules) != 1 || egress.CredentialRules[0].CredentialRef != "existing-bind" {
				t.Fatalf("updated credential rules = %#v, want only existing rule", egress.CredentialRules)
			}
			policyCleared = true
			writeTestJSON(t, w, map[string]any{"success": true, "data": updated})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/credential-sources":
			calls = append(calls, "list-sources")
			writeTestJSON(t, w, map[string]any{
				"success": true,
				"data": []map[string]any{
					{"name": sourceName, "resolverKind": "static_headers"},
					{"name": "external-source", "resolverKind": "static_headers"},
				},
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/credential-sources/"+sourceName:
			calls = append(calls, "delete-source")
			if !policyCleared {
				http.Error(w, `{"error":{"code":"conflict","message":"credential source is still referenced by sandbox bindings"}}`, http.StatusConflict)
				return
			}
			writeTestJSON(t, w, map[string]any{"success": true, "data": map[string]any{"message": "deleted"}})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/sandboxes/"+sandboxID:
			calls = append(calls, "delete-sandbox")
			if !policyCleared {
				t.Fatal("sandbox deleted before network policy credential references were cleared")
			}
			writeTestJSON(t, w, map[string]any{"success": true, "data": map[string]any{"message": "deleted"}})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/sandboxvolumes/vol_npm_session":
			calls = append(calls, "delete-env-volume")
			if !policyCleared {
				t.Fatal("environment volume deleted before network policy credential references were cleared")
			}
			writeTestJSON(t, w, map[string]any{"success": true, "data": map[string]any{"message": "deleted"}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	mgr := &SDKRuntimeManager{cfg: Config{SandboxBaseURL: server.URL, SandboxRequestTimeout: 5 * time.Second}, logger: zap.NewNop()}
	client, err := mgr.newSandboxClient("token_123", "team_123")
	if err != nil {
		t.Fatalf("newSandboxClient returned error: %v", err)
	}
	runtime := &gatewaymanagedagents.RuntimeRecord{
		SessionID:            sessionID,
		SandboxID:            sandboxID,
		EnvironmentVolumeIDs: map[string]string{"npm": "vol_npm_session"},
	}
	if err := mgr.cleanupRuntimeSandboxResources(context.Background(), client, runtime, false); err != nil {
		t.Fatalf("cleanupRuntimeSandboxResources returned error: %v", err)
	}
	got := strings.Join(calls, ",")
	want := "get-network,put-network,list-sources,delete-source,delete-sandbox,delete-env-volume"
	if got != want {
		t.Fatalf("calls = %s, want %s", got, want)
	}
}

func TestCleanupRuntimeSandboxResourcesStopsBeforeSandboxDeleteWhenCredentialCleanupFails(t *testing.T) {
	sessionID := "sesn_123"
	sandboxID := "sbx_123"
	sourceName := managedCredentialSourceName(sessionID, "vcrd_123")
	calls := make([]string, 0)
	initialPolicy := apispec.SandboxNetworkPolicy{
		Mode: apispec.SandboxNetworkPolicyModeAllowAll,
		CredentialBindings: []apispec.CredentialBinding{
			testCredentialBinding(managedCredentialBindingRef(sessionID, "vcrd_123"), sourceName),
		},
		Egress: apispec.NewOptNetworkEgressPolicy(apispec.NetworkEgressPolicy{
			CredentialRules: []apispec.EgressCredentialRule{{
				Name:          apispec.NewOptString(managedCredentialRuleName(sessionID, "vcrd_123")),
				CredentialRef: managedCredentialBindingRef(sessionID, "vcrd_123"),
				Domains:       []string{"api.example.com"},
			}},
		}),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sandboxes/"+sandboxID+"/network":
			calls = append(calls, "get-network")
			writeTestJSON(t, w, map[string]any{"success": true, "data": initialPolicy})
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/sandboxes/"+sandboxID+"/network":
			calls = append(calls, "put-network")
			var updated apispec.SandboxNetworkPolicy
			if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
				t.Fatalf("decode updated network policy: %v", err)
			}
			writeTestJSON(t, w, map[string]any{"success": true, "data": updated})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/credential-sources":
			calls = append(calls, "list-sources")
			writeTestJSON(t, w, map[string]any{
				"success": true,
				"data": []map[string]any{
					{"name": sourceName, "resolverKind": "static_headers"},
				},
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/credential-sources/"+sourceName:
			calls = append(calls, "delete-source")
			http.Error(w, `{"error":{"code":"conflict","message":"credential source is still referenced by sandbox bindings"}}`, http.StatusConflict)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/sandboxes/"+sandboxID:
			t.Fatal("sandbox delete should not run when credential source cleanup fails")
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	mgr := &SDKRuntimeManager{cfg: Config{SandboxBaseURL: server.URL, SandboxRequestTimeout: 5 * time.Second}, logger: zap.NewNop()}
	client, err := mgr.newSandboxClient("token_123", "team_123")
	if err != nil {
		t.Fatalf("newSandboxClient returned error: %v", err)
	}
	runtime := &gatewaymanagedagents.RuntimeRecord{
		SessionID: sessionID,
		SandboxID: sandboxID,
	}
	if err := mgr.cleanupRuntimeSandboxResources(context.Background(), client, runtime, false); err == nil {
		t.Fatal("cleanupRuntimeSandboxResources error = nil, want credential cleanup error")
	}
	got := strings.Join(calls, ",")
	want := "get-network,put-network,list-sources,delete-source"
	if got != want {
		t.Fatalf("calls = %s, want %s", got, want)
	}
}

func TestBuildEnvironmentArtifactAttemptSkipsPackageVolumesWhenNoPackages(t *testing.T) {
	mgr := &SDKRuntimeManager{}
	environment := &gatewaymanagedagents.Environment{Config: gatewaymanagedagents.CloudConfig{
		Packages: gatewaymanagedagents.EnvironmentPackages{Type: "packages"},
	}}

	assets, buildLog, err := mgr.buildEnvironmentArtifactAttempt(context.Background(), nil, environment, nil, nil)
	if err != nil {
		t.Fatalf("buildEnvironmentArtifactAttempt returned error: %v", err)
	}
	if got := assets.VolumeIDs(); len(got) != 0 {
		t.Fatalf("artifact volume ids = %#v, want empty", got)
	}
	if buildLog != "no environment packages requested; no package volumes created\n" {
		t.Fatalf("build log = %q", buildLog)
	}
}

func TestPublishEnvironmentArtifactVolumesRetriesTransientForkFailure(t *testing.T) {
	forkCalls := 0
	unexpectedRequest := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/sandboxvolumes/temp_pip/fork" {
			unexpectedRequest = r.Method + " " + r.URL.Path
			http.Error(w, `{"error":{"code":"unexpected_request","message":"unexpected request"}}`, http.StatusBadRequest)
			return
		}
		forkCalls++
		if forkCalls == 1 {
			w.WriteHeader(http.StatusBadGateway)
			writeTestJSON(t, w, map[string]any{
				"error": map[string]any{
					"code":    "upstream_unavailable",
					"message": "upstream service unavailable",
				},
			})
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeTestJSON(t, w, map[string]any{
			"success": true,
			"data": map[string]any{
				"id":          "published_pip",
				"team_id":     "team_123",
				"user_id":     "user_123",
				"access_mode": "ROX",
				"created_at":  "2026-04-24T00:00:00Z",
				"updated_at":  "2026-04-24T00:00:00Z",
			},
		})
	}))
	t.Cleanup(server.Close)

	mgr := &SDKRuntimeManager{cfg: Config{SandboxBaseURL: server.URL, SandboxRequestTimeout: 5 * time.Second}}
	client, err := mgr.newSandboxClient("token_123", "team_123")
	if err != nil {
		t.Fatalf("newSandboxClient returned error: %v", err)
	}
	assets, err := publishEnvironmentArtifactVolumesWithRetry(context.Background(), client, map[string]string{"pip": "temp_pip"}, 5*time.Second, time.Millisecond)
	if err != nil {
		if unexpectedRequest != "" {
			t.Fatalf("unexpected request = %s", unexpectedRequest)
		}
		t.Fatalf("publishEnvironmentArtifactVolumesWithRetry returned error: %v", err)
	}
	if got := assets.PipVolumeID; got != "published_pip" {
		t.Fatalf("pip volume id = %q, want published_pip", got)
	}
	if forkCalls != 2 {
		t.Fatalf("fork calls = %d, want 2", forkCalls)
	}
}

func TestPublishEnvironmentArtifactVolumesRetriesActiveMountConflict(t *testing.T) {
	forkCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		forkCalls++
		if forkCalls == 1 {
			w.WriteHeader(http.StatusConflict)
			writeTestJSON(t, w, map[string]any{
				"error": map[string]any{
					"code":    "conflict",
					"message": "volume has active ctld mounts",
				},
			})
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeTestJSON(t, w, map[string]any{
			"success": true,
			"data": map[string]any{
				"id":          "published_npm",
				"team_id":     "team_123",
				"user_id":     "user_123",
				"access_mode": "ROX",
				"created_at":  "2026-04-24T00:00:00Z",
				"updated_at":  "2026-04-24T00:00:00Z",
			},
		})
	}))
	t.Cleanup(server.Close)

	mgr := &SDKRuntimeManager{cfg: Config{SandboxBaseURL: server.URL, SandboxRequestTimeout: 5 * time.Second}}
	client, err := mgr.newSandboxClient("token_123", "team_123")
	if err != nil {
		t.Fatalf("newSandboxClient returned error: %v", err)
	}
	assets, err := publishEnvironmentArtifactVolumesWithRetry(context.Background(), client, map[string]string{"npm": "temp_npm"}, 5*time.Second, time.Millisecond)
	if err != nil {
		t.Fatalf("publishEnvironmentArtifactVolumesWithRetry returned error: %v", err)
	}
	if got := assets.NPMVolumeID; got != "published_npm" {
		t.Fatalf("npm volume id = %q, want published_npm", got)
	}
	if forkCalls != 2 {
		t.Fatalf("fork calls = %d, want 2", forkCalls)
	}
}

func TestPublishEnvironmentArtifactVolumesForksManagersConcurrently(t *testing.T) {
	var (
		mu        sync.Mutex
		seen      = map[string]bool{}
		release   = make(chan struct{})
		closeOnce sync.Once
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		const prefix = "/api/v1/sandboxvolumes/"
		const suffix = "/fork"
		if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, prefix) || !strings.HasSuffix(r.URL.Path, suffix) {
			http.Error(w, `{"error":{"code":"unexpected_request","message":"unexpected request"}}`, http.StatusBadRequest)
			return
		}
		tempVolumeID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), suffix)
		mu.Lock()
		seen[tempVolumeID] = true
		if len(seen) == 2 {
			closeOnce.Do(func() { close(release) })
		}
		mu.Unlock()

		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeTestJSON(t, w, map[string]any{
			"success": true,
			"data": map[string]any{
				"id":          strings.Replace(tempVolumeID, "temp_", "published_", 1),
				"team_id":     "team_123",
				"user_id":     "user_123",
				"access_mode": "ROX",
				"created_at":  "2026-04-24T00:00:00Z",
				"updated_at":  "2026-04-24T00:00:00Z",
			},
		})
	}))
	t.Cleanup(server.Close)

	mgr := &SDKRuntimeManager{cfg: Config{SandboxBaseURL: server.URL, SandboxRequestTimeout: 5 * time.Second}}
	client, err := mgr.newSandboxClient("token_123", "team_123")
	if err != nil {
		t.Fatalf("newSandboxClient returned error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	assets, err := publishEnvironmentArtifactVolumesWithRetry(ctx, client, map[string]string{
		"npm": "temp_npm",
		"pip": "temp_pip",
	}, 500*time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatalf("publishEnvironmentArtifactVolumesWithRetry returned error: %v", err)
	}
	if assets.NPMVolumeID != "published_npm" || assets.PipVolumeID != "published_pip" {
		t.Fatalf("artifact assets = %#v, want npm and pip published", assets)
	}
}

func TestForkSessionEnvironmentVolumesUsesRWOAccessMode(t *testing.T) {
	var seenRequests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/sandboxvolumes/artifact_npm/fork" {
			http.Error(w, `{"error":{"code":"unexpected_request","message":"unexpected request"}}`, http.StatusBadRequest)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode fork payload: %v", err)
		}
		seenRequests = append(seenRequests, payload)
		w.WriteHeader(http.StatusCreated)
		writeTestJSON(t, w, map[string]any{
			"success": true,
			"data": map[string]any{
				"id":          "session_npm",
				"team_id":     "team_123",
				"user_id":     "user_123",
				"access_mode": "RWO",
				"created_at":  "2026-04-24T00:00:00Z",
				"updated_at":  "2026-04-24T00:00:00Z",
			},
		})
	}))
	t.Cleanup(server.Close)

	mgr := &SDKRuntimeManager{cfg: Config{SandboxBaseURL: server.URL, SandboxRequestTimeout: 5 * time.Second}}
	client, err := mgr.newSandboxClient("token_123", "team_123")
	if err != nil {
		t.Fatalf("newSandboxClient returned error: %v", err)
	}
	mounts, volumeIDs, err := forkSessionEnvironmentVolumes(context.Background(), client, []environmentArtifactMount{{
		manager:   "npm",
		volumeID:  "artifact_npm",
		mountPath: gatewaymanagedagents.ManagedEnvironmentNPMMountPath,
	}})
	if err != nil {
		t.Fatalf("forkSessionEnvironmentVolumes returned error: %v", err)
	}
	if len(seenRequests) != 1 || seenRequests[0]["access_mode"] != "RWO" {
		t.Fatalf("fork requests = %#v, want RWO access mode", seenRequests)
	}
	if len(mounts) != 1 || mounts[0].volumeID != "session_npm" || mounts[0].mountPath != gatewaymanagedagents.ManagedEnvironmentNPMMountPath {
		t.Fatalf("session mounts = %#v, want session npm mount", mounts)
	}
	if volumeIDs["npm"] != "session_npm" {
		t.Fatalf("volumeIDs = %#v, want npm session volume", volumeIDs)
	}
}

func testCredentialBinding(ref, sourceRef string) apispec.CredentialBinding {
	return apispec.CredentialBinding{
		Ref:       ref,
		SourceRef: sourceRef,
		Projection: apispec.ProjectionSpec{
			Type: apispec.CredentialProjectionTypeHTTPHeaders,
			HttpHeaders: apispec.NewOptHTTPHeadersProjection(apispec.HTTPHeadersProjection{
				Headers: []apispec.ProjectedHeader{{
					Name:          "Authorization",
					ValueTemplate: "{{ .authorization }}",
				}},
			}),
		},
	}
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func TestEnvironmentArtifactMountsOnlyConfiguredPackageManagers(t *testing.T) {
	environment := &gatewaymanagedagents.Environment{Config: gatewaymanagedagents.CloudConfig{
		Packages: gatewaymanagedagents.EnvironmentPackages{
			Type: "packages",
			NPM:  []string{"typescript"},
			Pip:  []string{"ruff==0.9.0"},
		},
	}}
	artifact := &gatewaymanagedagents.EnvironmentArtifact{
		ID: "art_123",
		Assets: gatewaymanagedagents.EnvironmentArtifactAssets{
			AptVolumeID: "vol_unused_apt",
			NPMVolumeID: "vol_npm",
			PipVolumeID: "vol_pip",
		},
	}

	mounts, err := environmentArtifactMounts(environment, artifact)
	if err != nil {
		t.Fatalf("environmentArtifactMounts returned error: %v", err)
	}
	if len(mounts) != 2 {
		t.Fatalf("mount count = %d, want 2: %#v", len(mounts), mounts)
	}
	if mounts[0].volumeID != "vol_npm" || mounts[0].mountPath != gatewaymanagedagents.ManagedEnvironmentNPMMountPath {
		t.Fatalf("first mount = %#v, want npm", mounts[0])
	}
	if mounts[1].volumeID != "vol_pip" || mounts[1].mountPath != gatewaymanagedagents.ManagedEnvironmentPipMountPath {
		t.Fatalf("second mount = %#v, want pip", mounts[1])
	}
}

func TestEnvironmentArtifactMountsAllowsEmptyPackageConfig(t *testing.T) {
	environment := &gatewaymanagedagents.Environment{Config: gatewaymanagedagents.CloudConfig{
		Packages: gatewaymanagedagents.EnvironmentPackages{Type: "packages"},
	}}
	mounts, err := environmentArtifactMounts(environment, &gatewaymanagedagents.EnvironmentArtifact{ID: "art_empty"})
	if err != nil {
		t.Fatalf("environmentArtifactMounts returned error: %v", err)
	}
	if len(mounts) != 0 {
		t.Fatalf("mount count = %d, want 0: %#v", len(mounts), mounts)
	}
}

func TestEnvironmentArtifactMountsRequiresConfiguredManagerVolume(t *testing.T) {
	environment := &gatewaymanagedagents.Environment{Config: gatewaymanagedagents.CloudConfig{
		Packages: gatewaymanagedagents.EnvironmentPackages{Type: "packages", Pip: []string{"ruff==0.9.0"}},
	}}

	_, err := environmentArtifactMounts(environment, &gatewaymanagedagents.EnvironmentArtifact{ID: "art_missing"})
	if err == nil {
		t.Fatal("environmentArtifactMounts error = nil, want missing pip volume error")
	}
}

func gatewayCredential(token string) gatewaymanagedagents.RequestCredential {
	return gatewaymanagedagents.RequestCredential{Token: token}
}
