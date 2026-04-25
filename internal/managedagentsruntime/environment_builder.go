package managedagentsruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
	sandbox0sdk "github.com/sandbox0-ai/sdk-go"
	apispec "github.com/sandbox0-ai/sdk-go/pkg/apispec"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

type environmentBuildResources struct {
	workspaceVolumeID string
	managerVolumeIDs  map[string]string
}

const environmentBuildCleanupTimeout = 2 * time.Minute
const environmentArtifactCleanupPollInterval = 100 * time.Millisecond
const environmentArtifactCleanupGracePeriod = 2 * time.Second
const environmentBuildStepConcurrency = 3
const environmentArtifactPublishRetryTimeout = 2 * time.Minute
const environmentArtifactPublishRetryInterval = 2 * time.Second
const managedEnvironmentBasePATH = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

func (m *SDKRuntimeManager) BuildEnvironmentArtifact(ctx context.Context, credential gatewaymanagedagents.RequestCredential, teamID string, environment *gatewaymanagedagents.Environment) (*gatewaymanagedagents.EnvironmentArtifactBuildResult, error) {
	_ = credential
	if environment == nil {
		return nil, gatewaymanagedagents.ErrEnvironmentNotFound
	}
	if !m.cfg.Enabled {
		return nil, errors.New("managed-agent runtime is disabled")
	}
	if m.repo == nil {
		return nil, errors.New("managed-agent repository is required")
	}
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, errors.New("team id is required")
	}
	if _, err := m.runtimeSandboxToken(); err != nil {
		return nil, err
	}
	templateClient, err := m.templateClient(ctx, gatewaymanagedagents.RequestCredential{}, teamID)
	if err != nil {
		return nil, err
	}
	templateRequest, err := m.templateRequestForEnvironment(environment)
	if err != nil {
		return nil, fmt.Errorf("prepare environment template: %w", err)
	}
	if err := m.ensureManagedTemplate(ctx, templateClient, templateRequest); err != nil {
		return nil, fmt.Errorf("ensure managed template: %w", err)
	}
	client, err := m.runtimeSandboxClient()
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
		assets, buildLog, buildErr := m.buildEnvironmentArtifactAttempt(ctx, client, environment, templateRequest, templateClient)
		logs.WriteString(buildLog)
		if buildErr == nil {
			return &gatewaymanagedagents.EnvironmentArtifactBuildResult{Assets: assets, BuildLog: logs.String()}, nil
		}
		lastErr = buildErr
	}
	if lastErr == nil {
		lastErr = errors.New("environment artifact build failed")
	}
	return nil, lastErr
}

