package managedagentsruntime

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
	sandbox0sdk "github.com/sandbox0-ai/sdk-go"
	apispec "github.com/sandbox0-ai/sdk-go/pkg/apispec"
	"go.uber.org/zap"
)

type managedCredentialBinding struct {
	key                string
	sourceName         string
	domains            []string
	mcpServerName      string
	targetCanonicalURL string
	protocol           apispec.EgressAuthProtocol
	tlsMode            apispec.EgressTLSMode
	failurePolicy      apispec.EgressAuthFailurePolicy
	projectionHeaders  []managedProjectedHeader
	secretValues       map[string]string
}

type managedVaultCredentials struct {
	vault       gatewaymanagedagents.Vault
	credentials []gatewaymanagedagents.StoredCredential
}

type mcpServerTarget struct {
	name         string
	canonicalURL string
	host         string
	protocol     apispec.EgressAuthProtocol
}

type managedProjectedHeader struct {
	name          string
	valueTemplate string
}

const volumeFileOperationAttempts = 3

type volumeFileResourceClient interface {
	CloneVolumeFiles(ctx context.Context, volumeID string, request apispec.CloneVolumeFilesRequest) ([]apispec.CloneVolumeFileResult, error)
	ReadVolumeFile(ctx context.Context, volumeID, path string) ([]byte, error)
	MkdirVolumeFile(ctx context.Context, volumeID, path string, recursive bool) (*apispec.SuccessCreatedResponse, error)
	WriteVolumeFile(ctx context.Context, volumeID, path string, data []byte) (*apispec.SuccessWrittenResponse, error)
}

func (m *SDKRuntimeManager) syncBootstrapState(ctx context.Context, credential gatewaymanagedagents.RequestCredential, runtime *gatewaymanagedagents.RuntimeRecord, req *gatewaymanagedagents.WrapperSessionBootstrapRequest) (err error) {
	_ = credential
	ctx, op := m.observability.StartOperation(ctx, "runtime_sync_bootstrap_state", runtimeVendorForLog(runtime),
		zap.String("session_id", runtimeSessionID(runtime)),
		zap.String("sandbox_id", runtimeSandboxID(runtime)),
	)
	defer func() { op.End(err) }()
	phaseStarted := time.Now()
	record, _, err := m.repo.GetSession(ctx, req.SessionID)
	if err != nil {
		op.ObservePhase("load_session", time.Since(phaseStarted), err)
		return err
	}
	op.ObservePhase("load_session", time.Since(phaseStarted), nil)
	phaseStarted = time.Now()
	client, err := m.sandboxClientForRuntime(ctx, runtime)
	if err != nil {
		op.ObservePhase("create_sandbox_client", time.Since(phaseStarted), err)
		return err
	}
	op.ObservePhase("create_sandbox_client", time.Since(phaseStarted), nil)
	phaseStarted = time.Now()
	environment, err := m.repo.GetEnvironment(ctx, record.TeamID, record.EnvironmentID)
	if err != nil {
		op.ObservePhase("load_environment", time.Since(phaseStarted), err)
		return fmt.Errorf("resolve environment: %w", err)
	}
	op.ObservePhase("load_environment", time.Since(phaseStarted), nil)
	req.Environment = environmentSnapshot(environment)
	if strings.TrimSpace(record.EnvironmentArtifactID) != "" {
		phaseStarted = time.Now()
		artifact, err := m.repo.GetEnvironmentArtifact(ctx, record.TeamID, record.EnvironmentArtifactID)
		if err != nil {
			op.ObservePhase("load_environment_artifact", time.Since(phaseStarted), err)
			return fmt.Errorf("resolve environment artifact: %w", err)
		}
		req.EnvironmentArtifact = gatewaymanagedagents.EnvironmentArtifactSnapshotForRuntime(artifact)
		op.ObservePhase("load_environment_artifact", time.Since(phaseStarted), nil,
			zap.String("environment_artifact_id", artifact.ID),
			zap.String("environment_artifact_status", artifact.Status),
		)
	}
	phaseStarted = time.Now()
	if err := m.materializeFileResources(ctx, client, runtime.WorkspaceVolumeID, record.TeamID, req.Resources); err != nil {
		op.ObservePhase("materialize_file_resources", time.Since(phaseStarted), err,
			zap.Int("resource_count", len(req.Resources)),
		)
		return err
	}
	op.ObservePhase("materialize_file_resources", time.Since(phaseStarted), nil,
		zap.Int("resource_count", len(req.Resources)),
	)
	phaseStarted = time.Now()
	githubBindings, err := m.syncGitHubCredentialSources(ctx, client, req.SessionID, req.Resources)
	if err != nil {
		op.ObservePhase("sync_github_credential_sources", time.Since(phaseStarted), err)
		return err
	}
	op.ObservePhase("sync_github_credential_sources", time.Since(phaseStarted), nil,
		zap.Int("binding_count", len(githubBindings)),
	)
	mcpTargets := sessionAgentMCPServerTargets(req.Agent)
	phaseStarted = time.Now()
	vaultCredentials, bootstrapEvents, err := m.loadActiveVaultCredentials(ctx, record.TeamID, req.VaultIDs, mcpTargets)
	if err != nil {
		op.ObservePhase("load_vault_credentials", time.Since(phaseStarted), err,
			zap.Int("vault_count", len(req.VaultIDs)),
		)
		return err
	}
	op.ObservePhase("load_vault_credentials", time.Since(phaseStarted), nil,
		zap.Int("vault_count", len(req.VaultIDs)),
		zap.Int("bootstrap_event_count", len(bootstrapEvents)),
	)
	phaseStarted = time.Now()
	skillNames, skippedSkillMaterialization, err := m.agentSkillNamesFromWorkspaceBase(ctx, record.TeamID, req.WorkingDirectory, req.Agent, runtime.WorkspaceBaseDigest)
	if err != nil {
		op.ObservePhase("resolve_workspace_base_skills", time.Since(phaseStarted), err)
		return err
	}
	if !skippedSkillMaterialization {
		skillNames, err = m.materializeAgentSkills(ctx, client, runtime.SandboxID, runtime.WorkspaceVolumeID, record.TeamID, req.WorkingDirectory, req.Vendor, req.Engine, req.Agent)
		if err != nil {
			op.ObservePhase("materialize_agent_skills", time.Since(phaseStarted), err)
			return err
		}
	}
	req.SkillNames = skillNames
	op.ObservePhase("materialize_agent_skills", time.Since(phaseStarted), nil,
		zap.Int("skill_count", len(skillNames)),
		zap.Bool("skipped", skippedSkillMaterialization),
	)
	var llmCredential *managedLLMCredential
	phaseStarted = time.Now()
	req.Engine, llmCredential, err = applyManagedLLMEnv(req.Vendor, req.Engine, vaultCredentials)
	if err != nil {
		op.ObservePhase("apply_llm_environment", time.Since(phaseStarted), err)
		return err
	}
	op.ObservePhase("apply_llm_environment", time.Since(phaseStarted), nil,
		zap.Bool("has_llm_credential", llmCredential != nil),
	)
	phaseStarted = time.Now()
	llmBindings, err := m.syncManagedLLMCredentialSource(ctx, client, req.SessionID, req.Vendor, llmCredential)
	if err != nil {
		op.ObservePhase("sync_llm_credential_source", time.Since(phaseStarted), err)
		return err
	}
	op.ObservePhase("sync_llm_credential_source", time.Since(phaseStarted), nil,
		zap.Int("binding_count", len(llmBindings)),
	)
	phaseStarted = time.Now()
	vaultBindings, vaultEvents, err := m.syncVaultCredentialSources(ctx, client, req.SessionID, mcpTargets, flattenVaultCredentials(vaultCredentials))
	if err != nil {
		op.ObservePhase("sync_vault_credential_sources", time.Since(phaseStarted), err)
		return err
	}
	op.ObservePhase("sync_vault_credential_sources", time.Since(phaseStarted), nil,
		zap.Int("binding_count", len(vaultBindings)),
		zap.Int("bootstrap_event_count", len(vaultEvents)),
	)
	req.BootstrapEvents = append(req.BootstrapEvents, bootstrapEvents...)
	req.BootstrapEvents = append(req.BootstrapEvents, vaultEvents...)
	bindings := append(githubBindings, llmBindings...)
	bindings = append(bindings, vaultBindings...)
	phaseStarted = time.Now()
	policy := m.runtimeNetworkPolicy(environment, req.Engine, req.Agent)
	err = m.syncSandboxNetworkPolicy(ctx, client.Sandbox(runtime.SandboxID), req.SessionID, policy, bindings)
	op.ObservePhase("sync_sandbox_network_policy", time.Since(phaseStarted), err,
		zap.Int("binding_count", len(bindings)),
	)
	return err
}

