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
	"sort"
	"strings"
	"time"

	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
	sandbox0sdk "github.com/sandbox0-ai/sdk-go"
	apispec "github.com/sandbox0-ai/sdk-go/pkg/apispec"
	"go.uber.org/zap"
)

const (
	DefaultSandboxTTLSeconds = 180
	defaultTemplateID        = "managed-agents"
)

// Config configures sandbox-backed managed-agent runtimes.
type Config struct {
	Enabled                bool
	TemplateID             string
	TemplateManifestPath   string
	TemplateMainImage      string
	WrapperPort            int
	WorkspaceMountPath     string
	SandboxTTLSeconds      int
	SandboxRequestTimeout  time.Duration
	SandboxBaseURL         string
	SandboxAdminAPIKey     string
	RuntimeCallbackBaseURL string
	RuntimeAllowedDomains  []string
}

// WithDefaults fills missing fields with stable local defaults.
func (c Config) WithDefaults(httpPort int) Config {
	if strings.TrimSpace(c.TemplateID) == "" {
		c.TemplateID = defaultTemplateID
	}
	if strings.TrimSpace(c.TemplateMainImage) == "" {
		c.TemplateMainImage = "sandbox0ai/managed-agents:wrapper-latest"
	}
	if c.WrapperPort == 0 {
		c.WrapperPort = 8080
	}
	if strings.TrimSpace(c.WorkspaceMountPath) == "" {
		c.WorkspaceMountPath = "/workspace"
	}
	c.WorkspaceMountPath = cleanMountPath(c.WorkspaceMountPath)
	if c.WorkspaceMountPath == "" {
		c.WorkspaceMountPath = "/workspace"
	}
	if c.SandboxTTLSeconds <= 0 {
		c.SandboxTTLSeconds = DefaultSandboxTTLSeconds
	}
	if c.SandboxRequestTimeout <= 0 {
		c.SandboxRequestTimeout = 90 * time.Second
	}
	if strings.TrimSpace(c.SandboxBaseURL) == "" {
		c.SandboxBaseURL = fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	}
	return c
}

// SDKRuntimeManager provisions sandboxes and talks to the wrapper over an exposed HTTP endpoint.
type SDKRuntimeManager struct {
	repo            *gatewaymanagedagents.Repository
	cfg             Config
	logger          *zap.Logger
	httpClient      *http.Client
	templateRequest *apispec.TemplateCreateRequest
	observability   *gatewaymanagedagents.Observability
}

type RuntimeManagerOption func(*SDKRuntimeManager)

func WithObservability(observability *gatewaymanagedagents.Observability) RuntimeManagerOption {
	return func(m *SDKRuntimeManager) {
		m.observability = observability
	}
}

func WithHTTPClient(client *http.Client) RuntimeManagerOption {
	return func(m *SDKRuntimeManager) {
		if client != nil {
			m.httpClient = client
		}
	}
}

// NewSDKRuntimeManager creates a runtime manager backed by sandbox0 sdk-go.
func NewSDKRuntimeManager(repo *gatewaymanagedagents.Repository, cfg Config, logger *zap.Logger, opts ...RuntimeManagerOption) (*SDKRuntimeManager, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	manager := &SDKRuntimeManager{
		repo:       repo,
		cfg:        cfg,
		logger:     logger,
		httpClient: &http.Client{Timeout: cfg.SandboxRequestTimeout},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(manager)
		}
	}
	if cfg.Enabled {
		request, err := loadTemplateRequest(cfg)
		if err != nil {
			return nil, err
		}
		manager.templateRequest = request
	}
	return manager, nil
}

