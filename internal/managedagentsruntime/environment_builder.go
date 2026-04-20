package managedagentsruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
	sandbox0sdk "github.com/sandbox0-ai/sdk-go"
	apispec "github.com/sandbox0-ai/sdk-go/pkg/apispec"
	"go.uber.org/zap"
)

type environmentBuildResources struct {
	workspaceVolumeID string
	managerVolumeIDs  map[string]string
}

const environmentBuildCleanupTimeout = 2 * time.Minute

func (m *SDKRuntimeManager) resolveReadyEnvironmentArtifact(ctx context.Context, credential gatewaymanagedagents.RequestCredential, session *gatewaymanagedagents.SessionRecord, environment *gatewaymanagedagents.Environment, templateRequest *managedTemplateRequest, templateClient templateClient) (*gatewaymanagedagents.EnvironmentArtifact, error) {
	artifact, err := m.lookupPinnedEnvironmentArtifact(ctx, session, environment)
	if err != nil {
		return nil, err
	}
	return m.ensureEnvironmentArtifactReady(ctx, credential, artifact, environment, templateRequest, templateClient)
}

func (m *SDKRuntimeManager) PrebuildEnvironmentArtifact(ctx context.Context, credential gatewaymanagedagents.RequestCredential, teamID string, environment *gatewaymanagedagents.Environment) error {
	_ = credential
	if !m.cfg.Enabled || environment == nil {
		return nil
	}
	if m.repo == nil {
		return errors.New("managed-agent repository is required")
	}
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return errors.New("team id is required")
	}
	if _, err := m.runtimeSandboxToken(); err != nil {
		return nil
	}
	templateClient, err := m.templateClient(ctx, gatewaymanagedagents.RequestCredential{}, teamID)
	if err != nil {
		return err
	}
	templateRequest, err := m.templateRequestForEnvironment(environment)
	if err != nil {
		return fmt.Errorf("prepare environment template: %w", err)
	}
	if err := m.ensureManagedTemplate(ctx, templateClient, templateRequest); err != nil {
		return fmt.Errorf("ensure managed template: %w", err)
	}
	artifact, err := m.lookupPinnedEnvironmentArtifact(ctx, &gatewaymanagedagents.SessionRecord{
		TeamID:        teamID,
		EnvironmentID: environment.ID,
	}, environment)
	if err != nil {
		return fmt.Errorf("resolve environment artifact: %w", err)
	}
	_, err = m.ensureEnvironmentArtifactReady(ctx, gatewaymanagedagents.RequestCredential{}, artifact, environment, templateRequest, templateClient)
	return err
}

