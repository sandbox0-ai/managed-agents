package managedagentsruntime

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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
	GetTemplateSpec(ctx context.Context, templateID string) (map[string]any, error)
	CreateTemplate(ctx context.Context, request *managedTemplateRequest) error
	UpdateTemplate(ctx context.Context, templateID string, request *managedTemplateRequest) error
}

type managedTemplateRequest struct {
	TemplateID string
	Spec       apispec.SandboxTemplateSpec
	raw        map[string]any
}

func loadTemplateRequest(cfg Config) (*managedTemplateRequest, error) {
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
		case "MANAGED_AGENT_TEMPLATE_WORKSPACE_STATE_PATH":
			return runtimeStateMountPath(cfg.WorkspaceMountPath)
		default:
			return os.Getenv(name)
		}
	})

	var document any
	if err := yaml.Unmarshal([]byte(rendered), &document); err != nil {
		return nil, fmt.Errorf("decode template manifest: %w", err)
	}
	normalized, ok := normalizeYAMLValue(document).(map[string]any)
	if !ok {
		return nil, errors.New("template manifest must be an object")
	}
	jsonBody, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("marshal template manifest: %w", err)
	}
	if err := json.Unmarshal(jsonBody, &normalized); err != nil {
		return nil, fmt.Errorf("normalize template manifest: %w", err)
	}

	var request apispec.TemplateCreateRequest
	if err := json.Unmarshal(jsonBody, &request); err != nil {
		return nil, fmt.Errorf("decode template request: %w", err)
	}
	if strings.TrimSpace(request.TemplateID) == "" {
		request.TemplateID = strings.TrimSpace(cfg.TemplateID)
	}
	normalized["template_id"] = request.TemplateID
	if err := validateTemplateRequest(&managedTemplateRequest{
		TemplateID: request.TemplateID,
		Spec:       request.Spec,
		raw:        normalized,
	}); err != nil {
		return nil, err
	}
	return &managedTemplateRequest{
		TemplateID: request.TemplateID,
		Spec:       request.Spec,
		raw:        normalized,
	}, nil
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

func validateTemplateRequest(request *managedTemplateRequest) error {
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

func (r *managedTemplateRequest) createBody() map[string]any {
	if r == nil {
		return nil
	}
	body := cloneMap(r.raw)
	body["template_id"] = r.TemplateID
	return body
}

func (r *managedTemplateRequest) updateBody() map[string]any {
	if r == nil {
		return nil
	}
	return map[string]any{"spec": r.specBody()}
}

func (r *managedTemplateRequest) specBody() map[string]any {
	if r == nil {
		return nil
	}
	if spec, ok := r.raw["spec"].(map[string]any); ok {
		return cloneMap(spec)
	}
	return nil
}

func cloneTemplateRequest(request *managedTemplateRequest) (*managedTemplateRequest, error) {
	if request == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(request.createBody())
	if err != nil {
		return nil, fmt.Errorf("marshal template request: %w", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		return nil, fmt.Errorf("clone template request raw body: %w", err)
	}
	var typed apispec.TemplateCreateRequest
	if err := json.Unmarshal(encoded, &typed); err != nil {
		return nil, fmt.Errorf("clone template request: %w", err)
	}
	return &managedTemplateRequest{
		TemplateID: typed.TemplateID,
		Spec:       typed.Spec,
		raw:        raw,
	}, nil
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

func (m *SDKRuntimeManager) ensureManagedTemplate(ctx context.Context, client templateClient, request *managedTemplateRequest) error {
	if request == nil {
		return nil
	}
	existingSpec, err := client.GetTemplateSpec(ctx, request.TemplateID)
	if err != nil {
		var apiErr *sandbox0sdk.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return client.CreateTemplate(ctx, request)
		}
		return err
	}
	if specsEqual(existingSpec, request.specBody()) {
		return nil
	}
	return client.UpdateTemplate(ctx, request.TemplateID, request)
}

func specsEqual(left, right map[string]any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return bytes.Equal(leftJSON, rightJSON)
}

type sandboxTemplateHTTPClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func (c *sandboxTemplateHTTPClient) GetTemplateSpec(ctx context.Context, templateID string) (map[string]any, error) {
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/templates/"+url.PathEscape(templateID), nil, &envelope); err != nil {
		return nil, err
	}
	spec, _ := envelope.Data["spec"].(map[string]any)
	if spec == nil {
		return nil, &sandbox0sdk.APIError{StatusCode: http.StatusOK, Code: "unexpected_response", Message: "template response missing spec"}
	}
	return spec, nil
}

func (c *sandboxTemplateHTTPClient) CreateTemplate(ctx context.Context, request *managedTemplateRequest) error {
	return c.doJSON(ctx, http.MethodPost, "/api/v1/templates", request.createBody(), nil)
}

func (c *sandboxTemplateHTTPClient) UpdateTemplate(ctx context.Context, templateID string, request *managedTemplateRequest) error {
	return c.doJSON(ctx, http.MethodPut, "/api/v1/templates/"+url.PathEscape(templateID), request.updateBody(), nil)
}

func (c *sandboxTemplateHTTPClient) doJSON(ctx context.Context, method, path string, body any, out any) error {
	var bodyReader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.baseURL, "/")+path, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(c.token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.token))
	}
	httpClient := c.httpClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return sandboxAPIError(resp.StatusCode, responseBody)
	}
	if out == nil || len(bytes.TrimSpace(responseBody)) == 0 {
		return nil
	}
	if err := json.Unmarshal(responseBody, out); err != nil {
		return fmt.Errorf("decode template response: %w", err)
	}
	return nil
}

func sandboxAPIError(statusCode int, body []byte) *sandbox0sdk.APIError {
	apiErr := &sandbox0sdk.APIError{
		StatusCode: statusCode,
		Code:       "unknown_error",
		Message:    strings.TrimSpace(string(body)),
		Body:       append([]byte(nil), body...),
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details any    `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil {
		if strings.TrimSpace(envelope.Error.Code) != "" {
			apiErr.Code = envelope.Error.Code
		}
		if strings.TrimSpace(envelope.Error.Message) != "" {
			apiErr.Message = envelope.Error.Message
		}
		apiErr.Details = envelope.Error.Details
	}
	return apiErr
}
