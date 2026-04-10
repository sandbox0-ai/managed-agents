package managedagentsruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
	sandbox0sdk "github.com/sandbox0-ai/sdk-go"
	apispec "github.com/sandbox0-ai/sdk-go/pkg/apispec"
	"go.uber.org/zap"
)

// Config configures sandbox-backed Claude managed-agent runtimes.
type Config struct {
	Enabled               bool
	ClaudeTemplate        string
	WrapperPort           int
	WorkspaceMountPath    string
	EngineStateMountPath  string
	SandboxTTLSeconds     int
	SandboxHardTTLSeconds int
	SandboxRequestTimeout time.Duration
	SandboxBaseURL        string
	RegionID              string
}

// WithDefaults fills missing fields with stable local defaults.
func (c Config) WithDefaults(httpPort int) Config {
	if strings.TrimSpace(c.ClaudeTemplate) == "" {
		c.ClaudeTemplate = "managed-agent-claude"
	}
	if c.WrapperPort == 0 {
		c.WrapperPort = 8080
	}
	if strings.TrimSpace(c.WorkspaceMountPath) == "" {
		c.WorkspaceMountPath = "/workspace"
	}
	if strings.TrimSpace(c.EngineStateMountPath) == "" {
		c.EngineStateMountPath = "/var/lib/agent-wrapper"
	}
	if c.SandboxTTLSeconds == 0 {
		c.SandboxTTLSeconds = 3600
	}
	if c.SandboxHardTTLSeconds == 0 {
		c.SandboxHardTTLSeconds = 21600
	}
	if c.SandboxRequestTimeout <= 0 {
		c.SandboxRequestTimeout = 60 * time.Second
	}
	if strings.TrimSpace(c.SandboxBaseURL) == "" {
		c.SandboxBaseURL = fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	}
	return c
}

// SDKRuntimeManager provisions sandboxes and talks to the wrapper over an exposed HTTP endpoint.
type SDKRuntimeManager struct {
	repo       *gatewaymanagedagents.Repository
	cfg        Config
	logger     *zap.Logger
	httpClient *http.Client
}

// NewSDKRuntimeManager creates a runtime manager backed by sandbox0 sdk-go.
func NewSDKRuntimeManager(repo *gatewaymanagedagents.Repository, cfg Config, logger *zap.Logger) *SDKRuntimeManager {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &SDKRuntimeManager{
		repo:       repo,
		cfg:        cfg,
		logger:     logger,
		httpClient: &http.Client{Timeout: cfg.SandboxRequestTimeout},
	}
}