func (m *SDKRuntimeManager) CleanupEnvironmentArtifacts(ctx context.Context, credential gatewaymanagedagents.RequestCredential, teamID, environmentID string) error {
	_ = credential
	if !m.cfg.Enabled {
		return nil
	}
	if m.repo == nil {
		return errors.New("managed-agent repository is required")
	}
	teamID = strings.TrimSpace(teamID)
	environmentID = strings.TrimSpace(environmentID)
	if teamID == "" {
		return errors.New("team id is required")
	}
	if environmentID == "" {
		return errors.New("environment id is required")
	}
	if _, err := m.runtimeSandboxToken(); err != nil {
		return err
	}
	artifacts, err := m.repo.ListEnvironmentArtifacts(ctx, teamID, environmentID)
	if err != nil {
		return err
	}
	for _, artifact := range artifacts {
		if strings.TrimSpace(artifact.Status) == gatewaymanagedagents.EnvironmentArtifactStatusBuilding {
			return fmt.Errorf("environment artifact %s is still building", artifact.ID)
		}
	}
	client, err := m.runtimeSandboxClient()
	if err != nil {
		return err
	}
	var cleanupErrs []error
	for _, artifact := range artifacts {
		for _, volumeID := range artifact.Assets.VolumeIDs() {
			if _, err := client.DeleteVolumeWithOptions(ctx, volumeID, &sandbox0sdk.DeleteVolumeOptions{Force: true}); err != nil {
				if isSandboxNotFound(err) {
					continue
				}
				m.logger.Warn("delete environment artifact volume failed", zap.Error(err), zap.String("artifact_id", artifact.ID), zap.String("volume_id", volumeID))
				cleanupErrs = append(cleanupErrs, fmt.Errorf("delete environment artifact volume %s: %w", volumeID, err))
			}
		}
	}
	if err := errors.Join(cleanupErrs...); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, artifact := range artifacts {
		if strings.TrimSpace(artifact.Status) == gatewaymanagedagents.EnvironmentArtifactStatusArchived {
			continue
		}
		artifact.Status = gatewaymanagedagents.EnvironmentArtifactStatusArchived
		artifact.ArchivedAt = &now
		artifact.UpdatedAt = now
		if err := m.repo.UpdateEnvironmentArtifact(ctx, artifact); err != nil {
			return err
		}
	}
	return nil
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

func (m *SDKRuntimeManager) ensureEnvironmentArtifactReady(ctx context.Context, credential gatewaymanagedagents.RequestCredential, artifact *gatewaymanagedagents.EnvironmentArtifact, environment *gatewaymanagedagents.Environment, templateRequest *managedTemplateRequest, templateClient templateClient) (ready *gatewaymanagedagents.EnvironmentArtifact, err error) {
	ctx, op := m.observability.StartOperation(ctx, "environment_artifact_ready", "",
		zap.String("team_id", environmentArtifactTeamIDForLog(artifact)),
		zap.String("environment_id", environmentArtifactEnvironmentIDForLog(artifact)),
		zap.String("environment_artifact_id", environmentArtifactIDForLog(artifact)),
	)
	defer func() { op.End(err) }()
	if artifact == nil {
		return nil, gatewaymanagedagents.ErrEnvironmentArtifactNotFound
	}
	deadline := time.Now().UTC().Add(m.cfg.SandboxRequestTimeout)
	for {
		phaseStarted := time.Now()
		switch strings.TrimSpace(artifact.Status) {
		case gatewaymanagedagents.EnvironmentArtifactStatusReady:
			op.ObservePhase("artifact_already_ready", time.Since(phaseStarted), nil,
				zap.String("environment_artifact_id", artifact.ID),
			)
			return artifact, nil
		case gatewaymanagedagents.EnvironmentArtifactStatusArchived:
			return nil, fmt.Errorf("environment artifact %s is archived", artifact.ID)
		case gatewaymanagedagents.EnvironmentArtifactStatusPending, gatewaymanagedagents.EnvironmentArtifactStatusFailed:
			acquired, err := m.repo.BeginEnvironmentArtifactBuild(ctx, artifact.TeamID, artifact.ID, time.Now().UTC())
			if err != nil {
				op.ObservePhase("begin_artifact_build", time.Since(phaseStarted), err,
					zap.String("environment_artifact_status", artifact.Status),
				)
				return nil, err
			}
			op.ObservePhase("begin_artifact_build", time.Since(phaseStarted), nil,
				zap.Bool("acquired", acquired),
				zap.String("environment_artifact_status", artifact.Status),
			)
			if acquired {
				return m.buildEnvironmentArtifact(ctx, credential, artifact, environment, templateRequest, templateClient)
			}
		case gatewaymanagedagents.EnvironmentArtifactStatusBuilding:
			if time.Now().UTC().After(deadline) {
				return nil, fmt.Errorf("timed out waiting for environment artifact %s to build", artifact.ID)
			}
			op.ObservePhase("wait_artifact_building", time.Since(phaseStarted), nil,
				zap.String("environment_artifact_id", artifact.ID),
			)
			time.Sleep(250 * time.Millisecond)
		default:
			return nil, fmt.Errorf("unsupported environment artifact status %q", artifact.Status)
		}
		phaseStarted = time.Now()
		refreshed, err := m.repo.GetEnvironmentArtifact(ctx, artifact.TeamID, artifact.ID)
		if err != nil {
			op.ObservePhase("refresh_artifact_status", time.Since(phaseStarted), err,
				zap.String("environment_artifact_id", artifact.ID),
			)
			return nil, err
		}
		op.ObservePhase("refresh_artifact_status", time.Since(phaseStarted), nil,
			zap.String("environment_artifact_id", artifact.ID),
			zap.String("environment_artifact_status", refreshed.Status),
		)
		artifact = refreshed
	}
}

func (m *SDKRuntimeManager) buildEnvironmentArtifact(ctx context.Context, credential gatewaymanagedagents.RequestCredential, artifact *gatewaymanagedagents.EnvironmentArtifact, environment *gatewaymanagedagents.Environment, templateRequest *managedTemplateRequest, templateClient templateClient) (built *gatewaymanagedagents.EnvironmentArtifact, err error) {
	_ = credential
	ctx, op := m.observability.StartOperation(ctx, "environment_artifact_build", "",
		zap.String("team_id", environmentArtifactTeamIDForLog(artifact)),
		zap.String("environment_id", environmentArtifactEnvironmentIDForLog(artifact)),
		zap.String("environment_artifact_id", environmentArtifactIDForLog(artifact)),
	)
	defer func() { op.End(err) }()
	phaseStarted := time.Now()
	client, err := m.runtimeSandboxClient()
	if err != nil {
		op.ObservePhase("create_sandbox_client", time.Since(phaseStarted), err)
		return nil, err
	}
	op.ObservePhase("create_sandbox_client", time.Since(phaseStarted), nil)
	var (
		lastErr error
		logs    strings.Builder
	)
	for attempt := 1; attempt <= 2; attempt++ {
		if attempt > 1 {
			logs.WriteString("\n\nretrying environment artifact build\n")
		}
		phaseStarted = time.Now()
		assets, buildLog, buildErr := m.buildEnvironmentArtifactAttempt(ctx, client, environment, templateRequest, templateClient)
		logs.WriteString(buildLog)
		op.ObservePhase("build_attempt", time.Since(phaseStarted), buildErr,
			zap.Int("attempt", attempt),
		)
		if buildErr == nil {
			artifact.Status = gatewaymanagedagents.EnvironmentArtifactStatusReady
			artifact.Assets = assets
			artifact.BuildLog = logs.String()
			artifact.FailureReason = nil
			artifact.UpdatedAt = time.Now().UTC()
			phaseStarted = time.Now()
			if err := m.repo.UpdateEnvironmentArtifact(ctx, artifact); err != nil {
				op.ObservePhase("mark_artifact_ready", time.Since(phaseStarted), err)
				return nil, err
			}
			op.ObservePhase("mark_artifact_ready", time.Since(phaseStarted), nil,
				zap.Int("asset_volume_count", len(artifact.Assets.VolumeIDs())),
			)
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
	phaseStarted = time.Now()
	if err := m.repo.UpdateEnvironmentArtifact(ctx, artifact); err != nil {
		op.ObservePhase("mark_artifact_failed", time.Since(phaseStarted), err)
		return nil, err
	}
	op.ObservePhase("mark_artifact_failed", time.Since(phaseStarted), nil)
	return nil, lastErr
}

func environmentArtifactTeamIDForLog(artifact *gatewaymanagedagents.EnvironmentArtifact) string {
	if artifact == nil {
		return ""
	}
	return artifact.TeamID
}

func environmentArtifactEnvironmentIDForLog(artifact *gatewaymanagedagents.EnvironmentArtifact) string {
	if artifact == nil {
		return ""
	}
	return artifact.EnvironmentID
}

func environmentArtifactIDForLog(artifact *gatewaymanagedagents.EnvironmentArtifact) string {
	if artifact == nil {
		return ""
	}
	return artifact.ID
}

func (m *SDKRuntimeManager) buildEnvironmentArtifactAttempt(ctx context.Context, client *sandbox0sdk.Client, environment *gatewaymanagedagents.Environment, templateRequest *managedTemplateRequest, templateClient templateClient) (gatewaymanagedagents.EnvironmentArtifactAssets, string, error) {
	steps := environmentBuildSteps(environment)
	if len(steps) == 0 {
		return gatewaymanagedagents.EnvironmentArtifactAssets{}, "no environment packages requested; no package volumes created\n", nil
	}

	resources, err := m.createEnvironmentBuildResources(ctx, client, stepManagers(steps))
	if err != nil {
		return gatewaymanagedagents.EnvironmentArtifactAssets{}, "", err
	}

	if err := m.ensureManagedTemplate(ctx, templateClient, templateRequest); err != nil {
		m.cleanupEnvironmentBuildResources(ctx, client, nil, resources)
		return gatewaymanagedagents.EnvironmentArtifactAssets{}, "", fmt.Errorf("ensure managed template: %w", err)
	}

	claimOpts := []sandbox0sdk.SandboxOption{
		sandbox0sdk.WithSandboxBootstrapMount(resources.workspaceVolumeID, m.cfg.WorkspaceMountPath, nil),
		sandbox0sdk.WithSandboxBootstrapMountWait(m.cfg.SandboxRequestTimeout),
		sandbox0sdk.WithSandboxTTL(0),
		sandbox0sdk.WithSandboxHardTTL(0),
		sandbox0sdk.WithSandboxNetworkPolicy(builderNetworkPolicy(environment)),
	}
	for _, manager := range stepManagers(steps) {
		volumeID := resources.managerVolumeIDs[manager]
		mountPath := gatewaymanagedagents.ManagedEnvironmentMountPath(manager)
		if strings.TrimSpace(volumeID) == "" || strings.TrimSpace(mountPath) == "" {
			m.cleanupEnvironmentBuildResources(ctx, client, nil, resources)
			return gatewaymanagedagents.EnvironmentArtifactAssets{}, "", fmt.Errorf("environment builder is missing %s volume", manager)
		}
		claimOpts = append(claimOpts, sandbox0sdk.WithSandboxBootstrapMount(volumeID, mountPath, nil))
	}
	sandbox, err := client.ClaimSandbox(ctx, m.templateIDForSession("claude", templateRequest), claimOpts...)
	if err != nil {
		m.cleanupEnvironmentBuildResources(ctx, client, nil, resources)
		return gatewaymanagedagents.EnvironmentArtifactAssets{}, "", fmt.Errorf("claim environment builder sandbox: %w", err)
	}
	builderSandbox := client.Sandbox(sandbox.ID)
	defer m.cleanupEnvironmentBuildResources(ctx, client, builderSandbox, resources)

	var buildLog strings.Builder
	for _, step := range steps {
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

func (m *SDKRuntimeManager) createEnvironmentBuildResources(ctx context.Context, client *sandbox0sdk.Client, managers []string) (*environmentBuildResources, error) {
	workspace, err := client.CreateVolume(ctx, apispec.CreateSandboxVolumeRequest{})
	if err != nil {
		return nil, fmt.Errorf("create builder workspace volume: %w", err)
	}
	resources := &environmentBuildResources{
		workspaceVolumeID: workspace.ID,
		managerVolumeIDs:  make(map[string]string, len(managers)),
	}
	for _, manager := range managers {
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
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), environmentBuildCleanupTimeout)
	defer cancel()
	ctx = cleanupCtx

	if sandbox != nil {
		if _, err := client.DeleteSandbox(ctx, sandbox.ID); err != nil {
			if !isSandboxNotFound(err) {
				m.logger.Warn("delete environment builder sandbox failed", zap.Error(err), zap.String("sandbox_id", sandbox.ID))
			}
		}
	}
	if resources == nil {
		return
	}
	for _, volumeID := range uniqueVolumeIDs(append([]string{resources.workspaceVolumeID}, mapValues(resources.managerVolumeIDs)...)...) {
		if _, err := client.DeleteVolumeWithOptions(ctx, volumeID, &sandbox0sdk.DeleteVolumeOptions{Force: true}); err != nil {
			if !isSandboxNotFound(err) {
				m.logger.Warn("delete environment builder volume failed", zap.Error(err), zap.String("volume_id", volumeID))
			}
		}
	}
}

func publishEnvironmentArtifactVolumes(ctx context.Context, client *sandbox0sdk.Client, tempVolumes map[string]string) (gatewaymanagedagents.EnvironmentArtifactAssets, error) {
	assets := gatewaymanagedagents.EnvironmentArtifactAssets{}
	published := make([]string, 0, len(tempVolumes))
	for _, manager := range gatewaymanagedagents.ManagedEnvironmentPackageManagers() {
		tempVolumeID := strings.TrimSpace(tempVolumes[manager])
		if tempVolumeID == "" {
			continue
		}
		volume, err := client.ForkVolume(ctx, tempVolumeID, &apispec.ForkVolumeRequest{
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
				if isSandboxNotFound(err) {
					continue
				}
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
	steps := make([]environmentBuildStep, 0, len(gatewaymanagedagents.ConfiguredEnvironmentPackageManagers(environment.Config)))
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

func stepManagers(steps []environmentBuildStep) []string {
	out := make([]string, 0, len(steps))
	for _, step := range steps {
		if strings.TrimSpace(step.manager) != "" {
			out = append(out, step.manager)
		}
	}
	return out
}

type environmentArtifactMount struct {
	volumeID  string
	mountPath string
}

func environmentArtifactMounts(environment *gatewaymanagedagents.Environment, artifact *gatewaymanagedagents.EnvironmentArtifact) ([]environmentArtifactMount, error) {
	if environment == nil || artifact == nil {
		return nil, nil
	}
	managers := gatewaymanagedagents.ConfiguredEnvironmentPackageManagers(environment.Config)
	mounts := make([]environmentArtifactMount, 0, len(managers))
	for _, manager := range managers {
		volumeID := artifact.Assets.VolumeIDForManager(manager)
		mountPath := gatewaymanagedagents.ManagedEnvironmentMountPath(manager)
		if strings.TrimSpace(volumeID) == "" || strings.TrimSpace(mountPath) == "" {
			return nil, fmt.Errorf("environment artifact %s is missing %s volume", artifact.ID, manager)
		}
		mounts = append(mounts, environmentArtifactMount{volumeID: volumeID, mountPath: mountPath})
	}
	return mounts, nil
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