func (m *SDKRuntimeManager) loadActiveVaultCredentials(ctx context.Context, teamID string, vaultIDs []string, mcpTargets map[string]mcpServerTarget) ([]managedVaultCredentials, []map[string]any, error) {
	if len(vaultIDs) == 0 {
		return nil, nil, nil
	}
	vaults := make([]managedVaultCredentials, 0, len(vaultIDs))
	bootstrapEvents := make([]map[string]any, 0)
	for _, vaultID := range vaultIDs {
		vault, err := m.repo.GetVault(ctx, teamID, vaultID)
		if err != nil {
			return nil, nil, err
		}
		vaultCopy := *vault
		items, err := m.repo.ListActiveCredentialsForVault(ctx, teamID, vaultID)
		if err != nil {
			return nil, nil, err
		}
		group := managedVaultCredentials{vault: vaultCopy, credentials: make([]gatewaymanagedagents.StoredCredential, 0, len(items))}
		config := gatewaymanagedagents.ManagedVaultConfigFromMetadata(vaultCopy.Metadata)
		if config.Role == gatewaymanagedagents.ManagedAgentsVaultRoleCredential && len(items) != 1 {
			return nil, nil, fmt.Errorf("credential vault %s must contain exactly one active credential", vaultID)
		}
		for _, credential := range items {
			credential.Vault = &vaultCopy
			credential, err = m.maybeRefreshVaultCredential(ctx, teamID, vaultID, credential, time.Now().UTC())
			credential.Vault = &vaultCopy
			if err != nil {
				if target, ok := credentialMCPServerTarget(credential, mcpTargets); ok && !isManagedLLMCredential(credential) {
					bootstrapEvents = append(bootstrapEvents, mcpAuthenticationFailedEvent(target.name, err))
					continue
				}
				if !isManagedLLMCredential(credential) {
					continue
				}
				return nil, nil, err
			}
			group.credentials = append(group.credentials, credential)
		}
		vaults = append(vaults, group)
	}
	return vaults, bootstrapEvents, nil
}

func flattenVaultCredentials(vaults []managedVaultCredentials) []gatewaymanagedagents.StoredCredential {
	credentials := make([]gatewaymanagedagents.StoredCredential, 0)
	for i := range vaults {
		credentials = append(credentials, vaults[i].credentials...)
	}
	return credentials
}

func (m *SDKRuntimeManager) materializeFileResources(ctx context.Context, client volumeFileResourceClient, workspaceVolumeID, teamID string, resources []map[string]any) error {
	if strings.TrimSpace(workspaceVolumeID) == "" {
		return errors.New("workspace volume is required")
	}
	var teamStore *gatewaymanagedagents.TeamAssetStore
	entries := make([]apispec.CloneVolumeFileEntry, 0)
	for _, resource := range resources {
		if resourceType(resource) != "file" {
			continue
		}
		fileID := strings.TrimSpace(stringValue(resource["file_id"]))
		mountPath := cleanMountPath(stringValue(resource["mount_path"]))
		if fileID == "" || mountPath == "" {
			return fmt.Errorf("file resource is missing file_id or mount_path")
		}
		record, err := m.repo.GetFile(ctx, teamID, fileID)
		if err != nil {
			return fmt.Errorf("resolve file resource %s: %w", fileID, err)
		}
		if teamStore == nil {
			regionID, err := m.repo.ResolveRuntimeRegionID(ctx, teamID)
			if err != nil {
				return fmt.Errorf("resolve team asset store region: %w", err)
			}
			teamStore, err = m.repo.GetTeamAssetStore(ctx, teamID, regionID)
			if err != nil {
				return fmt.Errorf("resolve team asset store: %w", err)
			}
		}
		entries = append(entries, apispec.CloneVolumeFileEntry{
			SourceVolumeID: teamStore.VolumeID,
			SourcePath:     record.StorePath,
			TargetPath:     mountPath,
			Overwrite:      apispec.NewOptBool(true),
			CreateParents:  apispec.NewOptBool(true),
		})
	}
	if len(entries) == 0 {
		return nil
	}
	if err := cloneFileResourceEntries(ctx, client, workspaceVolumeID, entries); err != nil {
		return fmt.Errorf("clone file resources into workspace volume: %w", err)
	}
	return nil
}

