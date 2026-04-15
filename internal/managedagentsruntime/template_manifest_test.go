package managedagentsruntime

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	sandbox0sdk "github.com/sandbox0-ai/sdk-go"
	apispec "github.com/sandbox0-ai/sdk-go/pkg/apispec"
	"go.uber.org/zap"
)

func TestLoadTemplateRequest(t *testing.T) {
	request, err := loadTemplateRequest((Config{
		ClaudeTemplate:       "managed-agent-claude",
		TemplateMainImage:    "example.com/wrapper:latest",
		WrapperPort:          8080,
		WorkspaceMountPath:   "/workspace",
		EngineStateMountPath: "/var/lib/agent-wrapper",
	}).WithDefaults(0))
	if err != nil {
		t.Fatalf("loadTemplateRequest returned error: %v", err)
	}
	if request.TemplateID != "managed-agent-claude" {
		t.Fatalf("TemplateID = %q, want managed-agent-claude", request.TemplateID)
	}
	main, ok := request.Spec.MainContainer.Get()
	if !ok {
		t.Fatal("MainContainer not set")
	}
	if main.Image != "example.com/wrapper:latest" {
		t.Fatalf("MainContainer.Image = %q", main.Image)
	}
	if len(request.Spec.WarmProcesses) != 1 {
		t.Fatalf("WarmProcesses length = %d, want 1", len(request.Spec.WarmProcesses))
	}
	warmProcess := request.Spec.WarmProcesses[0]
	if warmProcess.Type != apispec.WarmProcessSpecTypeCmd {
		t.Fatalf("WarmProcesses[0].Type = %q, want cmd", warmProcess.Type)
	}
	if !reflect.DeepEqual(warmProcess.Command, []string{"node", "src/index.js"}) {
		t.Fatalf("WarmProcesses[0].Command = %#v", warmProcess.Command)
	}
	envVars, ok := warmProcess.EnvVars.Get()
	if !ok {
		t.Fatal("WarmProcesses[0].EnvVars not set")
	}
	if envVars["PORT"] != "8080" {
		t.Fatalf("WarmProcesses[0].EnvVars[PORT] = %q, want 8080", envVars["PORT"])
	}
	if _, ok := envVars["PATH"]; ok {
		t.Fatal("WarmProcesses[0].EnvVars should inherit PATH from the image")
	}
	network, ok := request.Spec.Network.Get()
	if !ok {
		t.Fatal("Network not set")
	}
	if network.Mode != apispec.SandboxNetworkPolicyModeBlockAll {
		t.Fatalf("Network.Mode = %q, want block-all", network.Mode)
	}
	if request.Spec.ClusterId.IsSet() {
		t.Fatalf("ClusterId should be unset, got %#v", request.Spec.ClusterId)
	}
}

func TestEnsureManagedTemplateCreatesWhenMissing(t *testing.T) {
	mgr, err := NewSDKRuntimeManager(nil, (Config{
		Enabled:           true,
		ClaudeTemplate:    "managed-agent-claude",
		TemplateMainImage: "example.com/wrapper:latest",
	}).WithDefaults(0), zap.NewNop())
	if err != nil {
		t.Fatalf("NewSDKRuntimeManager returned error: %v", err)
	}
	client := &fakeTemplateClient{
		getErr: &sandbox0sdk.APIError{StatusCode: http.StatusNotFound},
	}
	if err := mgr.ensureManagedTemplate(context.Background(), client, mgr.templateRequest); err != nil {
		t.Fatalf("ensureManagedTemplate returned error: %v", err)
	}
	if client.created == nil {
		t.Fatal("expected CreateTemplate to be called")
	}
	if client.created.TemplateID != "managed-agent-claude" {
		t.Fatalf("created.TemplateID = %q", client.created.TemplateID)
	}
}

func TestEnsureManagedTemplateUpdatesOnDrift(t *testing.T) {
	mgr, err := NewSDKRuntimeManager(nil, (Config{
		Enabled:           true,
		ClaudeTemplate:    "managed-agent-claude",
		TemplateMainImage: "example.com/wrapper:latest",
	}).WithDefaults(0), zap.NewNop())
	if err != nil {
		t.Fatalf("NewSDKRuntimeManager returned error: %v", err)
	}
	request := *mgr.templateRequest
	existing := &apispec.Template{TemplateID: request.TemplateID, Spec: request.Spec}
	existing.Spec.DisplayName = apispec.NewOptString("stale")
	client := &fakeTemplateClient{getTemplate: existing}
	if err := mgr.ensureManagedTemplate(context.Background(), client, mgr.templateRequest); err != nil {
		t.Fatalf("ensureManagedTemplate returned error: %v", err)
	}
	if client.updated == nil {
		t.Fatal("expected UpdateTemplate to be called")
	}
	if !reflect.DeepEqual(client.updated.Spec, request.Spec) {
		t.Fatal("updated spec does not match manifest spec")
	}
}

func TestSyncManagedTemplateOnceRequiresAdminKey(t *testing.T) {
	mgr, err := NewSDKRuntimeManager(nil, (Config{
		Enabled:           true,
		ClaudeTemplate:    "managed-agent-claude",
		TemplateMainImage: "example.com/wrapper:latest",
	}).WithDefaults(0), zap.NewNop())
	if err != nil {
		t.Fatalf("NewSDKRuntimeManager returned error: %v", err)
	}

	err = mgr.syncManagedTemplateOnce(context.Background())
	if !errors.Is(err, errManagedTemplateAdminKeyMissing) {
		t.Fatalf("syncManagedTemplateOnce error = %v, want missing admin key", err)
	}
}

func TestEnsureConfiguredManagedTemplateUsesManifestRequest(t *testing.T) {
	mgr, err := NewSDKRuntimeManager(nil, (Config{
		Enabled:           true,
		ClaudeTemplate:    "managed-agent-claude",
		TemplateMainImage: "example.com/wrapper:latest",
	}).WithDefaults(0), zap.NewNop())
	if err != nil {
		t.Fatalf("NewSDKRuntimeManager returned error: %v", err)
	}
	client := &fakeTemplateClient{
		getErr: &sandbox0sdk.APIError{StatusCode: http.StatusNotFound},
	}

	if err := mgr.ensureConfiguredManagedTemplate(context.Background(), client); err != nil {
		t.Fatalf("ensureConfiguredManagedTemplate returned error: %v", err)
	}
	if client.created == nil {
		t.Fatal("expected CreateTemplate to be called")
	}
	if client.created.TemplateID != mgr.templateRequest.TemplateID {
		t.Fatalf("created.TemplateID = %q, want %q", client.created.TemplateID, mgr.templateRequest.TemplateID)
	}
}

type fakeTemplateClient struct {
	getTemplate *apispec.Template
	getErr      error
	created     *apispec.TemplateCreateRequest
	updated     *apispec.TemplateUpdateRequest
}

func (f *fakeTemplateClient) GetTemplate(_ context.Context, _ string) (*apispec.Template, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.getTemplate, nil
}

func (f *fakeTemplateClient) CreateTemplate(_ context.Context, request apispec.TemplateCreateRequest) (*apispec.Template, error) {
	f.created = &request
	return &apispec.Template{TemplateID: request.TemplateID, Spec: request.Spec}, nil
}

func (f *fakeTemplateClient) UpdateTemplate(_ context.Context, _ string, request apispec.TemplateUpdateRequest) (*apispec.Template, error) {
	f.updated = &request
	return &apispec.Template{Spec: request.Spec}, nil
}