func (m *SDKRuntimeManager) EnsureRuntime(ctx context.Context, _ gatewaymanagedagents.Principal, credential gatewaymanagedagents.RequestCredential, session *gatewaymanagedagents.SessionRecord, engine map[string]any, gatewayBaseURL string) (runtime *gatewaymanagedagents.RuntimeRecord, err error) {
	ctx, op := m.observability.StartOperation(ctx, "runtime_ensure", sessionVendorForLog(session),
		zap.String("team_id", sessionTeamIDForLog(session)),
		zap.String("session_id", sessionIDForLog(session)),
	)
	defer func() { op.End(err) }()
	if strings.TrimSpace(credential.Token) == "" {
		return nil, errors.New("request credential is required")
	}
	if strings.TrimSpace(gatewayBaseURL) == "" {
		return nil, errors.New("gateway base url is required")
	}
	var existingRuntime *gatewaymanagedagents.RuntimeRecord
	phaseStarted := time.Now()
	if runtime, err := m.repo.GetRuntime(ctx, session.ID); err == nil {
		if strings.TrimSpace(runtime.SandboxID) != "" {
			op.ObservePhase("load_existing_runtime", time.Since(phaseStarted), nil,
				zap.String("sandbox_id", runtime.SandboxID),
			)
			phaseStarted = time.Now()
			if err := m.configureRuntimeSandboxLifecycle(ctx, credential, runtime); err != nil {
				op.ObservePhase("configure_existing_lifecycle", time.Since(phaseStarted), err,
					zap.String("sandbox_id", runtime.SandboxID),
				)
				m.logger.Warn("configure existing runtime sandbox lifecycle failed", zap.Error(err), zap.String("session_id", runtimeSessionID(runtime)), zap.String("sandbox_id", runtimeSandboxID(runtime)))
			} else {
				op.ObservePhase("configure_existing_lifecycle", time.Since(phaseStarted), nil,
					zap.String("sandbox_id", runtime.SandboxID),
				)
			}
			phaseStarted = time.Now()
			ensured, ensureErr := m.ensureWrapperEndpoint(ctx, credential.Token, runtime)
			op.ObservePhase("ensure_wrapper_endpoint", time.Since(phaseStarted), ensureErr,
				zap.String("sandbox_id", runtime.SandboxID),
			)
			if ensureErr != nil {
				return nil, ensureErr
			}
			return ensured, nil
		}
		existingRuntime = runtime
	} else if !errors.Is(err, gatewaymanagedagents.ErrRuntimeNotFound) {
		op.ObservePhase("load_existing_runtime", time.Since(phaseStarted), err)
		return nil, err
	}
	op.ObservePhase("load_existing_runtime", time.Since(phaseStarted), nil,
		zap.Bool("existing_runtime_without_sandbox", existingRuntime != nil),
	)
	phaseStarted = time.Now()
	regionID, err := m.repo.ResolveRuntimeRegionID(ctx, session.TeamID)
	if err != nil {
		op.ObservePhase("resolve_region", time.Since(phaseStarted), err)
		return nil, err
	}
	op.ObservePhase("resolve_region", time.Since(phaseStarted), nil,
		zap.String("region_id", regionID),
	)
	phaseStarted = time.Now()
	client, err := m.newSandboxClient(credential.Token, session.TeamID)
	if err != nil {
		op.ObservePhase("create_sandbox_client", time.Since(phaseStarted), err)
		return nil, err
	}
	op.ObservePhase("create_sandbox_client", time.Since(phaseStarted), nil)
	phaseStarted = time.Now()
	templateClient, err := m.templateClient(ctx, credential, session.TeamID)
	if err != nil {
		op.ObservePhase("create_template_client", time.Since(phaseStarted), err)
		return nil, err
	}
	op.ObservePhase("create_template_client", time.Since(phaseStarted), nil)
	phaseStarted = time.Now()
	environment, err := m.repo.GetEnvironment(ctx, session.TeamID, session.EnvironmentID)
	if err != nil {
		op.ObservePhase("load_environment", time.Since(phaseStarted), err)
		return nil, fmt.Errorf("resolve environment: %w", err)
	}
	op.ObservePhase("load_environment", time.Since(phaseStarted), nil)
	phaseStarted = time.Now()
	templateRequest, err := m.templateRequestForEnvironment(environment)
	if err != nil {
		op.ObservePhase("prepare_template_request", time.Since(phaseStarted), err)
		return nil, fmt.Errorf("prepare environment template: %w", err)
	}
	op.ObservePhase("prepare_template_request", time.Since(phaseStarted), nil)
	phaseStarted = time.Now()
	if err := m.ensureManagedTemplate(ctx, templateClient, templateRequest); err != nil {
		op.ObservePhase("ensure_template", time.Since(phaseStarted), err)
		return nil, fmt.Errorf("ensure managed template: %w", err)
	}
	op.ObservePhase("ensure_template", time.Since(phaseStarted), nil)
	phaseStarted = time.Now()
	artifact, err := m.resolveReadyEnvironmentArtifact(ctx, credential, session, environment, templateRequest, templateClient)
	if err != nil {
		op.ObservePhase("resolve_environment_artifact", time.Since(phaseStarted), err)
		return nil, fmt.Errorf("resolve environment artifact: %w", err)
	}
	op.ObservePhase("resolve_environment_artifact", time.Since(phaseStarted), nil,
		zap.String("environment_artifact_id", artifact.ID),
		zap.String("environment_artifact_status", artifact.Status),
	)
	workspaceVolumeID := ""
	createdWorkspaceVolume := false
	if existingRuntime != nil {
		workspaceVolumeID = strings.TrimSpace(existingRuntime.WorkspaceVolumeID)
	}
	if workspaceVolumeID == "" {
		phaseStarted = time.Now()
		workspaceVolume, err := client.CreateVolume(ctx, apispec.CreateSandboxVolumeRequest{})
		if err != nil {
			op.ObservePhase("create_workspace_volume", time.Since(phaseStarted), err)
			return nil, fmt.Errorf("create workspace volume: %w", err)
		}
		workspaceVolumeID = workspaceVolume.ID
		createdWorkspaceVolume = true
		op.ObservePhase("create_workspace_volume", time.Since(phaseStarted), nil,
			zap.String("workspace_volume_id", workspaceVolumeID),
		)
	} else {
		op.ObservePhase("reuse_workspace_volume", 0, nil,
			zap.String("workspace_volume_id", workspaceVolumeID),
		)
	}
	sandboxID := ""
	cleanupPending := true
	defer func() {
		if !cleanupPending {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.cfg.SandboxRequestTimeout)
		defer cancel()
		if sandboxID != "" {
			if _, err := client.DeleteSandbox(cleanupCtx, sandboxID); err != nil {
				m.logger.Warn("delete sandbox after runtime claim failed", zap.Error(err), zap.String("sandbox_id", sandboxID))
			}
		}
		if createdWorkspaceVolume && workspaceVolumeID != "" {
			if _, err := client.DeleteVolumeWithOptions(cleanupCtx, workspaceVolumeID, &sandbox0sdk.DeleteVolumeOptions{Force: true}); err != nil {
				m.logger.Warn("delete workspace volume after runtime claim failed", zap.Error(err), zap.String("volume_id", workspaceVolumeID))
			}
		}
	}()
	controlToken := gatewaymanagedagents.NewID("ctl")
	claimOpts := []sandbox0sdk.SandboxOption{
		sandbox0sdk.WithSandboxBootstrapMount(workspaceVolumeID, m.cfg.WorkspaceMountPath, nil),
		sandbox0sdk.WithSandboxBootstrapMountWait(m.cfg.SandboxRequestTimeout),
		sandbox0sdk.WithSandboxTTL(int32(m.sandboxTTLSeconds())),
		sandbox0sdk.WithSandboxHardTTL(0),
		sandbox0sdk.WithSandboxAutoResume(true),
		sandbox0sdk.WithSandboxWebhook(m.runtimeWebhookURL(gatewayBaseURL), controlToken),
		sandbox0sdk.WithSandboxEnvVars(map[string]string{
			"AGENT_WRAPPER_CONTROL_TOKEN": controlToken,
		}),
	}
	phaseStarted = time.Now()
	packageMounts, err := environmentArtifactMounts(environment, artifact)
	if err != nil {
		op.ObservePhase("prepare_environment_artifact_mounts", time.Since(phaseStarted), err)
		return nil, err
	}
	op.ObservePhase("prepare_environment_artifact_mounts", time.Since(phaseStarted), nil,
		zap.Int("package_mount_count", len(packageMounts)),
	)
	for _, mount := range packageMounts {
		claimOpts = append(claimOpts, sandbox0sdk.WithSandboxBootstrapMount(mount.volumeID, mount.mountPath, nil))
	}
	claimOpts = append(claimOpts, sandbox0sdk.WithSandboxNetworkPolicy(m.runtimeNetworkPolicy(environment, engine, session.Agent)))
	phaseStarted = time.Now()
	sandbox, err := client.ClaimSandbox(ctx, m.templateIDForSession(session.Vendor, templateRequest), claimOpts...)
	if err != nil {
		op.ObservePhase("claim_sandbox", time.Since(phaseStarted), err,
			zap.Int("bootstrap_mount_count", len(packageMounts)+1),
		)
		return nil, fmt.Errorf("claim sandbox: %w", err)
	}
	sandboxID = sandbox.ID
	op.ObservePhase("claim_sandbox", time.Since(phaseStarted), nil,
		zap.String("sandbox_id", sandboxID),
		zap.Int("bootstrap_mount_count", len(packageMounts)+1),
	)
	phaseStarted = time.Now()
	publicURL, err := m.exposeWrapperPort(ctx, client.Sandbox(sandbox.ID))
	if err != nil {
		op.ObservePhase("expose_wrapper_port", time.Since(phaseStarted), err,
			zap.String("sandbox_id", sandboxID),
		)
		return nil, err
	}
	op.ObservePhase("expose_wrapper_port", time.Since(phaseStarted), nil,
		zap.String("sandbox_id", sandboxID),
	)
	now := time.Now().UTC()
	runtimeGeneration := int64(1)
	createdAt := now
	vendorSessionID := ""
	var activeRunID *string
	if existingRuntime != nil {
		runtimeGeneration = existingRuntime.RuntimeGeneration + 1
		if runtimeGeneration <= 1 {
			runtimeGeneration = 2
		}
		createdAt = existingRuntime.CreatedAt
		if createdAt.IsZero() {
			createdAt = now
		}
		vendorSessionID = existingRuntime.VendorSessionID
		activeRunID = existingRuntime.ActiveRunID
		if strings.TrimSpace(existingRuntime.RegionID) != "" {
			regionID = existingRuntime.RegionID
		}
	}
	runtime = &gatewaymanagedagents.RuntimeRecord{
		SessionID:         session.ID,
		Vendor:            session.Vendor,
		RegionID:          regionID,
		SandboxID:         sandboxID,
		WrapperURL:        publicURL,
		WorkspaceVolumeID: workspaceVolumeID,
		ControlToken:      controlToken,
		VendorSessionID:   vendorSessionID,
		RuntimeGeneration: runtimeGeneration,
		ActiveRunID:       activeRunID,
		CreatedAt:         createdAt,
		UpdatedAt:         now,
	}
	phaseStarted = time.Now()
	if err := m.repo.UpsertRuntime(ctx, runtime); err != nil {
		op.ObservePhase("upsert_runtime_record", time.Since(phaseStarted), err,
			zap.String("sandbox_id", sandboxID),
		)
		return nil, err
	}
	op.ObservePhase("upsert_runtime_record", time.Since(phaseStarted), nil,
		zap.String("sandbox_id", sandboxID),
	)
	cleanupPending = false
	return runtime, nil
}