func cloneFileResourceEntries(ctx context.Context, client volumeFileResourceClient, workspaceVolumeID string, entries []apispec.CloneVolumeFileEntry) error {
	if len(entries) == 0 {
		return nil
	}
	err := retryVolumeFileOperation(ctx, func() error {
		_, err := client.CloneVolumeFiles(ctx, workspaceVolumeID, apispec.CloneVolumeFilesRequest{
			Mode:    apispec.NewOptCloneVolumeFilesRequestMode(apispec.CloneVolumeFilesRequestModeCowOrCopy),
			Atomic:  apispec.NewOptBool(true),
			Entries: entries,
		})
		return err
	})
	if err != nil && shouldFallbackFileResourceClone(err) {
		return copyFileResourceEntries(ctx, client, workspaceVolumeID, entries)
	}
	if err != nil {
		return err
	}
	return nil
}

func copyFileResourceEntries(ctx context.Context, client volumeFileResourceClient, workspaceVolumeID string, entries []apispec.CloneVolumeFileEntry) error {
	for _, entry := range entries {
		var content []byte
		err := retryVolumeFileOperation(ctx, func() error {
			var err error
			content, err = client.ReadVolumeFile(ctx, entry.SourceVolumeID, entry.SourcePath)
			return err
		})
		if err != nil {
			return fmt.Errorf("read asset-store resource %s: %w", entry.SourcePath, err)
		}
		parent := path.Dir(entry.TargetPath)
		if parent != "." && parent != "/" {
			err := retryVolumeFileOperation(ctx, func() error {
				_, err := client.MkdirVolumeFile(ctx, workspaceVolumeID, parent, true)
				return err
			})
			if err != nil {
				return fmt.Errorf("mkdir resource path %s: %w", parent, err)
			}
		}
		err = retryVolumeFileOperation(ctx, func() error {
			_, err := client.WriteVolumeFile(ctx, workspaceVolumeID, entry.TargetPath, content)
			return err
		})
		if err != nil {
			return fmt.Errorf("write file resource %s to %s: %w", entry.SourcePath, entry.TargetPath, err)
		}
	}
	return nil
}

func shouldFallbackFileResourceClone(err error) bool {
	var apiErr *sandbox0sdk.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusNotFound &&
			strings.EqualFold(apiErr.Code, "not_found") &&
			strings.TrimSpace(apiErr.Message) == "not found"
	}
	message := err.Error()
	return strings.Contains(message, "sandbox0 API error (404): not_found - not found")
}

func retryVolumeFileOperation(ctx context.Context, operation func() error) error {
	var lastErr error
	for attempt := 1; attempt <= volumeFileOperationAttempts; attempt++ {
		if err := operation(); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			lastErr = err
			if attempt == volumeFileOperationAttempts {
				return lastErr
			}
			timer := time.NewTimer(time.Duration(attempt) * 200 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			continue
		}
		return nil
	}
	return lastErr
}

func (m *SDKRuntimeManager) materializeAgentSkills(ctx context.Context, client *sandbox0sdk.Client, sandboxID, workspaceVolumeID, teamID, workingDirectory, vendor string, engine map[string]any, agent map[string]any) ([]string, error) {
	skillEntries := anySlice(agent["skills"])
	if len(skillEntries) == 0 {
		return []string{}, nil
	}
	preloadSet := make(map[string]struct{}, len(skillEntries))
	var teamStore *gatewaymanagedagents.TeamAssetStore
	for _, raw := range skillEntries {
		skill := mapValue(raw)
		skillType := strings.TrimSpace(stringValue(skill["type"]))
		skillID := strings.TrimSpace(stringValue(skill["skill_id"]))
		version := strings.TrimSpace(stringValue(skill["version"]))
		if skillID == "" {
			return nil, errors.New("agent skill is missing skill_id")
		}
		switch skillType {
		case "anthropic":
			return nil, fmt.Errorf("anthropic pre-built skill %s is not supported", skillID)
		case "custom":
			if version == "" {
				return nil, fmt.Errorf("custom skill %s is missing version", skillID)
			}
			stored, err := m.repo.GetStoredSkillVersion(ctx, teamID, skillID, version)
			if err != nil {
				return nil, fmt.Errorf("resolve custom skill %s@%s: %w", skillID, version, err)
			}
			directory := skillDirectoryName(stored, skillID)
			if directory == "" {
				return nil, fmt.Errorf("custom skill %s@%s is missing directory", skillID, version)
			}
			if teamStore == nil {
				regionID, err := m.repo.ResolveRuntimeRegionID(ctx, teamID)
				if err != nil {
					return nil, fmt.Errorf("resolve team asset store region: %w", err)
				}
				teamStore, err = m.repo.GetTeamAssetStore(ctx, teamID, regionID)
				if err != nil {
					return nil, fmt.Errorf("resolve team asset store: %w", err)
				}
			}
			if err := m.materializeStoredSkillBundle(ctx, client, sandboxID, workspaceVolumeID, teamStore.VolumeID, stored.Snapshot.ID, stored.Bundle.Path, workingDirectory, directory); err != nil {
				return nil, fmt.Errorf("materialize custom skill %s@%s bundle: %w", skillID, version, err)
			}
			preloadSet[directory] = struct{}{}
		default:
			return nil, fmt.Errorf("unsupported agent skill type %q", skillType)
		}
	}
	preloadNames := make([]string, 0, len(preloadSet))
	for name := range preloadSet {
		preloadNames = append(preloadNames, name)
	}
	sort.Strings(preloadNames)
	return preloadNames, nil
}

