package managedagentsruntime

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
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
	projectionHeaders  []managedProjectedHeader
	secretValues       map[string]string
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

func (m *SDKRuntimeManager) syncBootstrapState(ctx context.Context, credential gatewaymanagedagents.RequestCredential, runtime *gatewaymanagedagents.RuntimeRecord, req *gatewaymanagedagents.WrapperSessionBootstrapRequest) error {
	record, _, err := m.repo.GetSession(ctx, req.SessionID)
	if err != nil {
		return err
	}
	client, err := m.newSandboxClient(credential.Token, record.TeamID)
	if err != nil {
		return err
	}
	environment, err := m.repo.GetEnvironment(ctx, record.TeamID, record.EnvironmentID)
	if err != nil {
		return fmt.Errorf("resolve environment: %w", err)
	}
	req.Environment = environmentSnapshot(environment)
	if strings.TrimSpace(record.EnvironmentArtifactID) != "" {
		artifact, err := m.repo.GetEnvironmentArtifact(ctx, record.TeamID, record.EnvironmentArtifactID)
		if err != nil {
			return fmt.Errorf("resolve environment artifact: %w", err)
		}
		req.EnvironmentArtifact = gatewaymanagedagents.EnvironmentArtifactSnapshotForRuntime(artifact)
	}
	if err := m.materializeFileResources(ctx, client, runtime.WorkspaceVolumeID, record.TeamID, req.Resources); err != nil {
		return err
	}
	githubBindings, err := m.syncGitHubCredentialSources(ctx, client, req.SessionID, req.Resources)
	if err != nil {
		return err
	}
	mcpTargets := sessionAgentMCPServerTargets(req.Agent)
	vaultCredentials, bootstrapEvents, err := m.loadActiveVaultCredentials(ctx, record.TeamID, req.VaultIDs, mcpTargets)
	if err != nil {
		return err
	}
	var llmCredential *managedLLMCredential
	req.Engine, llmCredential, err = applyManagedLLMEnv(req.Vendor, req.Engine, vaultCredentials)
	if err != nil {
		return err
	}
	skillNames, err := m.materializeAgentSkills(ctx, client, runtime.WorkspaceVolumeID, record.TeamID, req.WorkingDirectory, req.Vendor, req.Engine, req.Agent)
	if err != nil {
		return err
	}
	req.SkillNames = skillNames
	llmBindings, err := m.syncManagedLLMCredentialSource(ctx, client, req.SessionID, req.Vendor, llmCredential)
	if err != nil {
		return err
	}
	vaultBindings, vaultEvents, err := m.syncVaultCredentialSources(ctx, client, req.SessionID, mcpTargets, vaultCredentials)
	if err != nil {
		return err
	}
	req.BootstrapEvents = append(req.BootstrapEvents, bootstrapEvents...)
	req.BootstrapEvents = append(req.BootstrapEvents, vaultEvents...)
	bindings := append(githubBindings, llmBindings...)
	bindings = append(bindings, vaultBindings...)
	return m.syncSandboxNetworkPolicy(ctx, client.Sandbox(runtime.SandboxID), req.SessionID, runtimeNetworkPolicy(environment, req.Engine, req.Agent), bindings)
}

func (m *SDKRuntimeManager) loadActiveVaultCredentials(ctx context.Context, teamID string, vaultIDs []string, mcpTargets map[string]mcpServerTarget) ([]gatewaymanagedagents.StoredCredential, []map[string]any, error) {
	if len(vaultIDs) == 0 {
		return nil, nil, nil
	}
	credentials := make([]gatewaymanagedagents.StoredCredential, 0)
	bootstrapEvents := make([]map[string]any, 0)
	for _, vaultID := range vaultIDs {
		items, err := m.repo.ListActiveCredentialsForVault(ctx, teamID, vaultID)
		if err != nil {
			return nil, nil, err
		}
		for _, credential := range items {
			credential, err = m.maybeRefreshVaultCredential(ctx, teamID, vaultID, credential, time.Now().UTC())
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
			credentials = append(credentials, credential)
		}
	}
	return credentials, bootstrapEvents, nil
}

func (m *SDKRuntimeManager) materializeFileResources(ctx context.Context, client *sandbox0sdk.Client, workspaceVolumeID, teamID string, resources []map[string]any) error {
	if strings.TrimSpace(workspaceVolumeID) == "" {
		return errors.New("workspace volume is required")
	}
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
		content := record.Content
		if strings.TrimSpace(record.FileStoreVolumeID) != "" && strings.TrimSpace(record.FileStorePath) != "" {
			content, err = client.ReadVolumeFile(ctx, record.FileStoreVolumeID, record.FileStorePath)
			if err != nil {
				return fmt.Errorf("read file-store resource %s: %w", fileID, err)
			}
		}
		parent := path.Dir(mountPath)
		if parent != "." && parent != "/" {
			if _, err := client.MkdirVolumeFile(ctx, workspaceVolumeID, parent, true); err != nil {
				return fmt.Errorf("mkdir resource path %s: %w", parent, err)
			}
		}
		if _, err := client.WriteVolumeFile(ctx, workspaceVolumeID, mountPath, content); err != nil {
			return fmt.Errorf("write file resource %s to %s: %w", fileID, mountPath, err)
		}
	}
	return nil
}

