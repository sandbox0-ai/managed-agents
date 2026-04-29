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
		TemplateID:         "managed-agents",
		TemplateMainImage:  "example.com/wrapper:latest",
		WrapperPort:        8080,
		WorkspaceMountPath: "/workspace",
	}).WithDefaults(0))
	if err != nil {
		t.Fatalf("loadTemplateRequest returned error: %v", err)
	}
	if request.TemplateID != "managed-agents" {
		t.Fatalf("TemplateID = %q, want managed-agents", request.TemplateID)
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
	if envVars["AGENT_WRAPPER_STATE_DIR"] != "/workspace/.sandbox0/agent-wrapper" {
		t.Fatalf("WarmProcesses[0].EnvVars[AGENT_WRAPPER_STATE_DIR] = %q, want workspace state directory", envVars["AGENT_WRAPPER_STATE_DIR"])
	}
	for _, key := range []string{"DEBUG_CLAUDE_AGENT_SDK", "CLAUDE_CODE_PROFILE_QUERY", "CLAUDE_CODE_PROFILE_STARTUP"} {
		if envVars[key] != "1" {
			t.Fatalf("WarmProcesses[0].EnvVars[%s] = %q, want 1", key, envVars[key])
		}
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
	volumeMounts, ok := request.specBody()["volumeMounts"].([]any)
	if !ok {
		t.Fatal("volumeMounts missing from raw template spec")
	}
	if len(volumeMounts) != 7 {
		t.Fatalf("volumeMounts length = %d, want 7", len(volumeMounts))
	}
	firstMount, ok := volumeMounts[0].(map[string]any)
	if !ok {
		t.Fatalf("volumeMounts[0] = %#v, want object", volumeMounts[0])
	}
	if firstMount["name"] != "workspace" || firstMount["mountPath"] != "/workspace" {
		t.Fatalf("volumeMounts[0] = %#v, want workspace mount", firstMount)
	}
}

func TestEnsureManagedTemplateCreatesWhenMissing(t *testing.T) {
	mgr, err := NewSDKRuntimeManager(nil, (Config{
		Enabled:           true,
		TemplateID:        "managed-agents",
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
	if client.created.TemplateID != "managed-agents" {
		t.Fatalf("created.TemplateID = %q", client.created.TemplateID)
	}
}

func TestEnsureManagedTemplateUpdatesOnDrift(t *testing.T) {
	mgr, err := NewSDKRuntimeManager(nil, (Config{
		Enabled:           true,
		TemplateID:        "managed-agents",
		TemplateMainImage: "example.com/wrapper:latest",
	}).WithDefaults(0), zap.NewNop())
	if err != nil {
		t.Fatalf("NewSDKRuntimeManager returned error: %v", err)
	}
	request := mgr.templateRequest
	existingSpec := request.specBody()
	existingSpec["displayName"] = "stale"
	client := &fakeTemplateClient{getSpec: existingSpec}
	if err := mgr.ensureManagedTemplate(context.Background(), client, mgr.templateRequest); err != nil {
		t.Fatalf("ensureManagedTemplate returned error: %v", err)
	}
	if client.updated == nil {
		t.Fatal("expected UpdateTemplate to be called")
	}
	if !specsEqual(client.updated.specBody(), request.specBody()) {
		t.Fatal("updated spec does not match manifest spec")
	}
}

func TestSyncManagedTemplateOnceRequiresAdminKey(t *testing.T) {
	mgr, err := NewSDKRuntimeManager(nil, (Config{
		Enabled:           true,
		TemplateID:        "managed-agents",
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
		TemplateID:        "managed-agents",
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
	getSpec map[string]any
	getErr  error
	created *managedTemplateRequest
	updated *managedTemplateRequest
}

func (f *fakeTemplateClient) GetTemplateSpec(_ context.Context, _ string) (map[string]any, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return cloneMap(f.getSpec), nil
}

func (f *fakeTemplateClient) CreateTemplate(_ context.Context, request *managedTemplateRequest) error {
	f.created, _ = cloneTemplateRequest(request)
	return nil
}

func (f *fakeTemplateClient) UpdateTemplate(_ context.Context, _ string, request *managedTemplateRequest) error {
	f.updated, _ = cloneTemplateRequest(request)
	return nil
}