func (m *SDKRuntimeManager) materializeStoredSkillBundle(ctx context.Context, client *sandbox0sdk.Client, sandboxID, workspaceVolumeID, assetVolumeID, skillVersionID, bundlePath, workingDirectory, directory string) error {
	if strings.TrimSpace(sandboxID) == "" {
		return errors.New("sandbox id is required")
	}
	if strings.TrimSpace(workspaceVolumeID) == "" {
		return errors.New("workspace volume is required")
	}
	if strings.TrimSpace(assetVolumeID) == "" || strings.TrimSpace(bundlePath) == "" {
		return errors.New("skill bundle asset source is incomplete")
	}

	bundleContent, err := client.ReadVolumeFile(ctx, assetVolumeID, bundlePath)
	if err != nil {
		return fmt.Errorf("read skill bundle: %w", err)
	}

	bundleVolumePath := skillBundleWorkspaceVolumePath(skillVersionID)
	parent := path.Dir(bundleVolumePath)
	if parent != "." && parent != "/" {
		err := retryVolumeFileOperation(ctx, func() error {
			_, err := client.MkdirVolumeFile(ctx, workspaceVolumeID, parent, true)
			return err
		})
		if err != nil {
			return fmt.Errorf("mkdir skill bundle path %s: %w", parent, err)
		}
	}
	err = retryVolumeFileOperation(ctx, func() error {
		_, err := client.WriteVolumeFile(ctx, workspaceVolumeID, bundleVolumePath, bundleContent)
		return err
	})
	if err != nil {
		return fmt.Errorf("write skill bundle %s: %w", bundleVolumePath, err)
	}

	bundleContainerPath := workspaceVolumePathToMountedPath(m.cfg.WorkspaceMountPath, bundleVolumePath)
	skillsContainerPath := skillWorkspaceSkillsContainerPath(workingDirectory)
	if bundleContainerPath == "" || skillsContainerPath == "" || workspaceMountedPathToVolumePath(m.cfg.WorkspaceMountPath, skillsContainerPath) == "" {
		return errors.New("skill workspace path is invalid")
	}
	result, err := client.Sandbox(sandboxID).Cmd(
		ctx,
		"managed-agent-skill-bundle-unpack",
		sandbox0sdk.WithCommand(skillBundleUnpackCommand(bundleContainerPath, skillsContainerPath, directory)),
		sandbox0sdk.WithCmdCWD("/"),
	)
	if err != nil {
		output := strings.TrimSpace(result.OutputRaw)
		if output != "" {
			return fmt.Errorf("unpack skill bundle: %w: %s", err, output)
		}
		return fmt.Errorf("unpack skill bundle: %w", err)
	}
	return nil
}

