package managedagentsruntime

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"

	sandbox0sdk "github.com/sandbox0-ai/sdk-go"
	apispec "github.com/sandbox0-ai/sdk-go/pkg/apispec"
	"gopkg.in/yaml.v3"
)

const defaultTemplateManifestPath = "templates/managed-agents.yaml"

//go:embed templates/*.yaml
var templateManifestFS embed.FS

type templateClient interface {
	GetTemplate(ctx context.Context, templateID string) (*apispec.Template, error)
	CreateTemplate(ctx context.Context, request apispec.TemplateCreateRequest) (*apispec.Template, error)
	UpdateTemplate(ctx context.Context, templateID string, request apispec.TemplateUpdateRequest) (*apispec.Template, error)
}

func loadTemplateRequest(cfg Config) (*apispec.TemplateCreateRequest, error) {
	raw, err := readTemplateManifest(cfg.TemplateManifestPath)
	if err != nil {
		return nil, err
	}
	rendered := os.Expand(string(raw), func(name string) string {
		switch name {
		case "MANAGED_AGENT_TEMPLATE_ID":
			return strings.TrimSpace(cfg.TemplateID)
		case "MANAGED_AGENT_TEMPLATE_MAIN_IMAGE":
			return strings.TrimSpace(cfg.TemplateMainImage)
		case "MANAGED_AGENT_TEMPLATE_WRAPPER_PORT":
			return strconv.Itoa(cfg.WrapperPort)
		case "MANAGED_AGENT_TEMPLATE_WORKSPACE_MOUNT_PATH":
			return strings.TrimSpace(cfg.WorkspaceMountPath)
		case "MANAGED_AGENT_TEMPLATE_ENGINE_STATE_MOUNT_PATH":
			return strings.TrimSpace(cfg.EngineStateMountPath)
		default:
			return os.Getenv(name)
		}
	})

	var document any
	if err := yaml.Unmarshal([]byte(rendered), &document); err != nil {
		return nil, fmt.Errorf("decode template manifest: %w", err)
	}
	jsonBody, err := json.Marshal(normalizeYAMLValue(document))
	if err != nil {
		return nil, fmt.Errorf("marshal template manifest: %w", err)
	}

	var request apispec.TemplateCreateRequest
	if err := json.Unmarshal(jsonBody, &request); err != nil {
		return nil, fmt.Errorf("decode template request: %w", err)
	}
	if strings.TrimSpace(request.TemplateID) == "" {
		request.TemplateID = strings.TrimSpace(cfg.TemplateID)
	}
	if err := validateTemplateRequest(&request); err != nil {
		return nil, err
	}
	return &request, nil
}

func readTemplateManifest(path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path != "" {
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read template manifest %q: %w", path, err)
		}
		return body, nil
	}
	body, err := templateManifestFS.ReadFile(defaultTemplateManifestPath)
	if err != nil {
		return nil, fmt.Errorf("read embedded template manifest %q: %w", defaultTemplateManifestPath, err)
	}
	return body, nil
}

func validateTemplateRequest(request *apispec.TemplateCreateRequest) error {
	if request == nil {
		return errors.New("template request is required")
	}
	if strings.TrimSpace(request.TemplateID) == "" {
		return errors.New("template request template_id is required")
	}
	main, ok := request.Spec.MainContainer.Get()
	if !ok {
		return errors.New("template request mainContainer is required")
	}
	if strings.TrimSpace(main.Image) == "" {
		return errors.New("template request mainContainer.image is required")
	}
	if len(request.Spec.WarmProcesses) == 0 {
		return errors.New("template request warmProcesses is required")
	}
	for i, process := range request.Spec.WarmProcesses {
		switch process.Type {
		case apispec.WarmProcessSpecTypeCmd:
			if len(process.Command) == 0 || strings.TrimSpace(process.Command[0]) == "" {
				return fmt.Errorf("template request warmProcesses[%d].command[0] is required", i)
			}
		case apispec.WarmProcessSpecTypeRepl:
		default:
			return fmt.Errorf("template request warmProcesses[%d].type is invalid", i)
		}
	}
	return nil
}

func normalizeYAMLValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized[key] = normalizeYAMLValue(item)
		}
		return normalized
	case map[any]any:
		normalized := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized[fmt.Sprint(key)] = normalizeYAMLValue(item)
		}
		return normalized
	case []any:
		normalized := make([]any, len(typed))
		for i, item := range typed {
			normalized[i] = normalizeYAMLValue(item)
		}
		return normalized
	default:
		return value
	}
}

func (m *SDKRuntimeManager) ensureManagedTemplate(ctx context.Context, client templateClient, request *apispec.TemplateCreateRequest) error {
	if request == nil {
		return nil
	}
	existing, err := client.GetTemplate(ctx, request.TemplateID)
	if err != nil {
		var apiErr *sandbox0sdk.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			_, err = client.CreateTemplate(ctx, *request)
			return err
		}
		return err
	}
	if reflect.DeepEqual(existing.Spec, request.Spec) {
		return nil
	}
	_, err = client.UpdateTemplate(ctx, request.TemplateID, apispec.TemplateUpdateRequest{Spec: request.Spec})
	return err
}