func (m *SDKRuntimeManager) CleanupEnvironmentArtifactAssets(ctx context.Context, credential gatewaymanagedagents.RequestCredential, teamID string, assets gatewaymanagedagents.EnvironmentArtifactAssets) error {
	_ = credential
	_ = teamID
	if !m.cfg.Enabled || len(assets.VolumeIDs()) == 0 {
		return nil
	}
	if _, err := m.runtimeSandboxToken(); err != nil {
		return err
	}
	client, err := m.runtimeSandboxClient()
	if err != nil {
		return err
	}
	return m.deleteEnvironmentArtifactAssets(ctx, client, "", assets)
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
	artifacts, err := m.waitForEnvironmentArtifactsToSettle(ctx, teamID, environmentID)
	if err != nil {
		return err
	}
	client, err := m.runtimeSandboxClient()
	if err != nil {
		return err
	}
	var cleanupErrs []error
	for _, artifact := range artifacts {
		if err := m.deleteEnvironmentArtifactAssets(ctx, client, artifact.ID, artifact.Assets); err != nil {
			cleanupErrs = append(cleanupErrs, err)
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

func (m *SDKRuntimeManager) waitForEnvironmentArtifactsToSettle(ctx context.Context, teamID, environmentID string) ([]*gatewaymanagedagents.EnvironmentArtifact, error) {
	waitBudget := environmentArtifactCleanupGracePeriod
	if m.cfg.SandboxRequestTimeout > 0 && m.cfg.SandboxRequestTimeout < waitBudget {
		waitBudget = m.cfg.SandboxRequestTimeout
	}
	deadline := time.Now().UTC().Add(waitBudget)
	for {
		artifacts, err := m.repo.ListEnvironmentArtifacts(ctx, teamID, environmentID)
		if err != nil {
			return nil, err
		}
		buildingID := ""
		for _, artifact := range artifacts {
			if strings.TrimSpace(artifact.Status) == gatewaymanagedagents.EnvironmentArtifactStatusBuilding {
				buildingID = artifact.ID
				break
			}
		}
		if buildingID == "" {
			return artifacts, nil
		}
		if time.Now().UTC().After(deadline) {
			return nil, fmt.Errorf("%w: %s", gatewaymanagedagents.ErrEnvironmentArtifactBuilding, buildingID)
		}
		timer := time.NewTimer(environmentArtifactCleanupPollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
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
		sandbox0sdk.WithSandboxBootstrapMount(resources.workspaceVolumeID, m.cfg.WorkspaceMountPath),
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
		claimOpts = append(claimOpts, sandbox0sdk.WithSandboxBootstrapMount(volumeID, mountPath))
	}
	sandbox, err := client.ClaimSandbox(ctx, m.templateIDForSession("claude", templateRequest), claimOpts...)
	if err != nil {
		m.cleanupEnvironmentBuildResources(ctx, client, nil, resources)
		return gatewaymanagedagents.EnvironmentArtifactAssets{}, "", fmt.Errorf("claim environment builder sandbox: %w", err)
	}
	builderSandbox := client.Sandbox(sandbox.ID)
	defer m.cleanupEnvironmentBuildResources(ctx, client, builderSandbox, resources)

	type stepResult struct {
		output string
	}
	results := make([]stepResult, len(steps))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(environmentBuildStepConcurrency)
	for index, step := range steps {
		index, step := index, step
		group.Go(func() error {
			output, err := runEnvironmentBuildStep(groupCtx, builderSandbox, step)
			results[index] = stepResult{output: output}
			if err != nil {
				return fmt.Errorf("%s build failed: %w", step.manager, err)
			}
			return nil
		})
	}
	buildErr := group.Wait()
	var buildLog strings.Builder
	for index, step := range steps {
		output := results[index].output
		buildLog.WriteString("== " + step.manager + " ==\n")
		buildLog.WriteString(output)
		if !strings.HasSuffix(output, "\n") {
			buildLog.WriteString("\n")
		}
	}
	if buildErr != nil {
		return gatewaymanagedagents.EnvironmentArtifactAssets{}, buildLog.String(), buildErr
	}

	if _, err := client.DeleteSandbox(ctx, sandbox.ID); err != nil {
		return gatewaymanagedagents.EnvironmentArtifactAssets{}, buildLog.String(), fmt.Errorf("delete environment builder sandbox before publishing volumes: %w", err)
	}
	builderSandbox = nil

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
	return publishEnvironmentArtifactVolumesWithRetry(ctx, client, tempVolumes, environmentArtifactPublishRetryTimeout, environmentArtifactPublishRetryInterval)
}

func publishEnvironmentArtifactVolumesWithRetry(ctx context.Context, client *sandbox0sdk.Client, tempVolumes map[string]string, retryTimeout, retryInterval time.Duration) (gatewaymanagedagents.EnvironmentArtifactAssets, error) {
	assets := gatewaymanagedagents.EnvironmentArtifactAssets{}
	type publishResult struct {
		manager  string
		volumeID string
	}
	results := make(chan publishResult, len(tempVolumes))
	group, groupCtx := errgroup.WithContext(ctx)
	for _, manager := range gatewaymanagedagents.ManagedEnvironmentPackageManagers() {
		tempVolumeID := strings.TrimSpace(tempVolumes[manager])
		if tempVolumeID == "" {
			continue
		}
		manager, tempVolumeID := manager, tempVolumeID
		group.Go(func() error {
			volume, err := forkEnvironmentArtifactVolumeWithRetry(groupCtx, client, tempVolumeID, retryTimeout, retryInterval)
			if err != nil {
				return fmt.Errorf("publish %s environment volume: %w", manager, err)
			}
			results <- publishResult{manager: manager, volumeID: volume.ID}
			return nil
		})
	}
	err := group.Wait()
	close(results)
	published := make([]string, 0, len(tempVolumes))
	for result := range results {
		assets.SetVolumeIDForManager(result.manager, result.volumeID)
		published = append(published, result.volumeID)
	}
	if err != nil {
		for _, volumeID := range published {
			_, _ = client.DeleteVolumeWithOptions(ctx, volumeID, &sandbox0sdk.DeleteVolumeOptions{Force: true})
		}
		return gatewaymanagedagents.EnvironmentArtifactAssets{}, err
	}
	return assets, nil
}

func forkEnvironmentArtifactVolumeWithRetry(ctx context.Context, client *sandbox0sdk.Client, tempVolumeID string, retryTimeout, retryInterval time.Duration) (*apispec.SandboxVolume, error) {
	if retryTimeout <= 0 {
		retryTimeout = environmentArtifactPublishRetryTimeout
	}
	if retryInterval <= 0 {
		retryInterval = environmentArtifactPublishRetryInterval
	}
	waitCtx, cancel := context.WithTimeout(ctx, retryTimeout)
	defer cancel()
	var lastErr error
	for {
		volume, err := client.ForkVolume(waitCtx, tempVolumeID, &apispec.ForkVolumeRequest{
			AccessMode: apispec.NewOptVolumeAccessMode(apispec.VolumeAccessModeROX),
		})
		if err == nil {
			return volume, nil
		}
		lastErr = err
		if !isTransientEnvironmentArtifactPublishError(err) {
			return nil, err
		}
		timer := time.NewTimer(retryInterval)
		select {
		case <-waitCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			if lastErr != nil {
				return nil, fmt.Errorf("%w after retrying for %s", lastErr, retryTimeout)
			}
			return nil, waitCtx.Err()
		case <-timer.C:
		}
	}
}

func isTransientEnvironmentArtifactPublishError(err error) bool {
	var apiErr *sandbox0sdk.APIError
	if !errors.As(err, &apiErr) {
		return true
	}
	switch apiErr.StatusCode {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
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
		if err := m.deleteEnvironmentArtifactAssets(ctx, client, artifact.ID, artifact.Assets); err != nil {
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

func (m *SDKRuntimeManager) deleteEnvironmentArtifactAssets(ctx context.Context, client *sandbox0sdk.Client, artifactID string, assets gatewaymanagedagents.EnvironmentArtifactAssets) error {
	var deleteErrs []error
	for _, volumeID := range assets.VolumeIDs() {
		if _, err := client.DeleteVolumeWithOptions(ctx, volumeID, &sandbox0sdk.DeleteVolumeOptions{Force: true}); err != nil {
			if isSandboxNotFound(err) {
				continue
			}
			m.logger.Warn("delete environment artifact volume failed", zap.Error(err), zap.String("artifact_id", artifactID), zap.String("volume_id", volumeID))
			deleteErrs = append(deleteErrs, fmt.Errorf("delete environment artifact volume %s: %w", volumeID, err))
		}
	}
	return errors.Join(deleteErrs...)
}

type environmentBuildStep struct {
	manager string
	script  string
	envVars map[string]string
}

func managedEnvironmentRuntimeEnvVars(controlToken string, environment *gatewaymanagedagents.Environment) map[string]string {
	env := map[string]string{
		"AGENT_WRAPPER_CONTROL_TOKEN": controlToken,
	}
	if environment == nil {
		return env
	}
	managers := gatewaymanagedagents.ConfiguredEnvironmentPackageManagers(environment.Config)
	if len(managers) == 0 {
		return env
	}

	pathEntries := make([]string, 0, 8)
	addPath := func(path string) {
		path = strings.TrimSpace(path)
		if path != "" {
			pathEntries = append(pathEntries, path)
		}
	}
	for _, manager := range managers {
		switch manager {
		case "apt":
			addPath(gatewaymanagedagents.ManagedEnvironmentAptMountPath + "/rootfs/usr/local/sbin")
			addPath(gatewaymanagedagents.ManagedEnvironmentAptMountPath + "/rootfs/usr/local/bin")
			addPath(gatewaymanagedagents.ManagedEnvironmentAptMountPath + "/rootfs/usr/sbin")
			addPath(gatewaymanagedagents.ManagedEnvironmentAptMountPath + "/rootfs/usr/bin")
			addPath(gatewaymanagedagents.ManagedEnvironmentAptMountPath + "/rootfs/sbin")
			addPath(gatewaymanagedagents.ManagedEnvironmentAptMountPath + "/rootfs/bin")
			env["LD_LIBRARY_PATH"] = strings.Join([]string{
				gatewaymanagedagents.ManagedEnvironmentAptMountPath + "/rootfs/usr/local/lib",
				gatewaymanagedagents.ManagedEnvironmentAptMountPath + "/rootfs/usr/lib",
				gatewaymanagedagents.ManagedEnvironmentAptMountPath + "/rootfs/lib",
			}, ":")
		case "cargo":
			addPath(gatewaymanagedagents.ManagedEnvironmentCargoMountPath + "/root/bin")
			env["CARGO_HOME"] = gatewaymanagedagents.ManagedEnvironmentCargoMountPath + "/home"
		case "gem":
			addPath(gatewaymanagedagents.ManagedEnvironmentGemMountPath + "/bin")
			env["GEM_HOME"] = gatewaymanagedagents.ManagedEnvironmentGemMountPath + "/home"
			env["GEM_PATH"] = gatewaymanagedagents.ManagedEnvironmentGemMountPath + "/home"
		case "go":
			addPath(gatewaymanagedagents.ManagedEnvironmentGoMountPath + "/bin")
			env["GOMODCACHE"] = gatewaymanagedagents.ManagedEnvironmentGoMountPath + "/pkg/mod"
		case "npm":
			addPath(gatewaymanagedagents.ManagedEnvironmentNPMMountPath + "/bin")
			env["NODE_PATH"] = gatewaymanagedagents.ManagedEnvironmentNPMMountPath + "/lib/node_modules"
		case "pip":
			addPath(gatewaymanagedagents.ManagedEnvironmentPipMountPath + "/venv/bin")
			env["VIRTUAL_ENV"] = gatewaymanagedagents.ManagedEnvironmentPipMountPath + "/venv"
		}
	}
	if len(pathEntries) > 0 {
		pathEntries = append(pathEntries, strings.Split(managedEnvironmentBasePATH, ":")...)
		env["PATH"] = strings.Join(pathEntries, ":")
	}
	return env
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
	manager   string
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
		mounts = append(mounts, environmentArtifactMount{manager: manager, volumeID: volumeID, mountPath: mountPath})
	}
	return mounts, nil
}

func forkSessionEnvironmentVolumes(ctx context.Context, client *sandbox0sdk.Client, mounts []environmentArtifactMount) ([]environmentArtifactMount, map[string]string, error) {
	if len(mounts) == 0 {
		return nil, nil, nil
	}
	sessionMounts := make([]environmentArtifactMount, 0, len(mounts))
	volumeIDs := make(map[string]string, len(mounts))
	for _, mount := range mounts {
		manager := strings.TrimSpace(mount.manager)
		sourceVolumeID := strings.TrimSpace(mount.volumeID)
		mountPath := strings.TrimSpace(mount.mountPath)
		if manager == "" || sourceVolumeID == "" || mountPath == "" {
			return nil, nil, fmt.Errorf("environment artifact mount is incomplete")
		}
		volume, err := client.ForkVolume(ctx, sourceVolumeID, &apispec.ForkVolumeRequest{
			AccessMode: apispec.NewOptVolumeAccessMode(apispec.VolumeAccessModeRWO),
		})
		if err != nil {
			for _, created := range sessionMounts {
				if strings.TrimSpace(created.volumeID) != "" {
					_, _ = client.DeleteVolumeWithOptions(ctx, created.volumeID, &sandbox0sdk.DeleteVolumeOptions{Force: true})
				}
			}
			return nil, nil, fmt.Errorf("fork %s environment volume: %w", manager, err)
		}
		sessionMounts = append(sessionMounts, environmentArtifactMount{
			manager:   manager,
			volumeID:  volume.ID,
			mountPath: mountPath,
		})
		volumeIDs[manager] = volume.ID
	}
	return sessionMounts, volumeIDs, nil
}

func runEnvironmentBuildStep(ctx context.Context, sandbox *sandbox0sdk.Sandbox, step environmentBuildStep) (string, error) {
	stream, err := sandbox.CmdStream(
		ctx,
		"environment-build",
		sandbox0sdk.WithCommand([]string{"sh", "-lc", step.script}),
		sandbox0sdk.WithCmdCWD("/"),
		sandbox0sdk.WithCmdEnvVars(step.envVars),
	)
	if err != nil {
		return "", err
	}
	defer stream.Close()

	stopClose := make(chan struct{})
	defer close(stopClose)
	go func() {
		select {
		case <-ctx.Done():
			_ = stream.Close()
		case <-stopClose:
		}
	}()

	var output strings.Builder
	for {
		message, err := stream.Recv()
		if err == nil {
			output.WriteString(message.Data)
			continue
		}
		if errors.Is(err, io.EOF) {
			break
		}
		return output.String(), err
	}

	done, ok := stream.Result()
	if !ok || done.ExitCode == nil {
		return output.String(), errors.New("environment build command did not report exit code")
	}
	if *done.ExitCode != 0 {
		return output.String(), fmt.Errorf("environment build command exited with code %d", *done.ExitCode)
	}
	return output.String(), nil
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