func (m *SDKRuntimeManager) syncGitHubCredentialSources(ctx context.Context, client *sandbox0sdk.Client, sessionID string, resources []map[string]any) ([]managedCredentialBinding, error) {
	bindings := make([]managedCredentialBinding, 0)
	for _, resource := range resources {
		if resourceType(resource) != "github_repository" {
			continue
		}
		resourceID := strings.TrimSpace(stringValue(resource["id"]))
		if resourceID == "" {
			return nil, errors.New("github_repository resource is missing id")
		}
		secret, err := m.repo.GetSessionResourceSecret(ctx, sessionID, resourceID)
		if err != nil {
			return nil, err
		}
		token := strings.TrimSpace(stringValue(secret["authorization_token"]))
		if token == "" {
			return nil, fmt.Errorf("github_repository resource %s is missing authorization token", resourceID)
		}
		binding := managedCredentialBinding{
			key:        resourceID,
			sourceName: managedCredentialSourceName(sessionID, resourceID),
			domains:    githubDomains(resource),
			protocol:   apispec.EgressAuthProtocolHTTPS,
			tlsMode:    tlsModeForProtocol(apispec.EgressAuthProtocolHTTPS),
			projectionHeaders: []managedProjectedHeader{{
				name:          "Authorization",
				valueTemplate: "{{ .authorization }}",
			}},
			secretValues: map[string]string{
				"authorization": githubAuthorizationHeader(token),
			},
		}
		if err := m.upsertCredentialSource(ctx, client, binding); err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, nil
}

func (m *SDKRuntimeManager) syncManagedLLMCredentialSource(ctx context.Context, client *sandbox0sdk.Client, sessionID, vendor string, credential *managedLLMCredential) ([]managedCredentialBinding, error) {
	binding, err := managedLLMCredentialBinding(sessionID, vendor, credential)
	if err != nil {
		return nil, err
	}
	if binding == nil {
		return nil, nil
	}
	if err := m.upsertCredentialSource(ctx, client, *binding); err != nil {
		return nil, err
	}
	return []managedCredentialBinding{*binding}, nil
}

func (m *SDKRuntimeManager) syncVaultCredentialSources(ctx context.Context, client *sandbox0sdk.Client, sessionID string, mcpTargets map[string]mcpServerTarget, credentials []gatewaymanagedagents.StoredCredential) ([]managedCredentialBinding, []map[string]any, error) {
	if len(credentials) == 0 {
		return nil, nil, nil
	}
	bindings := make([]managedCredentialBinding, 0)
	bootstrapEvents := make([]map[string]any, 0)
	seenCanonicalTargets := make(map[string]managedCredentialBinding)
	seenHostTargets := make(map[string]managedCredentialBinding)
	for _, credential := range credentials {
		binding, err := managedBindingFromVaultCredential(sessionID, credential, mcpTargets)
		if err != nil {
			if target, ok := credentialMCPServerTarget(credential, mcpTargets); ok {
				bootstrapEvents = append(bootstrapEvents, mcpAuthenticationFailedEvent(target.name, err))
				continue
			}
			return nil, nil, err
		}
		if binding == nil {
			continue
		}
		if existing, ok := seenCanonicalTargets[binding.targetCanonicalURL]; ok {
			if binding.mcpServerName != "" {
				continue
			}
			return nil, nil, fmt.Errorf("multiple credentials target %s; credential %s conflicts with credential %s", strings.Join(binding.domains, ","), binding.key, existing.key)
		}
		seenCanonicalTargets[binding.targetCanonicalURL] = *binding
		targetKey := string(binding.protocol) + ":" + strings.Join(binding.domains, ",")
		if existing, ok := seenHostTargets[targetKey]; ok {
			err := fmt.Errorf("multiple credentials target %s; sandbox0 egress credential injection is currently scoped to host and protocol, so credential %s conflicts with credential %s", strings.Join(binding.domains, ","), binding.key, existing.key)
			if binding.mcpServerName != "" {
				bootstrapEvents = append(bootstrapEvents, mcpAuthenticationFailedEvent(binding.mcpServerName, err))
				continue
			}
			return nil, nil, err
		}
		seenHostTargets[targetKey] = *binding
		if err := m.upsertCredentialSource(ctx, client, *binding); err != nil {
			return nil, nil, err
		}
		bindings = append(bindings, *binding)
	}
	return bindings, bootstrapEvents, nil
}

func (m *SDKRuntimeManager) upsertCredentialSource(ctx context.Context, client *sandbox0sdk.Client, binding managedCredentialBinding) error {
	request := apispec.CredentialSourceWriteRequest{
		Name:         binding.sourceName,
		ResolverKind: apispec.CredentialSourceResolverKindStaticHeaders,
		Spec: apispec.CredentialSourceWriteSpec{
			StaticHeaders: apispec.NewOptStaticHeadersSourceSpec(apispec.StaticHeadersSourceSpec{
				Values: apispec.NewOptStaticHeadersSourceSpecValues(apispec.StaticHeadersSourceSpecValues(binding.secretValues)),
			}),
		},
	}
	if _, err := client.UpdateCredentialSource(ctx, binding.sourceName, request); err != nil {
		var apiErr *sandbox0sdk.APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != 404 {
			return fmt.Errorf("upsert credential source %s: %w", binding.sourceName, err)
		}
		if _, err := client.CreateCredentialSource(ctx, request); err != nil {
			return fmt.Errorf("create credential source %s: %w", binding.sourceName, err)
		}
	}
	return nil
}

func managedBindingFromVaultCredential(sessionID string, credential gatewaymanagedagents.StoredCredential, mcpTargets map[string]mcpServerTarget) (*managedCredentialBinding, error) {
	snapshot := credential.Snapshot
	secret := credential.Secret
	credentialID := strings.TrimSpace(snapshot.ID)
	if credentialID == "" {
		return nil, errors.New("vault credential is missing id")
	}
	if isManagedLLMCredential(credential) {
		return nil, nil
	}
	if isManagedGenericCredential(credential) {
		return managedHTTPHeadersBindingFromVaultCredential(sessionID, credential)
	}
	serverURL := credentialMCPServerURL(credential)
	if serverURL == "" {
		return nil, fmt.Errorf("vault credential %s is missing mcp_server_url", credentialID)
	}
	canonicalURL, err := gatewaymanagedagents.CanonicalMCPServerURL(serverURL)
	if err != nil {
		return nil, fmt.Errorf("vault credential %s has invalid mcp_server_url", credentialID)
	}
	target, ok := mcpTargets[canonicalURL]
	if !ok {
		return nil, nil
	}
	parsedURL, err := url.Parse(serverURL)
	if err != nil || strings.TrimSpace(parsedURL.Hostname()) == "" {
		return nil, fmt.Errorf("vault credential %s has invalid mcp_server_url", credentialID)
	}
	auth := gatewaymanagedagents.CredentialAuthToMapForRuntime(snapshot.Auth)
	binding := &managedCredentialBinding{
		key:                credentialID,
		sourceName:         managedCredentialSourceName(sessionID, credentialID),
		domains:            []string{target.host},
		mcpServerName:      target.name,
		targetCanonicalURL: canonicalURL,
		protocol:           target.protocol,
		tlsMode:            tlsModeForProtocol(target.protocol),
		projectionHeaders: []managedProjectedHeader{{
			name:          "Authorization",
			valueTemplate: "{{ .authorization }}",
		}},
		secretValues: map[string]string{},
	}
	switch strings.TrimSpace(stringValue(auth["type"])) {
	case "static_bearer":
		token := strings.TrimSpace(stringValue(secret["token"]))
		if token == "" {
			return nil, fmt.Errorf("vault credential %s is missing token", credentialID)
		}
		binding.secretValues["authorization"] = "Bearer " + token
	case "mcp_oauth":
		accessToken := strings.TrimSpace(stringValue(secret["access_token"]))
		if accessToken == "" {
			return nil, fmt.Errorf("vault credential %s is missing access_token", credentialID)
		}
		binding.secretValues["authorization"] = "Bearer " + accessToken
	default:
		return nil, fmt.Errorf("vault credential %s has unsupported auth type %q", credentialID, stringValue(auth["type"]))
	}
	return binding, nil
}

func managedHTTPHeadersBindingFromVaultCredential(sessionID string, credential gatewaymanagedagents.StoredCredential) (*managedCredentialBinding, error) {
	if credential.Vault == nil {
		return nil, errors.New("generic credential vault metadata is missing")
	}
	config := gatewaymanagedagents.ManagedVaultConfigFromMetadata(credential.Vault.Metadata)
	snapshot := credential.Snapshot
	credentialID := strings.TrimSpace(snapshot.ID)
	if credentialID == "" {
		return nil, errors.New("vault credential is missing id")
	}
	if config.Kind != gatewaymanagedagents.ManagedAgentsVaultKindHTTPHeaders {
		return nil, fmt.Errorf("credential vault %s has unsupported kind %q", credential.Vault.ID, config.Kind)
	}
	if len(config.TargetDomains) == 0 {
		return nil, fmt.Errorf("credential vault %s is missing target domains", credential.Vault.ID)
	}
	if len(config.Headers) == 0 {
		return nil, fmt.Errorf("credential vault %s is missing header projection metadata", credential.Vault.ID)
	}
	secretValues, err := managedCredentialSecretValues(credential)
	if err != nil {
		return nil, err
	}
	headers := make([]managedProjectedHeader, 0, len(config.Headers))
	for name, valueTemplate := range config.Headers {
		headers = append(headers, managedProjectedHeader{
			name:          strings.TrimSpace(name),
			valueTemplate: strings.TrimSpace(valueTemplate),
		})
	}
	sort.Slice(headers, func(i, j int) bool {
		return strings.ToLower(headers[i].name) < strings.ToLower(headers[j].name)
	})
	protocol := apispec.EgressAuthProtocolHTTPS
	if config.Protocol == "http" {
		protocol = apispec.EgressAuthProtocolHTTP
	}
	tlsMode := tlsModeForProtocol(protocol)
	if config.TLSMode != "" {
		tlsMode = apispec.EgressTLSMode(config.TLSMode)
	}
	return &managedCredentialBinding{
		key:                credentialID,
		sourceName:         managedCredentialSourceName(sessionID, credentialID),
		domains:            append([]string(nil), config.TargetDomains...),
		targetCanonicalURL: "generic:" + string(protocol) + ":" + strings.Join(config.TargetDomains, ","),
		protocol:           protocol,
		tlsMode:            tlsMode,
		failurePolicy:      apispec.EgressAuthFailurePolicy(config.FailurePolicy),
		projectionHeaders:  headers,
		secretValues:       secretValues,
	}, nil
}

func managedCredentialSecretValues(credential gatewaymanagedagents.StoredCredential) (map[string]string, error) {
	snapshot := credential.Snapshot
	secret := credential.Secret
	credentialID := strings.TrimSpace(snapshot.ID)
	auth := gatewaymanagedagents.CredentialAuthToMapForRuntime(snapshot.Auth)
	values := make(map[string]string)
	switch strings.TrimSpace(stringValue(auth["type"])) {
	case "static_bearer":
		token := strings.TrimSpace(stringValue(secret["token"]))
		if token == "" {
			return nil, fmt.Errorf("vault credential %s is missing token", credentialID)
		}
		values["token"] = token
		values["authorization"] = "Bearer " + token
	case "mcp_oauth":
		accessToken := strings.TrimSpace(stringValue(secret["access_token"]))
		if accessToken == "" {
			return nil, fmt.Errorf("vault credential %s is missing access_token", credentialID)
		}
		values["access_token"] = accessToken
		values["authorization"] = "Bearer " + accessToken
	default:
		return nil, fmt.Errorf("vault credential %s has unsupported auth type %q", credentialID, stringValue(auth["type"]))
	}
	return values, nil
}

func (m *SDKRuntimeManager) syncSandboxNetworkPolicy(ctx context.Context, sandbox *sandbox0sdk.Sandbox, sessionID string, policy apispec.SandboxNetworkPolicy, bindings []managedCredentialBinding) error {
	policy = mergeManagedCredentialPolicy(policy, sessionID, bindings)
	_, err := sandbox.UpdateNetworkPolicy(ctx, policy)
	if err != nil {
		return fmt.Errorf("update sandbox network policy: %w", err)
	}
	return nil
}

func (m *SDKRuntimeManager) cleanupManagedCredentialSources(ctx context.Context, client *sandbox0sdk.Client, sessionID string) error {
	sources, err := client.ListCredentialSources(ctx)
	if err != nil {
		m.logger.Warn("list credential sources failed", zap.Error(err), zap.String("session_id", sessionID))
		return fmt.Errorf("list credential sources for session %s: %w", sessionID, err)
	}
	prefix := managedCredentialSourcePrefix(sessionID)
	var cleanupErrs []error
	for _, source := range sources {
		if !strings.HasPrefix(source.Name, prefix) {
			continue
		}
		if _, err := client.DeleteCredentialSource(ctx, source.Name); err != nil {
			if isSandboxNotFound(err) {
				continue
			}
			m.logger.Warn("delete credential source failed", zap.Error(err), zap.String("source_name", source.Name), zap.String("session_id", sessionID))
			cleanupErrs = append(cleanupErrs, fmt.Errorf("delete credential source %s: %w", source.Name, err))
		}
	}
	return errors.Join(cleanupErrs...)
}

func mergeManagedCredentialPolicy(base apispec.SandboxNetworkPolicy, sessionID string, bindings []managedCredentialBinding) apispec.SandboxNetworkPolicy {
	if strings.TrimSpace(string(base.Mode)) == "" {
		base.Mode = apispec.SandboxNetworkPolicyModeAllowAll
	}
	filteredBindings := make([]apispec.CredentialBinding, 0, len(base.CredentialBindings)+len(bindings))
	for _, binding := range base.CredentialBindings {
		if !strings.HasPrefix(binding.Ref, managedCredentialBindingPrefix(sessionID)) {
			filteredBindings = append(filteredBindings, binding)
		}
	}
	egress, _ := base.Egress.Get()
	filteredRules := make([]apispec.EgressCredentialRule, 0, len(egress.CredentialRules)+len(bindings))
	for _, rule := range egress.CredentialRules {
		name, ok := rule.Name.Get()
		if ok && strings.HasPrefix(name, managedCredentialRulePrefix(sessionID)) {
			continue
		}
		filteredRules = append(filteredRules, rule)
	}
	for _, binding := range bindings {
		if base.Mode == apispec.SandboxNetworkPolicyModeBlockAll {
			egress.AllowedDomains = appendUniqueStrings(egress.AllowedDomains, binding.domains...)
		}
		ref := managedCredentialBindingRef(sessionID, binding.key)
		filteredBindings = append(filteredBindings, apispec.CredentialBinding{
			Ref:       ref,
			SourceRef: binding.sourceName,
			Projection: apispec.ProjectionSpec{
				Type: apispec.CredentialProjectionTypeHTTPHeaders,
				HttpHeaders: apispec.NewOptHTTPHeadersProjection(apispec.HTTPHeadersProjection{
					Headers: projectedHeadersForBinding(binding),
				}),
			},
		})
		rule := apispec.EgressCredentialRule{
			Name:          apispec.NewOptString(managedCredentialRuleName(sessionID, binding.key)),
			CredentialRef: ref,
			Protocol:      apispec.NewOptEgressAuthProtocol(binding.protocol),
			Domains:       append([]string(nil), binding.domains...),
			Rollout:       apispec.NewOptEgressAuthRolloutMode(apispec.EgressAuthRolloutModeEnabled),
		}
		if binding.tlsMode != "" {
			rule.TlsMode = apispec.NewOptEgressTLSMode(binding.tlsMode)
		}
		if binding.failurePolicy != "" {
			rule.FailurePolicy = apispec.NewOptEgressAuthFailurePolicy(binding.failurePolicy)
		}
		filteredRules = append(filteredRules, rule)
	}
	egress.CredentialRules = filteredRules
	base.CredentialBindings = filteredBindings
	base.Egress = apispec.NewOptNetworkEgressPolicy(egress)
	return base
}

func clearManagedCredentialPolicy(base apispec.SandboxNetworkPolicy, sessionID string) (apispec.SandboxNetworkPolicy, bool) {
	if strings.TrimSpace(sessionID) == "" {
		return base, false
	}
	bindingPrefix := managedCredentialBindingPrefix(sessionID)
	sourcePrefix := managedCredentialSourcePrefix(sessionID)
	rulePrefix := managedCredentialRulePrefix(sessionID)
	changed := false

	filteredBindings := base.CredentialBindings[:0]
	for _, binding := range base.CredentialBindings {
		if strings.HasPrefix(binding.Ref, bindingPrefix) || strings.HasPrefix(binding.SourceRef, sourcePrefix) {
			changed = true
			continue
		}
		filteredBindings = append(filteredBindings, binding)
	}
	base.CredentialBindings = filteredBindings

	egress, ok := base.Egress.Get()
	if ok {
		filteredRules := egress.CredentialRules[:0]
		for _, rule := range egress.CredentialRules {
			name, hasName := rule.Name.Get()
			if (hasName && strings.HasPrefix(name, rulePrefix)) || strings.HasPrefix(rule.CredentialRef, bindingPrefix) {
				changed = true
				continue
			}
			filteredRules = append(filteredRules, rule)
		}
		egress.CredentialRules = filteredRules
		base.Egress = apispec.NewOptNetworkEgressPolicy(egress)
	}

	return base, changed
}

func projectedHeadersForBinding(binding managedCredentialBinding) []apispec.ProjectedHeader {
	out := make([]apispec.ProjectedHeader, 0, len(binding.projectionHeaders))
	for _, header := range binding.projectionHeaders {
		out = append(out, apispec.ProjectedHeader{
			Name:          header.name,
			ValueTemplate: header.valueTemplate,
		})
	}
	return out
}

func managedCredentialSourcePrefix(sessionID string) string {
	return "managed-agent-" + sanitizeName(sessionID) + "-"
}

func managedCredentialSourceName(sessionID, resourceID string) string {
	return managedCredentialSourcePrefix(sessionID) + sanitizeName(resourceID)
}

func managedCredentialBindingPrefix(sessionID string) string {
	return "ma-bind-" + sanitizeName(sessionID) + "-"
}

func managedCredentialBindingRef(sessionID, resourceID string) string {
	return managedCredentialBindingPrefix(sessionID) + sanitizeName(resourceID)
}

func managedCredentialRulePrefix(sessionID string) string {
	return "ma-rule-" + sanitizeName(sessionID) + "-"
}

func managedCredentialRuleName(sessionID, resourceID string) string {
	return managedCredentialRulePrefix(sessionID) + sanitizeName(resourceID)
}

func sanitizeName(value string) string {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	if trimmed == "" {
		return "unknown"
	}
	var builder strings.Builder
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-', r == '_':
			builder.WriteRune('-')
		default:
			builder.WriteRune('-')
		}
	}
	return strings.Trim(builder.String(), "-")
}

