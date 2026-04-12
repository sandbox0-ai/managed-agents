package managedagentsruntime

import (
	"context"
	"fmt"
	"strings"
	"time"

	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
	sandbox0sdk "github.com/sandbox0-ai/sdk-go"
	apispec "github.com/sandbox0-ai/sdk-go/pkg/apispec"
	"go.uber.org/zap"
)

type environmentBuildResources struct {
	workspaceVolumeID   string
	engineStateVolumeID string
	managerVolumeIDs    map[string]string
}

func (m *SDKRuntimeManager) resolveReadyEnvironmentArtifact(ctx context.Context, credential gatewaymanagedagents.RequestCredential, session *gatewaymanagedagents.SessionRecord, environment *gatewaymanagedagents.Environment, templateRequest *apispec.TemplateCreateRequest) (*gatewaymanagedagents.EnvironmentArtifact, error) {
	artifact, err := m.lookupPinnedEnvironmentArtifact(ctx, session, environment)
	if err != nil {
		return nil, err
	}
	return m.ensureEnvironmentArtifactReady(ctx, credential, artifact, environment, templateRequest)
}

func (m *SDKRuntimeManager) lookupPinnedEnvironmentArtifact(ctx context.Context, session *gatewaymanagedagents.SessionRecord, environment *gatewaymanagedagents.Environment) (*gatewaymanagedagents.EnvironmentArtifact, error) {
	if session == nil {
		return nil, gatewaymanagedagents.ErrSessionNotFound
	}
	if artifactID := strings.TrimSpace(session.EnvironmentArtifactID); artifactID != "" {
		return m.repo.GetEnvironmentArtifact(ctx, session.TeamID, artifactID)
	}
	if environment == nil {
		return nil, gatewaymanagedagents.ErrEnvironmentArtifactNotFound
	}
	compatibility := gatewaymanagedagents.DefaultEnvironmentArtifactCompatibility()
	digest, err := gatewaymanagedagents.EnvironmentArtifactDigest(environment.Config, compatibility)
	if err != nil {
		return nil, err
	}
	artifact, err := m.repo.GetEnvironmentArtifactByDigest(ctx, session.TeamID, session.EnvironmentID, digest)
	if err == nil {
		return artifact, nil
	}
	if err != gatewaymanagedagents.ErrEnvironmentArtifactNotFound {
		return nil, err
	}
	now := time.Now().UTC()
	artifact = &gatewaymanagedagents.EnvironmentArtifact{
		ID:             gatewaymanagedagents.NewID("envart"),
		TeamID:         session.TeamID,
		EnvironmentID:  session.EnvironmentID,
		Digest:         digest,
		Status:         gatewaymanagedagents.EnvironmentArtifactStatusPending,
		ConfigSnapshot: gatewaymanagedagents.EnvironmentConfigSnapshotForArtifact(environment.Config),
		Compatibility:  compatibility,
		Assets:         gatewaymanagedagents.EnvironmentArtifactAssets{},
		BuildLog:       "",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := m.repo.CreateEnvironmentArtifact(ctx, artifact); err != nil {
		return m.repo.GetEnvironmentArtifactByDigest(ctx, session.TeamID, session.EnvironmentID, digest)
	}
	return artifact, nil
}

func (m *SDKRuntimeManager) ensureEnvironmentArtifactReady(ctx context.Context, credential gatewaymanagedagents.RequestCredential, artifact *gatewaymanagedagents.EnvironmentArtifact, environment *gatewaymanagedagents.Environment, templateRequest *apispec.TemplateCreateRequest) (*gatewaymanagedagents.EnvironmentArtifact, error) {
	if artifact == nil {
		return nil, gatewaymanagedagents.ErrEnvironmentArtifactNotFound
	}
	deadline := time.Now().UTC().Add(m.cfg.SandboxRequestTimeout)
	for {
		switch strings.TrimSpace(artifact.Status) {
		case gatewaymanagedagents.EnvironmentArtifactStatusReady:
			return artifact, nil
		case gatewaymanagedagents.EnvironmentArtifactStatusArchived:
			return nil, fmt.Errorf("environment artifact %s is archived", artifact.ID)
		case gatewaymanagedagents.EnvironmentArtifactStatusPending, gatewaymanagedagents.EnvironmentArtifactStatusFailed:
			acquired, err := m.repo.BeginEnvironmentArtifactBuild(ctx, artifact.TeamID, artifact.ID, time.Now().UTC())
			if err != nil {
				return nil, err
			}
			if acquired {
				return m.buildEnvironmentArtifact(ctx, credential, artifact, environment, templateRequest)
			}
		case gatewaymanagedagents.EnvironmentArtifactStatusBuilding:
			if time.Now().UTC().After(deadline) {
				return nil, fmt.Errorf("timed out waiting for environment artifact %s to build", artifact.ID)
			}
			time.Sleep(250 * time.Millisecond)
		default:
			return nil, fmt.Errorf("unsupported environment artifact status %q", artifact.Status)
		}
		refreshed, err := m.repo.GetEnvironmentArtifact(ctx, artifact.TeamID, artifact.ID)
		if err != nil {
			return nil, err
		}
		artifact = refreshed
	}
}

func (m *SDKRuntimeManager) buildEnvironmentArtifact(ctx context.Context, credential gatewaymanagedagents.RequestCredential, artifact *gatewaymanagedagents.EnvironmentArtifact, environment *gatewaymanagedagents.Environment, templateRequest *apispec.TemplateCreateRequest) (*gatewaymanagedagents.EnvironmentArtifact, error) {
	client, err := m.newSandboxClient(credential.Token, artifact.TeamID)
	if err != nil {
		return nil, err
	}
	var (
		lastErr error
		logs    strings.Builder
	)
	for attempt := 1; attempt <= 2; attempt++ {
		if attempt > 1 {
			logs.WriteString("\n\nretrying environment artifact build\n")
		}
		assets, buildLog, buildErr := m.buildEnvironmentArtifactAttempt(ctx, client, environment, templateRequest)
		logs.WriteString(buildLog)
		if buildErr == nil {
			artifact.Status = gatewaymanagedagents.EnvironmentArtifactStatusReady
			artifact.Assets = assets
			artifact.BuildLog = logs.String()
			artifact.FailureReason = nil
			artifact.UpdatedAt = time.Now().UTC()
			if err := m.repo.UpdateEnvironmentArtifact(ctx, artifact); err != nil {
				return nil, err
			}
			m.collectGarbageEnvironmentArtifacts(ctx, client, artifact)
			return artifact, nil
		}
		lastErr = buildErr
	}
	reason := lastErr.Error()
	artifact.Status = gatewaymanagedagents.EnvironmentArtifactStatusFailed
	artifact.BuildLog = logs.String()
	artifact.FailureReason = &reason
	artifact.UpdatedAt = time.Now().UTC()
	if err := m.repo.UpdateEnvironmentArtifact(ctx, artifact); err != nil {
		return nil, err
	}
	return nil, lastErr
}

func (m *SDKRuntimeManager) buildEnvironmentArtifactAttempt(ctx context.Context, client *sandbox0sdk.Client, environment *gatewaymanagedagents.Environment, templateRequest *apispec.TemplateCreateRequest) (gatewaymanagedagents.EnvironmentArtifactAssets, string, error) {
	resources, err := m.createEnvironmentBuildResources(ctx, client)
	if err != nil {
		return gatewaymanagedagents.EnvironmentArtifactAssets{}, "", err
	}

	if err := m.ensureManagedTemplate(ctx, client, templateRequest); err != nil {
		m.cleanupEnvironmentBuildResources(ctx, client, nil, resources)
		return gatewaymanagedagents.EnvironmentArtifactAssets{}, "", fmt.Errorf("ensure managed template: %w", err)
	}

	claimOpts := []sandbox0sdk.SandboxOption{
		sandbox0sdk.WithSandboxBootstrapMount(resources.workspaceVolumeID, m.cfg.WorkspaceMountPath, nil),
		sandbox0sdk.WithSandboxBootstrapMount(resources.engineStateVolumeID, m.cfg.EngineStateMountPath, nil),
		sandbox0sdk.WithSandboxBootstrapMountWait(m.cfg.SandboxRequestTimeout),
		sandbox0sdk.WithSandboxTTL(int32(m.cfg.SandboxTTLSeconds)),
		sandbox0sdk.WithSandboxHardTTL(int32(m.cfg.SandboxHardTTLSeconds)),
		sandbox0sdk.WithSandboxNetworkPolicy(builderNetworkPolicy(environment)),
	}
	for _, manager := range gatewaymanagedagents.ManagedEnvironmentPackageManagers() {
		claimOpts = append(claimOpts, sandbox0sdk.WithSandboxBootstrapMount(resources.managerVolumeIDs[manager], gatewaymanagedagents.ManagedEnvironmentMountPath(manager), nil))
	}
	sandbox, err := client.ClaimSandbox(ctx, m.templateIDForSession("claude", templateRequest), claimOpts...)
	if err != nil {
		m.cleanupEnvironmentBuildResources(ctx, client, nil, resources)
		return gatewaymanagedagents.EnvironmentArtifactAssets{}, "", fmt.Errorf("claim environment builder sandbox: %w", err)
	}
	builderSandbox := client.Sandbox(sandbox.ID)
	defer m.cleanupEnvironmentBuildResources(ctx, client, builderSandbox, resources)

	var buildLog strings.Builder
	for _, step := range environmentBuildSteps(environment) {
		output, err := runEnvironmentBuildStep(ctx, builderSandbox, step)
		buildLog.WriteString("== " + step.manager + " ==\n")
		buildLog.WriteString(output)
		if !strings.HasSuffix(output, "\n") {
			buildLog.WriteString("\n")
		}
		if err != nil {
			return gatewaymanagedagents.EnvironmentArtifactAssets{}, buildLog.String(), fmt.Errorf("%s build failed: %w", step.manager, err)
		}
	}

	assets, err := publishEnvironmentArtifactVolumes(ctx, client, resources.managerVolumeIDs)
	if err != nil {
		return gatewaymanagedagents.EnvironmentArtifactAssets{}, buildLog.String(), err
	}
	return assets, buildLog.String(), nil
}

func (m *SDKRuntimeManager) createEnvironmentBuildResources(ctx context.Context, client *sandbox0sdk.Client) (*environmentBuildResources, error) {
	workspace, err := client.CreateVolume(ctx, apispec.CreateSandboxVolumeRequest{})
	if err != nil {
		return nil, fmt.Errorf("create builder workspace volume: %w", err)
	}
	engineState, err := client.CreateVolume(ctx, apispec.CreateSandboxVolumeRequest{})
	if err != nil {
		_, _ = client.DeleteVolumeWithOptions(ctx, workspace.ID, &sandbox0sdk.DeleteVolumeOptions{Force: true})
		return nil, fmt.Errorf("create builder engine-state volume: %w", err)
	}
	resources := &environmentBuildResources{
		workspaceVolumeID:   workspace.ID,
		engineStateVolumeID: engineState.ID,
		managerVolumeIDs:    make(map[string]string, len(gatewaymanagedagents.ManagedEnvironmentPackageManagers())),
	}
	for _, manager := range gatewaymanagedagents.ManagedEnvironmentPackageManagers() {
		volume, err := client.CreateVolume(ctx, apispec.CreateSandboxVolumeRequest{})
		if err != nil {
			m.cleanupEnvironmentBuildResources(ctx, client, nil, resources)
			return nil, fmt.Errorf("create builder %s volume: %w", manager, err)
		}
		resources.managerVolumeIDs[manager] = volume.ID
	}
	return resources, nil
}

func (m *SDKRuntimeManager) cleanupEnvironmentBuildResources(ctx context.Context, client *sandbox0sdk.Client, sandbox *sandbox0sdk.Sandbox, resources *environmentBuildResources) {
	if sandbox != nil {
		if _, err := client.DeleteSandbox(ctx, sandbox.ID); err != nil {
			m.logger.Warn("delete environment builder sandbox failed", zap.Error(err), zap.String("sandbox_id", sandbox.ID))
		}
	}
	if resources == nil {
		return
	}
	for _, volumeID := range append([]string{resources.workspaceVolumeID, resources.engineStateVolumeID}, mapValues(resources.managerVolumeIDs)...) {
		if strings.TrimSpace(volumeID) == "" {
			continue
		}
		if _, err := client.DeleteVolumeWithOptions(ctx, volumeID, &sandbox0sdk.DeleteVolumeOptions{Force: true}); err != nil {
			m.logger.Warn("delete environment builder volume failed", zap.Error(err), zap.String("volume_id", volumeID))
		}
	}
}

func publishEnvironmentArtifactVolumes(ctx context.Context, client *sandbox0sdk.Client, tempVolumes map[string]string) (gatewaymanagedagents.EnvironmentArtifactAssets, error) {
	assets := gatewaymanagedagents.EnvironmentArtifactAssets{}
	published := make([]string, 0, len(tempVolumes))
	for _, manager := range gatewaymanagedagents.ManagedEnvironmentPackageManagers() {
		volume, err := client.ForkVolume(ctx, tempVolumes[manager], &apispec.ForkVolumeRequest{
			AccessMode: apispec.NewOptVolumeAccessMode(apispec.VolumeAccessModeROX),
		})
		if err != nil {
			for _, volumeID := range published {
				_, _ = client.DeleteVolumeWithOptions(ctx, volumeID, &sandbox0sdk.DeleteVolumeOptions{Force: true})
			}
			return gatewaymanagedagents.EnvironmentArtifactAssets{}, fmt.Errorf("publish %s environment volume: %w", manager, err)
		}
		assets.SetVolumeIDForManager(manager, volume.ID)
		published = append(published, volume.ID)
	}
	return assets, nil
}

func (m *SDKRuntimeManager) collectGarbageEnvironmentArtifacts(ctx context.Context, client *sandbox0sdk.Client, current *gatewaymanagedagents.EnvironmentArtifact) {
	if current == nil {
		return
	}
	items, err := m.repo.ListGCableEnvironmentArtifacts(ctx, current.TeamID, current.EnvironmentID, current.ID)
	if err != nil {
		m.logger.Warn("list garbage-collectable environment artifacts failed", zap.Error(err), zap.String("environment_id", current.EnvironmentID))
		return
	}
	for _, artifact := range items {
		deleteFailed := false
		for _, volumeID := range artifact.Assets.VolumeIDs() {
			if _, err := client.DeleteVolumeWithOptions(ctx, volumeID, &sandbox0sdk.DeleteVolumeOptions{Force: true}); err != nil {
				deleteFailed = true
				m.logger.Warn("delete environment artifact volume failed", zap.Error(err), zap.String("artifact_id", artifact.ID), zap.String("volume_id", volumeID))
			}
		}
		if deleteFailed {
			continue
		}
		now := time.Now().UTC()
		artifact.Status = gatewaymanagedagents.EnvironmentArtifactStatusArchived
		artifact.ArchivedAt = &now
		artifact.UpdatedAt = now
		if err := m.repo.UpdateEnvironmentArtifact(ctx, artifact); err != nil {
			m.logger.Warn("archive garbage-collected environment artifact failed", zap.Error(err), zap.String("artifact_id", artifact.ID))
		}
	}
}

type environmentBuildStep struct {
	manager string
	script  string
	envVars map[string]string
}

func environmentBuildSteps(environment *gatewaymanagedagents.Environment) []environmentBuildStep {
	if environment == nil {
		return nil
	}
	packages := environment.Config.Packages
	steps := make([]environmentBuildStep, 0, len(gatewaymanagedagents.ManagedEnvironmentPackageManagers()))
	if len(packages.Apt) > 0 {
		steps = append(steps, environmentBuildStep{manager: "apt", script: buildAptScript(packages.Apt)})
	}
	if len(packages.Cargo) > 0 {
		steps = append(steps, environmentBuildStep{manager: "cargo", script: buildCargoScript(packages.Cargo)})
	}
	if len(packages.Gem) > 0 {
		steps = append(steps, environmentBuildStep{manager: "gem", script: buildGemScript(packages.Gem)})
	}
	if len(packages.Go) > 0 {
		steps = append(steps, environmentBuildStep{manager: "go", script: buildGoScript(packages.Go)})
	}
	if len(packages.NPM) > 0 {
		steps = append(steps, environmentBuildStep{manager: "npm", script: buildNPMScript(packages.NPM)})
	}
	if len(packages.Pip) > 0 {
		steps = append(steps, environmentBuildStep{manager: "pip", script: buildPipScript(packages.Pip)})
	}
	return steps
}

func runEnvironmentBuildStep(ctx context.Context, sandbox *sandbox0sdk.Sandbox, step environmentBuildStep) (string, error) {
	result, err := sandbox.Cmd(
		ctx,
		"environment-build",
		sandbox0sdk.WithCommand([]string{"sh", "-lc", step.script}),
		sandbox0sdk.WithCmdCWD("/"),
		sandbox0sdk.WithCmdEnvVars(step.envVars),
	)
	if err != nil {
		return result.OutputRaw, err
	}
	return result.OutputRaw, nil
}

func buildAptScript(packages []string) string {
	return strings.Join([]string{
		"set -eu",
		`ROOT="` + gatewaymanagedagents.ManagedEnvironmentAptMountPath + `/rootfs"`,
		`STATE_DIR="/tmp/managed-agent-apt/state"`,
		`CACHE_DIR="/tmp/managed-agent-apt/cache"`,
		`mkdir -p "$ROOT" "$STATE_DIR/lists/partial" "$STATE_DIR/lists/auxfiles" "$CACHE_DIR/archives/partial"`,
		`apt-get -o Dir::State="$STATE_DIR" -o Dir::State::status=/var/lib/dpkg/status -o Dir::Cache="$CACHE_DIR" update`,
		`DEBIAN_FRONTEND=noninteractive apt-get -y --download-only -o Dir::State="$STATE_DIR" -o Dir::State::status=/var/lib/dpkg/status -o Dir::Cache="$CACHE_DIR" install ` + shellWords(packages),
		`set -- "$CACHE_DIR"/archives/*.deb`,
		`if [ -e "$1" ]; then for deb in "$@"; do dpkg-deb -x "$deb" "$ROOT"; done; fi`,
	}, "\n")
}

func buildCargoScript(packages []string) string {
	lines := []string{
		"set -eu",
		`ROOT="` + gatewaymanagedagents.ManagedEnvironmentCargoMountPath + `/root"`,
		`CARGO_HOME_DIR="` + gatewaymanagedagents.ManagedEnvironmentCargoMountPath + `/home"`,
		`mkdir -p "$ROOT" "$CARGO_HOME_DIR"`,
		`export CARGO_HOME="$CARGO_HOME_DIR"`,
	}
	for _, pkg := range packages {
		lines = append(lines, `cargo install --root "$ROOT" `+shellQuote(pkg))
	}
	return strings.Join(lines, "\n")
}

func buildGemScript(packages []string) string {
	return strings.Join([]string{
		"set -eu",
		`GEM_HOME_DIR="` + gatewaymanagedagents.ManagedEnvironmentGemMountPath + `/home"`,
		`GEM_BIN_DIR="` + gatewaymanagedagents.ManagedEnvironmentGemMountPath + `/bin"`,
		`mkdir -p "$GEM_HOME_DIR" "$GEM_BIN_DIR"`,
		`gem install --no-document --install-dir "$GEM_HOME_DIR" --bindir "$GEM_BIN_DIR" ` + shellWords(packages),
	}, "\n")
}

func buildGoScript(packages []string) string {
	lines := []string{
		"set -eu",
		`GOBIN_DIR="` + gatewaymanagedagents.ManagedEnvironmentGoMountPath + `/bin"`,
		`GOMODCACHE_DIR="` + gatewaymanagedagents.ManagedEnvironmentGoMountPath + `/pkg/mod"`,
		`mkdir -p "$GOBIN_DIR" "$GOMODCACHE_DIR"`,
		`export GOBIN="$GOBIN_DIR"`,
		`export GOMODCACHE="$GOMODCACHE_DIR"`,
		`export GOCACHE="/tmp/managed-agent-go-cache"`,
	}
	for _, pkg := range packages {
		lines = append(lines, `go install `+shellQuote(normalizeGoInstallTarget(pkg)))
	}
	return strings.Join(lines, "\n")
}

func buildNPMScript(packages []string) string {
	return strings.Join([]string{
		"set -eu",
		`PREFIX_DIR="` + gatewaymanagedagents.ManagedEnvironmentNPMMountPath + `"`,
		`mkdir -p "$PREFIX_DIR"`,
		`npm install --global --prefix "$PREFIX_DIR" ` + shellWords(packages),
	}, "\n")
}

func buildPipScript(packages []string) string {
	return strings.Join([]string{
		"set -eu",
		`VENV_DIR="` + gatewaymanagedagents.ManagedEnvironmentPipMountPath + `/venv"`,
		`python3 -m venv "$VENV_DIR"`,
		`"$VENV_DIR/bin/python" -m pip install --upgrade pip`,
		`"$VENV_DIR/bin/python" -m pip install ` + shellWords(packages),
	}, "\n")
}

func normalizeGoInstallTarget(value string) string {
	trimmed := strings.TrimSpace(value)
	if strings.Contains(trimmed, "@") {
		return trimmed
	}
	return trimmed + "@latest"
}

func shellWords(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, shellQuote(value))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func mapValues(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}