func (m *SDKRuntimeManager) BootstrapSession(ctx context.Context, credential gatewaymanagedagents.RequestCredential, runtime *gatewaymanagedagents.RuntimeRecord, req *gatewaymanagedagents.WrapperSessionBootstrapRequest) (err error) {
	ctx, op := m.observability.StartOperation(ctx, "runtime_bootstrap_session", runtimeVendorForLog(runtime),
		zap.String("session_id", runtimeSessionID(runtime)),
		zap.String("sandbox_id", runtimeSandboxID(runtime)),
	)
	defer func() { op.End(err) }()
	bootstrapReq := *req
	phaseStarted := time.Now()
	if err := m.syncBootstrapState(ctx, credential, runtime, &bootstrapReq); err != nil {
		op.ObservePhase("sync_bootstrap_state", time.Since(phaseStarted), err)
		return err
	}
	op.ObservePhase("sync_bootstrap_state", time.Since(phaseStarted), nil,
		zap.Int("bootstrap_event_count", len(bootstrapReq.BootstrapEvents)),
		zap.Int("skill_count", len(bootstrapReq.SkillNames)),
	)
	bootstrapReq.SandboxID = runtime.SandboxID
	bootstrapReq.CallbackURL = m.runtimeWebhookURL("")
	bootstrapReq.ControlToken = runtime.ControlToken
	m.logger.Debug("bootstrapping managed-agent wrapper session",
		zap.String("session_id", bootstrapReq.SessionID),
		zap.Any("engine_extra_args", mapValue(bootstrapReq.Engine["extra_args"])),
		zap.Strings("engine_env_keys", sortedMapKeys(mapValue(bootstrapReq.Engine["env"]))),
	)
	phaseStarted = time.Now()
	err = m.wrapperJSON(ctx, credential, runtime, http.MethodPut, "/v1/runtime/session", &bootstrapReq)
	op.ObservePhase("wrapper_put_session", time.Since(phaseStarted), err)
	return err
}