func githubAuthorizationHeader(token string) string {
	payload := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return "Basic " + payload
}

func githubDomains(resource map[string]any) []string {
	resourceURL := strings.TrimSpace(stringValue(resource["url"]))
	if resourceURL == "" {
		return []string{"github.com"}
	}
	parsedURL, err := url.Parse(resourceURL)
	if err != nil || strings.TrimSpace(parsedURL.Hostname()) == "" {
		return []string{"github.com"}
	}
	return []string{strings.ToLower(strings.TrimSpace(parsedURL.Hostname()))}
}

func sessionAgentMCPServerTargets(agent map[string]any) map[string]mcpServerTarget {
	out := make(map[string]mcpServerTarget)
	for _, entry := range anySlice(agent["mcp_servers"]) {
		server := mapValue(entry)
		if strings.TrimSpace(stringValue(server["type"])) != "url" {
			continue
		}
		name := strings.TrimSpace(stringValue(server["name"]))
		serverURL := strings.TrimSpace(stringValue(server["url"]))
		if name == "" || serverURL == "" {
			continue
		}
		canonicalURL, err := gatewaymanagedagents.CanonicalMCPServerURL(serverURL)
		if err != nil {
			continue
		}
		host, err := gatewaymanagedagents.MCPServerURLHost(serverURL)
		if err != nil {
			continue
		}
		parsedURL, err := url.Parse(canonicalURL)
		if err != nil {
			continue
		}
		out[canonicalURL] = mcpServerTarget{
			name:         name,
			canonicalURL: canonicalURL,
			host:         host,
			protocol:     protocolForURL(parsedURL),
		}
	}
	return out
}

