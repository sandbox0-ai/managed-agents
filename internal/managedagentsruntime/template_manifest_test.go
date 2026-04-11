package managedagentsruntime

import (
	"context"
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
		TemplateMainImage:    "example.com/main:latest",
		TemplateSidecarImage: "example.com/sidecar:latest",
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
	if main.Image != "example.com/main:latest" {
		t.Fatalf("MainContainer.Image = %q", main.Image)
	}
	if len(request.Spec.Sidecars) != 1 || request.Spec.Sidecars[0].Image != "example.com/sidecar:latest" {
		t.Fatalf("Sidecars = %#v", request.Spec.Sidecars)
	}
	if request.Spec.ClusterId.IsSet() {
		t.Fatalf("ClusterId should be unset, got %#v", request.Spec.ClusterId)
	}
}

func TestEnsureManagedTemplateCreatesWhenMissing(t *testing.T) {
	mgr, err := NewSDKRuntimeManager(nil, (Config{
		Enabled:              true,
		ClaudeTemplate:       "managed-agent-claude",
		TemplateMainImage:    "example.com/main:latest",
		TemplateSidecarImage: "example.com/sidecar:latest",
	}).WithDefaults(0), zap.NewNop())
	if err != nil {
		t.Fatalf("NewSDKRuntimeManager returned error: %v", err)
	}
	client := &fakeTemplateClient{
		getErr: &sandbox0sdk.APIError{StatusCode: http.StatusNotFound},
	}
	if err := mgr.ensureManagedTemplate(context.Background(), client); err != nil {
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
		Enabled:              true,
		ClaudeTemplate:       "managed-agent-claude",
		TemplateMainImage:    "example.com/main:latest",
		TemplateSidecarImage: "example.com/sidecar:latest",
	}).WithDefaults(0), zap.NewNop())
	if err != nil {
		t.Fatalf("NewSDKRuntimeManager returned error: %v", err)
	}
	request := *mgr.templateRequest
	existing := &apispec.Template{TemplateID: request.TemplateID, Spec: request.Spec}
	existing.Spec.DisplayName = apispec.NewOptString("stale")
	client := &fakeTemplateClient{getTemplate: existing}
	if err := mgr.ensureManagedTemplate(context.Background(), client); err != nil {
		t.Fatalf("ensureManagedTemplate returned error: %v", err)
	}
	if client.updated == nil {
		t.Fatal("expected UpdateTemplate to be called")
	}
	if !reflect.DeepEqual(client.updated.Spec, request.Spec) {
		t.Fatal("updated spec does not match manifest spec")
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