func (m *SDKRuntimeManager) runtimeWebhookURL(requestBaseURL string) string {
	baseURL := strings.TrimSpace(m.cfg.RuntimeCallbackBaseURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(requestBaseURL)
	}
	if baseURL == "" {
		return ""
	}
	return gatewaymanagedagents.InternalSandboxWebhookURL(baseURL)
}

func (m *SDKRuntimeManager) StartRun(ctx context.Context, credential gatewaymanagedagents.RequestCredential, runtime *gatewaymanagedagents.RuntimeRecord, req *gatewaymanagedagents.WrapperRunRequest) (err error) {
	ctx, op := m.observability.StartOperation(ctx, "runtime_start_run", runtimeVendorForLog(runtime),
		zap.String("session_id", runtimeSessionID(runtime)),
		zap.String("sandbox_id", runtimeSandboxID(runtime)),
		zap.String("run_id", reqRunIDForLog(req)),
		zap.Int("input_event_count", reqInputEventCountForLog(req)),
	)
	defer func() { op.End(err) }()
	phaseStarted := time.Now()
	if err := m.RefreshRuntimeTTL(ctx, credential, runtime); err != nil {
		op.ObservePhase("refresh_runtime_ttl", time.Since(phaseStarted), err)
		m.logger.Warn("refresh runtime ttl before run failed", zap.Error(err), zap.String("session_id", runtimeSessionID(runtime)), zap.String("sandbox_id", runtimeSandboxID(runtime)))
	} else {
		op.ObservePhase("refresh_runtime_ttl", time.Since(phaseStarted), nil)
	}
	phaseStarted = time.Now()
	err = m.wrapperJSON(ctx, credential, runtime, http.MethodPost, "/v1/runs", req)
	op.ObservePhase("wrapper_start_run", time.Since(phaseStarted), err)
	return err
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
	client, err := m.sandboxClientForRuntime(ctx, credential, runtime)
	if err != nil {
		return err
	}
	m.cleanupManagedCredentialSources(ctx, client, runtime.SessionID)
	if strings.TrimSpace(runtime.SandboxID) != "" {
		if _, err := client.DeleteSandbox(ctx, runtime.SandboxID); err != nil {
			m.logger.Warn("delete sandbox failed", zap.Error(err), zap.String("sandbox_id", runtime.SandboxID))
		}
	}
	if runtime.WorkspaceVolumeID != "" {
		if _, err := client.DeleteVolumeWithOptions(ctx, runtime.WorkspaceVolumeID, &sandbox0sdk.DeleteVolumeOptions{Force: true}); err != nil {
			m.logger.Warn("delete workspace volume failed", zap.Error(err), zap.String("volume_id", runtime.WorkspaceVolumeID))
		}
	}
	return nil
}