func credentialMCPServerTarget(credential gatewaymanagedagents.StoredCredential, targets map[string]mcpServerTarget) (mcpServerTarget, bool) {
	serverURL := credentialMCPServerURL(credential)
	if serverURL == "" {
		return mcpServerTarget{}, false
	}
	canonicalURL, err := gatewaymanagedagents.CanonicalMCPServerURL(serverURL)
	if err != nil {
		return mcpServerTarget{}, false
	}
	target, ok := targets[canonicalURL]
	return target, ok
}

func credentialMCPServerURL(credential gatewaymanagedagents.StoredCredential) string {
	auth := gatewaymanagedagents.CredentialAuthToMapForRuntime(credential.Snapshot.Auth)
	serverURL := strings.TrimSpace(stringValue(auth["mcp_server_url"]))
	if serverURL == "" {
		serverURL = strings.TrimSpace(stringValue(credential.Secret["mcp_server_url"]))
	}
	return serverURL
}

func isManagedLLMCredential(credential gatewaymanagedagents.StoredCredential) bool {
	if credential.Vault == nil {
		return false
	}
	return gatewaymanagedagents.ManagedVaultConfigFromMetadata(credential.Vault.Metadata).Role == gatewaymanagedagents.ManagedAgentsVaultRoleLLM
}