func (m *SDKRuntimeManager) EnsureRuntime(ctx context.Context, _ gatewaymanagedagents.Principal, credential gatewaymanagedagents.RequestCredential, session *gatewaymanagedagents.SessionRecord, engine map[string]any, gatewayBaseURL string) (*gatewaymanagedagents.RuntimeRecord, error) {
	if strings.TrimSpace(credential.Token) == "" {
		return nil, errors.New("request credential is required")
	}
	if strings.TrimSpace(gatewayBaseURL) == "" {
		return nil, errors.New("gateway base url is required")
	}
	if runtime, err := m.repo.GetRuntime(ctx, session.ID); err == nil {
		return m.ensureWrapperEndpoint(ctx, credential.Token, runtime)
	} else if !errors.Is(err, gatewaymanagedagents.ErrRuntimeNotFound) {
		return nil, err
	}
	regionID, err := m.repo.ResolveRuntimeRegionID(ctx, session.TeamID, m.cfg.RegionID)
	if err != nil {
		return nil, err
	}
	client, err := m.newSandboxClient(credential.Token)
	if err != nil {
		return nil, err
	}
	workspaceVolume, err := client.CreateVolume(ctx, apispec.CreateSandboxVolumeRequest{})
	if err != nil {
		return nil, fmt.Errorf("create workspace volume: %w", err)
	}
	engineStateVolume, err := client.CreateVolume(ctx, apispec.CreateSandboxVolumeRequest{})
	if err != nil {
		return nil, fmt.Errorf("create engine-state volume: %w", err)
	}
	controlToken := gatewaymanagedagents.NewID("ctl")
	claimOpts := []sandbox0sdk.SandboxOption{
		sandbox0sdk.WithSandboxBootstrapMount(workspaceVolume.ID, m.cfg.WorkspaceMountPath, nil),
		sandbox0sdk.WithSandboxBootstrapMount(engineStateVolume.ID, m.cfg.EngineStateMountPath, nil),
		sandbox0sdk.WithSandboxBootstrapMountWait(m.cfg.SandboxRequestTimeout),
		sandbox0sdk.WithSandboxTTL(int32(m.cfg.SandboxTTLSeconds)),
		sandbox0sdk.WithSandboxHardTTL(int32(m.cfg.SandboxHardTTLSeconds)),
		sandbox0sdk.WithSandboxWebhook(gatewaymanagedagents.InternalSandboxWebhookURL(gatewayBaseURL), controlToken),
		sandbox0sdk.WithSandboxEnvVars(map[string]string{
			"AGENT_WRAPPER_CONTROL_TOKEN": controlToken,
		}),
	}
	if networkPolicy, ok := decodeNetworkPolicy(engine); ok {
		claimOpts = append(claimOpts, sandbox0sdk.WithSandboxNetworkPolicy(networkPolicy))
	}
	sandbox, err := client.ClaimSandbox(ctx, m.templateForVendor(session.Vendor), claimOpts...)
	if err != nil {
		return nil, fmt.Errorf("claim sandbox: %w", err)
	}
	publicURL, err := m.exposeWrapperPort(ctx, client.Sandbox(sandbox.ID))
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	runtime := &gatewaymanagedagents.RuntimeRecord{
		SessionID:           session.ID,
		Vendor:              session.Vendor,
		RegionID:            regionID,
		SandboxID:           sandbox.ID,
		WrapperURL:          publicURL,
		WorkspaceVolumeID:   workspaceVolume.ID,
		EngineStateVolumeID: engineStateVolume.ID,
		ControlToken:        controlToken,
		RuntimeGeneration:   1,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if err := m.repo.UpsertRuntime(ctx, runtime); err != nil {
		return nil, err
	}
	return runtime, nil
}

func (m *SDKRuntimeManager) BootstrapSession(ctx context.Context, credential gatewaymanagedagents.RequestCredential, runtime *gatewaymanagedagents.RuntimeRecord, req *gatewaymanagedagents.WrapperSessionBootstrapRequest) error {
	return m.wrapperJSON(ctx, credential, runtime, http.MethodPut, "/v1/runtime/session", req)
}

func (m *SDKRuntimeManager) StartRun(ctx context.Context, credential gatewaymanagedagents.RequestCredential, runtime *gatewaymanagedagents.RuntimeRecord, req *gatewaymanagedagents.WrapperRunRequest) error {
	return m.wrapperJSON(ctx, credential, runtime, http.MethodPost, "/v1/runs", req)
}

func (m *SDKRuntimeManager) ResolveActions(ctx context.Context, credential gatewaymanagedagents.RequestCredential, runtime *gatewaymanagedagents.RuntimeRecord, req *gatewaymanagedagents.WrapperResolveActionsRequest) (*gatewaymanagedagents.WrapperResolveActionsResponse, error) {
	var response gatewaymanagedagents.WrapperResolveActionsResponse
	if err := m.wrapperJSONDecode(ctx, credential, runtime, http.MethodPost, "/v1/runtime/session/"+url.PathEscape(req.SessionID)+"/actions/resolve", req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (m *SDKRuntimeManager) InterruptRun(ctx context.Context, credential gatewaymanagedagents.RequestCredential, runtime *gatewaymanagedagents.RuntimeRecord, runID string) error {
	return m.wrapperJSON(ctx, credential, runtime, http.MethodPost, "/v1/runs/"+url.PathEscape(runID)+"/interrupt", nil)
}

func (m *SDKRuntimeManager) DeleteWrapperSession(ctx context.Context, credential gatewaymanagedagents.RequestCredential, runtime *gatewaymanagedagents.RuntimeRecord, sessionID string) error {
	return m.wrapperJSON(ctx, credential, runtime, http.MethodDelete, "/v1/runtime/session/"+url.PathEscape(sessionID), nil)
}

func (m *SDKRuntimeManager) DestroyRuntime(ctx context.Context, credential gatewaymanagedagents.RequestCredential, runtime *gatewaymanagedagents.RuntimeRecord) error {
	client, err := m.newSandboxClient(credential.Token)
	if err != nil {
		return err
	}
	if _, err := client.DeleteSandbox(ctx, runtime.SandboxID); err != nil {
		m.logger.Warn("delete sandbox failed", zap.Error(err), zap.String("sandbox_id", runtime.SandboxID))
	}
	if runtime.WorkspaceVolumeID != "" {
		if _, err := client.DeleteVolumeWithOptions(ctx, runtime.WorkspaceVolumeID, &sandbox0sdk.DeleteVolumeOptions{Force: true}); err != nil {
			m.logger.Warn("delete workspace volume failed", zap.Error(err), zap.String("volume_id", runtime.WorkspaceVolumeID))
		}
	}
	if runtime.EngineStateVolumeID != "" {
		if _, err := client.DeleteVolumeWithOptions(ctx, runtime.EngineStateVolumeID, &sandbox0sdk.DeleteVolumeOptions{Force: true}); err != nil {
			m.logger.Warn("delete engine-state volume failed", zap.Error(err), zap.String("volume_id", runtime.EngineStateVolumeID))
		}
	}
	return nil
}

func (m *SDKRuntimeManager) newSandboxClient(token string) (*sandbox0sdk.Client, error) {
	return sandbox0sdk.NewClient(
		sandbox0sdk.WithBaseURL(strings.TrimRight(m.cfg.SandboxBaseURL, "/")),
		sandbox0sdk.WithToken(token),
		sandbox0sdk.WithTimeout(m.cfg.SandboxRequestTimeout),
	)
}

func (m *SDKRuntimeManager) templateForVendor(vendor string) string {
	return m.cfg.ClaudeTemplate
}

func (m *SDKRuntimeManager) ensureWrapperEndpoint(ctx context.Context, token string, runtime *gatewaymanagedagents.RuntimeRecord) (*gatewaymanagedagents.RuntimeRecord, error) {
	if strings.TrimSpace(runtime.WrapperURL) != "" {
		return runtime, nil
	}
	client, err := m.newSandboxClient(token)
	if err != nil {
		return nil, err
	}
	publicURL, err := m.exposeWrapperPort(ctx, client.Sandbox(runtime.SandboxID))
	if err != nil {
		return nil, err
	}
	runtime.WrapperURL = publicURL
	runtime.UpdatedAt = time.Now().UTC()
	if err := m.repo.UpsertRuntime(ctx, runtime); err != nil {
		return nil, err
	}
	return runtime, nil
}

func (m *SDKRuntimeManager) exposeWrapperPort(ctx context.Context, sandbox *sandbox0sdk.Sandbox) (string, error) {
	exposed, err := sandbox.ExposePort(ctx, int32(m.cfg.WrapperPort), true)
	if err != nil {
		return "", fmt.Errorf("expose wrapper port: %w", err)
	}
	for _, port := range exposed.Ports {
		if int(port.Port) == m.cfg.WrapperPort && strings.TrimSpace(port.PublicURL) != "" {
			return strings.TrimSpace(port.PublicURL), nil
		}
	}
	return "", errors.New("wrapper public url is required")
}

func (m *SDKRuntimeManager) wrapperJSON(ctx context.Context, credential gatewaymanagedagents.RequestCredential, runtime *gatewaymanagedagents.RuntimeRecord, method, path string, payload any) error {
	return m.wrapperJSONDecode(ctx, credential, runtime, method, path, payload, nil)
}

func (m *SDKRuntimeManager) wrapperJSONDecode(ctx context.Context, credential gatewaymanagedagents.RequestCredential, runtime *gatewaymanagedagents.RuntimeRecord, method, path string, payload any, out any) error {
	runtime, err := m.ensureWrapperEndpoint(ctx, credential.Token, runtime)
	if err != nil {
		return err
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal wrapper payload: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	requestURL := strings.TrimRight(runtime.WrapperURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return fmt.Errorf("build wrapper request: %w", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(runtime.ControlToken) != "" {
		req.Header.Set("Authorization", "Bearer "+runtime.ControlToken)
	}
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("call wrapper: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read wrapper response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("wrapper call failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	if out != nil && len(bytes.TrimSpace(responseBody)) > 0 {
		if err := json.Unmarshal(responseBody, out); err != nil {
			return fmt.Errorf("decode wrapper response: %w", err)
		}
	}
	return nil
}

func decodeNetworkPolicy(engine map[string]any) (apispec.SandboxNetworkPolicy, bool) {
	if engine == nil {
		return apispec.SandboxNetworkPolicy{}, false
	}
	raw, ok := engine["network"]
	if !ok || raw == nil {
		return apispec.SandboxNetworkPolicy{}, false
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return apispec.SandboxNetworkPolicy{}, false
	}
	var policy apispec.SandboxNetworkPolicy
	if err := json.Unmarshal(encoded, &policy); err != nil {
		return apispec.SandboxNetworkPolicy{}, false
	}
	return policy, true
}