func (m *SDKRuntimeManager) DeleteRuntimeSandbox(ctx context.Context, runtime *gatewaymanagedagents.RuntimeRecord) error {
	if runtime == nil || strings.TrimSpace(runtime.SandboxID) == "" {
		return nil
	}
	client, err := m.sandboxClientForRuntime(ctx, gatewaymanagedagents.RequestCredential{}, runtime)
	if err != nil {
		return err
	}
	m.cleanupManagedCredentialSources(ctx, client, runtime.SessionID)
	if _, err := client.DeleteSandbox(ctx, runtime.SandboxID); err != nil {
		m.logger.Warn("delete sandbox failed", zap.Error(err), zap.String("sandbox_id", runtime.SandboxID))
		return err
	}
	return nil
}

func (m *SDKRuntimeManager) RefreshRuntimeTTL(ctx context.Context, credential gatewaymanagedagents.RequestCredential, runtime *gatewaymanagedagents.RuntimeRecord) error {
	if runtime == nil || strings.TrimSpace(runtime.SandboxID) == "" {
		return nil
	}
	client, err := m.sandboxClientForRuntime(ctx, credential, runtime)
	if err != nil {
		return err
	}
	if err := m.configureRuntimeSandboxLifecycleWithClient(ctx, client, runtime); err != nil {
		return err
	}
	_, err = client.RefreshSandbox(ctx, runtime.SandboxID, &apispec.SandboxRefreshRequest{
		Duration: apispec.NewOptInt32(int32(m.sandboxTTLSeconds())),
	})
	if err != nil {
		return fmt.Errorf("refresh sandbox ttl: %w", err)
	}
	return nil
}