func isManagedGenericCredential(credential gatewaymanagedagents.StoredCredential) bool {
	if credential.Vault == nil {
		return false
	}
	return gatewaymanagedagents.ManagedVaultConfigFromMetadata(credential.Vault.Metadata).Role == gatewaymanagedagents.ManagedAgentsVaultRoleCredential
}

func mcpAuthenticationFailedEvent(serverName string, err error) map[string]any {
	message := "MCP authentication failed"
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		message = err.Error()
	}
	return map[string]any{
		"type": "session.error",
		"error": map[string]any{
			"type":            "mcp_authentication_failed_error",
			"message":         message,
			"retry_status":    map[string]any{"type": "terminal"},
			"mcp_server_name": serverName,
		},
	}
}

func protocolForURL(parsedURL *url.URL) apispec.EgressAuthProtocol {
	if parsedURL != nil && strings.EqualFold(strings.TrimSpace(parsedURL.Scheme), "http") {
		return apispec.EgressAuthProtocolHTTP
	}
	return apispec.EgressAuthProtocolHTTPS
}

func tlsModeForProtocol(protocol apispec.EgressAuthProtocol) apispec.EgressTLSMode {
	if protocol == apispec.EgressAuthProtocolHTTPS {
		return apispec.EgressTLSModeTerminateReoriginate
	}
	return ""
}

func managedLLMCredentialBinding(sessionID, vendor string, credential *managedLLMCredential) (*managedCredentialBinding, error) {
	if credential == nil {
		return nil, nil
	}
	baseURL, err := canonicalManagedRuntimeURL(resolvedManagedLLMBaseURL(vendor, credential))
	if err != nil {
		return nil, fmt.Errorf("vault credential %s has invalid managed-agent llm base URL", credential.CredentialID)
	}
	parsedURL, err := url.Parse(baseURL)
	if err != nil || strings.TrimSpace(parsedURL.Hostname()) == "" {
		return nil, fmt.Errorf("vault credential %s has invalid managed-agent llm base URL", credential.CredentialID)
	}
	protocol := protocolForURL(parsedURL)
	key := "llm-" + credential.CredentialID
	return &managedCredentialBinding{
		key:        key,
		sourceName: managedCredentialSourceName(sessionID, key),
		domains:    []string{strings.ToLower(strings.TrimSpace(parsedURL.Hostname()))},
		protocol:   protocol,
		tlsMode:    tlsModeForProtocol(protocol),
		projectionHeaders: []managedProjectedHeader{
			{name: "X-Api-Key", valueTemplate: "{{ .x_api_key }}"},
			{name: "Authorization", valueTemplate: "{{ .authorization }}"},
		},
		secretValues: map[string]string{
			"x_api_key":     credential.Token,
			"authorization": "Bearer " + credential.Token,
		},
	}, nil
}

func skillDirectoryName(stored *gatewaymanagedagents.StoredSkillVersion, fallback string) string {
	if stored != nil {
		if directory := sanitizeName(stored.Snapshot.Directory); directory != "" {
			return directory
		}
	}
	return sanitizeName(fallback)
}

func skillBundleWorkspaceVolumePath(skillVersionID string) string {
	return cleanMountPath(path.Join("/.sandbox0/managed-agents/skills", sanitizeName(skillVersionID), "bundle.tar.gz"))
}

func skillWorkspaceSkillsContainerPath(workingDirectory string) string {
	return cleanMountPath(path.Join(strings.TrimSpace(workingDirectory), ".claude", "skills"))
}

func workspaceMountedPathToVolumePath(workspaceMountPath, containerPath string) string {
	mountPath := cleanMountPath(workspaceMountPath)
	if mountPath == "" {
		mountPath = "/workspace"
	}
	targetPath := cleanMountPath(containerPath)
	if targetPath == "" {
		return ""
	}
	if mountPath == "/" {
		return targetPath
	}
	if targetPath == mountPath {
		return "/"
	}
	prefix := strings.TrimRight(mountPath, "/") + "/"
	if !strings.HasPrefix(targetPath, prefix) {
		return ""
	}
	return cleanMountPath("/" + strings.TrimPrefix(targetPath, prefix))
}

func workspaceVolumePathToMountedPath(workspaceMountPath, volumePath string) string {
	mountPath := cleanMountPath(workspaceMountPath)
	if mountPath == "" {
		mountPath = "/workspace"
	}
	targetPath := cleanMountPath(volumePath)
	if targetPath == "" {
		return ""
	}
	if mountPath == "/" {
		return targetPath
	}
	return cleanMountPath(path.Join(mountPath, strings.TrimPrefix(targetPath, "/")))
}

func skillBundleUnpackCommand(bundleContainerPath, skillsContainerPath, directory string) []string {
	script := strings.Join([]string{
		"set -eu",
		`BUNDLE_PATH="$1"`,
		`SKILLS_ROOT="$2"`,
		`SKILL_DIR="$3"`,
		`TMP_ROOT="$SKILLS_ROOT/.tmp-$SKILL_DIR-$$"`,
		`rm -rf "$TMP_ROOT"`,
		`mkdir -p "$TMP_ROOT" "$SKILLS_ROOT"`,
		`tar -xzf "$BUNDLE_PATH" -C "$TMP_ROOT"`,
		`if [ ! -f "$TMP_ROOT/$SKILL_DIR/SKILL.md" ]; then echo "missing extracted SKILL.md for $SKILL_DIR" >&2; exit 1; fi`,
		`rm -rf "$SKILLS_ROOT/$SKILL_DIR"`,
		`mv "$TMP_ROOT/$SKILL_DIR" "$SKILLS_ROOT/$SKILL_DIR"`,
		`rm -rf "$TMP_ROOT"`,
		`rm -f "$BUNDLE_PATH"`,
	}, "\n")
	return []string{"sh", "-lc", script, "managed-agent-skill-bundle", bundleContainerPath, skillsContainerPath, directory}
}

func resourceType(resource map[string]any) string {
	return strings.TrimSpace(stringValue(resource["type"]))
}

func cleanMountPath(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	cleaned := path.Clean(trimmed)
	if !strings.HasPrefix(cleaned, "/") {
		return ""
	}
	return cleaned
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func mapValue(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return map[string]any{}
}

func anySlice(value any) []any {
	if typed, ok := value.([]any); ok {
		return typed
	}
	return nil
}