func (m *SDKRuntimeManager) materializeAgentSkills(ctx context.Context, client *sandbox0sdk.Client, workspaceVolumeID, teamID, workingDirectory, vendor string, engine map[string]any, agent map[string]any) ([]string, error) {
	skillEntries := anySlice(agent["skills"])
	if len(skillEntries) == 0 {
		return []string{}, nil
	}
	preloadSet := make(map[string]struct{}, len(skillEntries))
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
			if err := writeStoredSkillFiles(ctx, client, workspaceVolumeID, workingDirectory, directory, stored.Files); err != nil {
				return nil, fmt.Errorf("materialize custom skill %s@%s: %w", skillID, version, err)
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
	seenCanonicalTargets := make(map[string]struct{})
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
		if _, ok := seenCanonicalTargets[binding.targetCanonicalURL]; ok {
			continue
		}
		seenCanonicalTargets[binding.targetCanonicalURL] = struct{}{}
		targetKey := string(binding.protocol) + ":" + strings.Join(binding.domains, ",")
		if existing, ok := seenHostTargets[targetKey]; ok {
			bootstrapEvents = append(bootstrapEvents, mcpAuthenticationFailedEvent(binding.mcpServerName, fmt.Errorf("multiple MCP credentials target %s; sandbox0 egress credential injection is currently scoped to host and protocol, so %s is using credential %s and %s cannot install a second credential for the same host", strings.Join(binding.domains, ","), existing.mcpServerName, existing.key, binding.mcpServerName)))
			continue
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

func (m *SDKRuntimeManager) syncSandboxNetworkPolicy(ctx context.Context, sandbox *sandbox0sdk.Sandbox, sessionID string, policy apispec.SandboxNetworkPolicy, bindings []managedCredentialBinding) error {
	policy = mergeManagedCredentialPolicy(policy, sessionID, bindings)
	_, err := sandbox.UpdateNetworkPolicy(ctx, policy)
	if err != nil {
		return fmt.Errorf("update sandbox network policy: %w", err)
	}
	return nil
}

func (m *SDKRuntimeManager) cleanupManagedCredentialSources(ctx context.Context, client *sandbox0sdk.Client, sessionID string) {
	sources, err := client.ListCredentialSources(ctx)
	if err != nil {
		m.logger.Warn("list credential sources failed", zap.Error(err), zap.String("session_id", sessionID))
		return
	}
	prefix := managedCredentialSourcePrefix(sessionID)
	for _, source := range sources {
		if !strings.HasPrefix(source.Name, prefix) {
			continue
		}
		if _, err := client.DeleteCredentialSource(ctx, source.Name); err != nil {
			m.logger.Warn("delete credential source failed", zap.Error(err), zap.String("source_name", source.Name), zap.String("session_id", sessionID))
		}
	}
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
		filteredRules = append(filteredRules, rule)
	}
	egress.CredentialRules = filteredRules
	base.CredentialBindings = filteredBindings
	base.Egress = apispec.NewOptNetworkEgressPolicy(egress)
	return base
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
	return normalizeManagedRuntimeMetadataValue(credential.Snapshot.Metadata[gatewaymanagedagents.ManagedAgentCredentialKindKey]) == gatewaymanagedagents.ManagedAgentCredentialKindLLM
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

func writeStoredSkillFiles(ctx context.Context, client *sandbox0sdk.Client, workspaceVolumeID, workingDirectory, directory string, files []gatewaymanagedagents.StoredSkillFile) error {
	for _, file := range files {
		targetPath := skillFileTargetPath(workingDirectory, directory, file.Path)
		if targetPath == "" {
			return errors.New("stored skill file path is invalid")
		}
		parent := path.Dir(targetPath)
		if parent != "." && parent != "/" {
			if _, err := client.MkdirVolumeFile(ctx, workspaceVolumeID, parent, true); err != nil {
				return fmt.Errorf("mkdir skill path %s: %w", parent, err)
			}
		}
		if _, err := client.WriteVolumeFile(ctx, workspaceVolumeID, targetPath, file.Content); err != nil {
			return fmt.Errorf("write skill file %s: %w", targetPath, err)
		}
	}
	return nil
}

func skillDirectoryName(stored *gatewaymanagedagents.StoredSkillVersion, fallback string) string {
	if stored != nil {
		if directory := sanitizeName(stored.Snapshot.Directory); directory != "" {
			return directory
		}
	}
	return sanitizeName(fallback)
}

func skillFileTargetPath(workingDirectory, directory, storedPath string) string {
	base := cleanMountPath(path.Join(strings.TrimSpace(workingDirectory), ".claude", "skills"))
	if base == "" {
		return ""
	}
	relative := normalizedStoredSkillRelativePath(directory, storedPath)
	if relative == "" {
		return ""
	}
	return cleanMountPath(path.Join(base, relative))
}

func normalizedStoredSkillRelativePath(directory, storedPath string) string {
	cleanDirectory := sanitizeName(directory)
	cleanPath := path.Clean(strings.TrimSpace(strings.TrimPrefix(storedPath, "/")))
	if cleanDirectory == "" || cleanPath == "." || cleanPath == "" || strings.HasPrefix(cleanPath, "../") {
		return ""
	}
	if cleanPath == cleanDirectory || strings.HasPrefix(cleanPath, cleanDirectory+"/") {
		return cleanPath
	}
	return path.Join(cleanDirectory, cleanPath)
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