func (m *SDKRuntimeManager) configureRuntimeSandboxLifecycle(ctx context.Context, credential gatewaymanagedagents.RequestCredential, runtime *gatewaymanagedagents.RuntimeRecord) error {
	if runtime == nil || strings.TrimSpace(runtime.SandboxID) == "" {
		return nil
	}
	client, err := m.sandboxClientForRuntime(ctx, credential, runtime)
	if err != nil {
		return err
	}
	return m.configureRuntimeSandboxLifecycleWithClient(ctx, client, runtime)
}

func (m *SDKRuntimeManager) configureRuntimeSandboxLifecycleWithClient(ctx context.Context, client *sandbox0sdk.Client, runtime *gatewaymanagedagents.RuntimeRecord) error {
	_, err := client.UpdateSandbox(ctx, runtime.SandboxID, apispec.SandboxUpdateRequest{
		Config: apispec.NewOptSandboxUpdateConfig(apispec.SandboxUpdateConfig{
			TTL:        apispec.NewOptInt32(int32(m.sandboxTTLSeconds())),
			HardTTL:    apispec.NewOptInt32(0),
			AutoResume: apispec.NewOptBool(true),
		}),
	})
	if err != nil {
		return fmt.Errorf("update sandbox lifecycle: %w", err)
	}
	return nil
}

func (m *SDKRuntimeManager) sandboxClientForRuntime(ctx context.Context, credential gatewaymanagedagents.RequestCredential, runtime *gatewaymanagedagents.RuntimeRecord) (*sandbox0sdk.Client, error) {
	if runtime == nil || strings.TrimSpace(runtime.SessionID) == "" {
		return nil, errors.New("runtime session id is required")
	}
	teamID, err := m.teamIDForRuntime(ctx, runtime)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(credential.Token)
	if token == "" {
		token = strings.TrimSpace(m.cfg.SandboxAdminAPIKey)
	}
	if token == "" {
		return nil, errors.New("request credential is required")
	}
	return m.newSandboxClient(token, teamID)
}

func (m *SDKRuntimeManager) templateClient(ctx context.Context, credential gatewaymanagedagents.RequestCredential, fallbackTeamID string) (*sandbox0sdk.Client, error) {
	_ = ctx
	if adminAPIKey := strings.TrimSpace(m.cfg.SandboxAdminAPIKey); adminAPIKey != "" {
		return m.newSandboxClient(adminAPIKey, "")
	}
	if strings.TrimSpace(credential.Token) == "" {
		return nil, errors.New("request credential is required")
	}
	return m.newSandboxClient(credential.Token, fallbackTeamID)
}

func (m *SDKRuntimeManager) newSandboxClient(token, teamID string) (*sandbox0sdk.Client, error) {
	opts := []sandbox0sdk.Option{
		sandbox0sdk.WithBaseURL(strings.TrimRight(m.cfg.SandboxBaseURL, "/")),
		sandbox0sdk.WithToken(token),
		sandbox0sdk.WithTimeout(m.cfg.SandboxRequestTimeout),
	}
	if m.httpClient != nil {
		opts = append(opts, sandbox0sdk.WithHTTPClient(m.httpClient))
	}
	if trimmedTeamID := strings.TrimSpace(teamID); trimmedTeamID != "" {
		opts = append(opts, sandbox0sdk.WithRequestEditor(func(_ context.Context, req *http.Request) error {
			req.Header.Set("X-Team-ID", trimmedTeamID)
			return nil
		}))
	}
	return sandbox0sdk.NewClient(opts...)
}

func (m *SDKRuntimeManager) templateForVendor(vendor string) string {
	if m.templateRequest != nil && strings.TrimSpace(m.templateRequest.TemplateID) != "" {
		return m.templateRequest.TemplateID
	}
	return m.cfg.TemplateID
}

func (m *SDKRuntimeManager) templateIDForSession(vendor string, request *apispec.TemplateCreateRequest) string {
	if request != nil && strings.TrimSpace(request.TemplateID) != "" {
		return request.TemplateID
	}
	return m.templateForVendor(vendor)
}

func (m *SDKRuntimeManager) sandboxTTLSeconds() int {
	if m.cfg.SandboxTTLSeconds > 0 {
		return m.cfg.SandboxTTLSeconds
	}
	return DefaultSandboxTTLSeconds
}

func sortedMapKeys(in map[string]any) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for key := range in {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func runtimeSessionID(runtime *gatewaymanagedagents.RuntimeRecord) string {
	if runtime == nil {
		return ""
	}
	return runtime.SessionID
}

func runtimeSandboxID(runtime *gatewaymanagedagents.RuntimeRecord) string {
	if runtime == nil {
		return ""
	}
	return runtime.SandboxID
}

func runtimeVendorForLog(runtime *gatewaymanagedagents.RuntimeRecord) string {
	if runtime == nil {
		return ""
	}
	return runtime.Vendor
}

func sessionIDForLog(session *gatewaymanagedagents.SessionRecord) string {
	if session == nil {
		return ""
	}
	return session.ID
}

func sessionVendorForLog(session *gatewaymanagedagents.SessionRecord) string {
	if session == nil {
		return ""
	}
	return session.Vendor
}

func sessionTeamIDForLog(session *gatewaymanagedagents.SessionRecord) string {
	if session == nil {
		return ""
	}
	return session.TeamID
}

func reqRunIDForLog(req *gatewaymanagedagents.WrapperRunRequest) string {
	if req == nil {
		return ""
	}
	return req.RunID
}

func reqInputEventCountForLog(req *gatewaymanagedagents.WrapperRunRequest) int {
	if req == nil {
		return 0
	}
	return len(req.InputEvents)
}

func (m *SDKRuntimeManager) teamIDForRuntime(ctx context.Context, runtime *gatewaymanagedagents.RuntimeRecord) (string, error) {
	if runtime == nil || strings.TrimSpace(runtime.SessionID) == "" {
		return "", errors.New("runtime session id is required")
	}
	record, _, err := m.repo.GetSession(ctx, runtime.SessionID)
	if err != nil {
		return "", err
	}
	return record.TeamID, nil
}

func (m *SDKRuntimeManager) ensureWrapperEndpoint(ctx context.Context, token string, runtime *gatewaymanagedagents.RuntimeRecord) (*gatewaymanagedagents.RuntimeRecord, error) {
	if strings.TrimSpace(runtime.WrapperURL) != "" {
		return runtime, nil
	}
	if strings.TrimSpace(runtime.SandboxID) == "" {
		return nil, errors.New("runtime sandbox id is required")
	}
	teamID, err := m.teamIDForRuntime(ctx, runtime)
	if err != nil {
		return nil, err
	}
	client, err := m.newSandboxClient(token, teamID)
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
			return canonicalWrapperURL(port.PublicURL)
		}
	}
	return "", errors.New("wrapper public url is required")
}

func canonicalWrapperURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("wrapper public url is required")
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + trimmed
	}
	return canonicalManagedRuntimeURL(trimmed)
}

func (m *SDKRuntimeManager) wrapperRequestTarget(rawWrapperURL, path string) (string, string, error) {
	wrapperURL, err := canonicalWrapperURL(rawWrapperURL)
	if err != nil {
		return "", "", err
	}
	return strings.TrimRight(wrapperURL, "/") + path, "", nil
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
	requestURL, hostHeader, err := m.wrapperRequestTarget(runtime.WrapperURL, path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return fmt.Errorf("build wrapper request: %w", err)
	}
	if hostHeader != "" {
		req.Host = hostHeader
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
