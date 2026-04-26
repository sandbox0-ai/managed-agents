package managedagents

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"
)

type validatedSessionResource struct {
	Public map[string]any
	Secret map[string]any
}

type WorkspaceBasePreparer interface {
	PrepareWorkspaceBase(ctx context.Context, teamID string, agent Agent, workingDirectory string) (*WorkspaceBaseRecord, error)
}

const workspaceBasePreparationTimeout = 2 * time.Minute

func (s *Service) CreateAgent(ctx context.Context, principal Principal, req CreateAgentRequest) (*Agent, error) {
	if strings.TrimSpace(principal.TeamID) == "" {
		return nil, errors.New("team id is required")
	}
	if req.Model == nil {
		return nil, errors.New("model is required")
	}
	name, err := normalizeRequiredText(req.Name, "name", 256)
	if err != nil {
		return nil, err
	}
	vendor, model, err := normalizeAgentModel(req.Model, "")
	if err != nil {
		return nil, err
	}
	description, err := normalizeOptionalText(req.Description, "description", 2048)
	if err != nil {
		return nil, err
	}
	system, err := normalizeOptionalText(req.System, "system", 100000)
	if err != nil {
		return nil, err
	}
	tools, err := normalizeAgentTools(req.Tools)
	if err != nil {
		return nil, err
	}
	mcpServers, mcpServerNames, err := normalizeMCPServers(req.MCPServers)
	if err != nil {
		return nil, err
	}
	if err := validateToolReferences(tools, mcpServerNames); err != nil {
		return nil, err
	}
	skills, err := s.normalizeAgentSkills(ctx, principal, req.Skills)
	if err != nil {
		return nil, err
	}
	metadata, err := normalizeAgentMetadata(req.Metadata)
	if err != nil {
		return nil, err
	}
	if err := ValidateManagedAgentMetadata(metadata); err != nil {
		return nil, err
	}
	req.Name = name
	req.Model = model
	req.Description = description
	req.System = system
	req.Tools = tools
	req.MCPServers = mcpServers
	req.Skills = skills
	req.Metadata = metadata
	now := time.Now().UTC()
	agent := buildAgentObject(NewID("agent"), 1, vendor, req, now, nil)
	if err := s.repo.CreateAgent(ctx, principal.TeamID, vendor, 1, agent, now); err != nil {
		return nil, err
	}
	s.prepareWorkspaceBaseAsync(ctx, principal.TeamID, agent)
	return &agent, nil
}

func (s *Service) ListAgents(ctx context.Context, principal Principal, opts AgentListOptions) ([]Agent, *string, error) {
	return s.repo.ListAgents(ctx, principal.TeamID, opts)
}

func (s *Service) GetAgent(ctx context.Context, principal Principal, agentID string, version int) (*Agent, error) {
	agent, _, err := s.repo.GetAgent(ctx, principal.TeamID, agentID, version)
	return agent, err
}

func (s *Service) UpdateAgent(ctx context.Context, principal Principal, agentID string, req UpdateAgentRequest) (*Agent, error) {
	agent, vendor, err := s.repo.GetAgent(ctx, principal.TeamID, agentID, 0)
	if err != nil {
		return nil, err
	}
	if req.Version <= 0 {
		return nil, errors.New("version is required")
	}
	if req.Version != agent.Version {
		return nil, errors.New("invalid version")
	}
	if req.Name.Set {
		trimmed, err := normalizeRequiredText(req.Name.Value, "name", 256)
		if err != nil {
			return nil, err
		}
		agent.Name = trimmed
	}
	if req.Description.Set {
		description, err := normalizeOptionalText(req.Description.Value, "description", 2048)
		if err != nil {
			return nil, err
		}
		agent.Description = description
	}
	if req.System.Set {
		system, err := normalizeOptionalText(req.System.Value, "system", 100000)
		if err != nil {
			return nil, err
		}
		agent.System = system
	}
	if req.Model.Set {
		var model map[string]any
		vendor, model, err = normalizeAgentModel(req.Model.Value, vendor)
		if err != nil {
			return nil, err
		}
		agent.Model = modelConfigFromMap(model)
	}
	tools := valueToJSONArray(agent.Tools)
	if req.Tools.Set {
		tools, err = normalizeAgentTools(req.Tools.Values)
		if err != nil {
			return nil, err
		}
		agent.Tools = agentToolsFromAny(tools)
	}
	mcpServers := valueToJSONArray(agent.MCPServers)
	if req.MCPServers.Set {
		mcpServers, _, err = normalizeMCPServers(req.MCPServers.Values)
		if err != nil {
			return nil, err
		}
		agent.MCPServers = mcpServersFromAny(mcpServers)
	}
	if err := validateToolReferences(tools, mcpServerNames(mcpServers)); err != nil {
		return nil, err
	}
	if req.Skills.Set {
		skills, err := s.normalizeAgentSkills(ctx, principal, req.Skills.Values)
		if err != nil {
			return nil, err
		}
		agent.Skills = agentSkillsFromAny(skills)
	}
	if req.Metadata.Set {
		metadata, err := mergeAgentMetadata(agent.Metadata, req.Metadata)
		if err != nil {
			return nil, err
		}
		if err := ValidateManagedAgentMetadata(metadata); err != nil {
			return nil, err
		}
		agent.Metadata = metadata
	}
	version := agent.Version + 1
	if version <= 1 {
		version = 2
	}
	updatedAt := time.Now().UTC()
	agent.Version = version
	agent.UpdatedAt = nowRFC3339(updatedAt)
	if err := s.repo.UpdateAgent(ctx, principal.TeamID, agentID, vendor, req.Version, version, agent, parseTimestampPointer(agent.ArchivedAt), updatedAt); err != nil {
		return nil, err
	}
	s.prepareWorkspaceBaseAsync(ctx, principal.TeamID, *agent)
	return agent, nil
}

func (s *Service) prepareWorkspaceBaseAsync(ctx context.Context, teamID string, agent Agent) {
	preparer, ok := s.runtime.(WorkspaceBasePreparer)
	if !ok {
		return
	}
	teamID = strings.TrimSpace(teamID)
	go func() {
		prepareCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workspaceBasePreparationTimeout)
		defer cancel()
		if _, err := preparer.PrepareWorkspaceBase(prepareCtx, teamID, agent, "/workspace"); err != nil {
			s.logger.Warn("prepare workspace base failed",
				zap.Error(err),
				zap.String("team_id", teamID),
				zap.String("agent_id", agent.ID),
				zap.Int("agent_version", agent.Version),
			)
		}
	}()
}

func (s *Service) ArchiveAgent(ctx context.Context, principal Principal, agentID string) (*Agent, error) {
	agent, vendor, err := s.repo.GetAgent(ctx, principal.TeamID, agentID, 0)
	if err != nil {
		return nil, err
	}
	version := agent.Version
	if version <= 0 {
		version = 1
	}
	now := time.Now().UTC()
	agent.ArchivedAt = timestampPointerOrNil(&now)
	agent.UpdatedAt = nowRFC3339(now)
	if err := s.repo.UpdateAgent(ctx, principal.TeamID, agentID, vendor, version, version, agent, &now, now); err != nil {
		return nil, err
	}
	return agent, nil
}

func (s *Service) ListAgentVersions(ctx context.Context, principal Principal, agentID string, limit int, page string) ([]Agent, *string, error) {
	return s.repo.ListAgentVersions(ctx, principal.TeamID, agentID, limit, page)
}

func (s *Service) CreateEnvironment(ctx context.Context, principal Principal, credential RequestCredential, req CreateEnvironmentRequest) (*Environment, error) {
	name, err := normalizeRequiredText(req.Name, "name", 256)
	if err != nil {
		return nil, err
	}
	description, err := normalizeOptionalText(req.Description, "description", 1024)
	if err != nil {
		return nil, err
	}
	metadata, err := normalizeMetadataMap(req.Metadata, 0, 0, 0)
	if err != nil {
		return nil, err
	}
	if err := ValidateManagedEnvironmentMetadata(metadata); err != nil {
		return nil, err
	}
	config, err := normalizeCreateEnvironmentConfig(req.Config)
	if err != nil {
		return nil, err
	}
	req.Name = name
	req.Description = description
	req.Metadata = metadata
	req.Config = config
	exists, err := s.repo.EnvironmentNameExists(ctx, principal.TeamID, req.Name, "")
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrEnvironmentNameConflict
	}
	now := time.Now().UTC()
	environment := buildEnvironmentObject(NewID("env"), req, now, nil)
	prepared, err := s.prepareReadyEnvironmentArtifact(ctx, credential, principal.TeamID, &environment)
	if err != nil {
		return nil, err
	}
	persisted := false
	defer func() {
		if !persisted && prepared != nil && prepared.cleanup != nil {
			prepared.cleanup(context.WithoutCancel(ctx))
		}
	}()
	if err := s.repo.WithTransaction(ctx, func(ctx context.Context) error {
		exists, err := s.repo.EnvironmentNameExists(ctx, principal.TeamID, req.Name, "")
		if err != nil {
			return err
		}
		if exists {
			return ErrEnvironmentNameConflict
		}
		if err := s.repo.CreateEnvironment(ctx, principal.TeamID, environment, nil, now); err != nil {
			return err
		}
		return prepared.persist(ctx)
	}); err != nil {
		return nil, err
	}
	persisted = true
	return &environment, nil
}

func (s *Service) ListEnvironments(ctx context.Context, principal Principal, limit int, page string, includeArchived bool) ([]Environment, *string, error) {
	return s.repo.ListEnvironments(ctx, principal.TeamID, limit, page, includeArchived)
}

func (s *Service) GetEnvironment(ctx context.Context, principal Principal, environmentID string) (*Environment, error) {
	return s.repo.GetEnvironment(ctx, principal.TeamID, environmentID)
}

func (s *Service) UpdateEnvironment(ctx context.Context, principal Principal, credential RequestCredential, environmentID string, req UpdateEnvironmentRequest) (*Environment, error) {
	environment, err := s.repo.GetEnvironment(ctx, principal.TeamID, environmentID)
	if err != nil {
		return nil, err
	}
	if req.Name != nil {
		trimmed, err := normalizeRequiredText(*req.Name, "name", 256)
		if err != nil {
			return nil, err
		}
		exists, err := s.repo.EnvironmentNameExists(ctx, principal.TeamID, trimmed, environmentID)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, ErrEnvironmentNameConflict
		}
		environment.Name = trimmed
	}
	if req.Description.Set {
		description, err := normalizeOptionalText(req.Description.Value, "description", 1024)
		if err != nil {
			return nil, err
		}
		if description == nil {
			environment.Description = ""
		} else {
			environment.Description = *description
		}
	}
	if req.Config != nil {
		config, err := normalizeUpdateEnvironmentConfig(environmentConfigToMap(environment.Config), req.Config)
		if err != nil {
			return nil, err
		}
		environment.Config = environmentConfigFromMap(config)
	}
	if req.Metadata.Set {
		metadata, err := mergeMetadataPatch(environment.Metadata, req.Metadata, 0, 0, 0)
		if err != nil {
			return nil, err
		}
		if err := ValidateManagedEnvironmentMetadata(metadata); err != nil {
			return nil, err
		}
		environment.Metadata = metadata
	}
	now := time.Now().UTC()
	environment.UpdatedAt = nowRFC3339(now)
	prepared, err := s.prepareReadyEnvironmentArtifact(ctx, credential, principal.TeamID, environment)
	if err != nil {
		return nil, err
	}
	persisted := false
	defer func() {
		if !persisted && prepared != nil && prepared.cleanup != nil {
			prepared.cleanup(context.WithoutCancel(ctx))
		}
	}()
	if err := s.repo.WithTransaction(ctx, func(ctx context.Context) error {
		if err := s.repo.UpdateEnvironment(ctx, principal.TeamID, environmentID, environment, parseTimestampPointer(environment.ArchivedAt), now); err != nil {
			return err
		}
		return prepared.persist(ctx)
	}); err != nil {
		return nil, err
	}
	persisted = true
	return environment, nil
}

func (s *Service) DeleteEnvironment(ctx context.Context, principal Principal, credential RequestCredential, environmentID string) (map[string]any, error) {
	count, err := s.repo.CountActiveSessionsForEnvironment(ctx, principal.TeamID, environmentID)
	if err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, ErrEnvironmentInUse
	}
	if cleaner, ok := s.runtime.(EnvironmentArtifactCleaner); ok && cleaner != nil {
		if err := cleaner.CleanupEnvironmentArtifacts(ctx, credential, principal.TeamID, environmentID); err != nil {
			return nil, err
		}
	}
	if err := s.repo.DeleteEnvironment(ctx, principal.TeamID, environmentID); err != nil {
		return nil, err
	}
	return deletedObject("environment_deleted", environmentID), nil
}

func (s *Service) ArchiveEnvironment(ctx context.Context, principal Principal, environmentID string) (*Environment, error) {
	environment, err := s.repo.GetEnvironment(ctx, principal.TeamID, environmentID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	environment.ArchivedAt = timestampPointerOrNil(&now)
	environment.UpdatedAt = nowRFC3339(now)
	if err := s.repo.UpdateEnvironment(ctx, principal.TeamID, environmentID, environment, &now, now); err != nil {
		return nil, err
	}
	return environment, nil
}

func (s *Service) CreateVault(ctx context.Context, principal Principal, req CreateVaultRequest) (*Vault, error) {
	displayName, err := normalizeRequiredText(req.DisplayName, "display_name", 255)
	if err != nil {
		return nil, err
	}
	metadata, err := normalizeMetadataMap(req.Metadata, 16, 64, 512)
	if err != nil {
		return nil, err
	}
	if err := ValidateManagedVaultMetadata(metadata); err != nil {
		return nil, err
	}
	req.DisplayName = displayName
	req.Metadata = metadata
	now := time.Now().UTC()
	vault := buildVaultObject(NewID("vlt"), req, now, nil)
	if err := s.repo.CreateVault(ctx, principal.TeamID, vault, nil, now); err != nil {
		return nil, err
	}
	return &vault, nil
}

func (s *Service) ListVaults(ctx context.Context, principal Principal, limit int, page string, includeArchived bool) ([]Vault, *string, error) {
	return s.repo.ListVaults(ctx, principal.TeamID, limit, page, includeArchived)
}

func (s *Service) GetVault(ctx context.Context, principal Principal, vaultID string) (*Vault, error) {
	return s.repo.GetVault(ctx, principal.TeamID, vaultID)
}

func (s *Service) UpdateVault(ctx context.Context, principal Principal, vaultID string, req UpdateVaultRequest) (*Vault, error) {
	vault, err := s.repo.GetVault(ctx, principal.TeamID, vaultID)
	if err != nil {
		return nil, err
	}
	if req.DisplayName.Set {
		displayName, err := normalizeOptionalText(req.DisplayName.Value, "display_name", 255)
		if err != nil {
			return nil, err
		}
		if displayName == nil {
			vault.DisplayName = ""
		} else {
			vault.DisplayName = *displayName
		}
	}
	if req.Metadata.Set {
		metadata, err := mergeMetadataPatch(vault.Metadata, req.Metadata, 16, 64, 512)
		if err != nil {
			return nil, err
		}
		if err := ValidateManagedVaultMetadata(metadata); err != nil {
			return nil, err
		}
		vault.Metadata = metadata
	}
	now := time.Now().UTC()
	vault.UpdatedAt = nowRFC3339(now)
	if err := s.repo.UpdateVault(ctx, principal.TeamID, vaultID, vault, parseTimestampPointer(vault.ArchivedAt), now); err != nil {
		return nil, err
	}
	return vault, nil
}

func (s *Service) DeleteVault(ctx context.Context, principal Principal, vaultID string) (map[string]any, error) {
	count, err := s.repo.CountActiveSessionsForVault(ctx, principal.TeamID, vaultID)
	if err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, ErrVaultInUse
	}
	if err := s.repo.DeleteVault(ctx, principal.TeamID, vaultID); err != nil {
		return nil, err
	}
	return deletedObject("vault_deleted", vaultID), nil
}

func (s *Service) ArchiveVault(ctx context.Context, principal Principal, vaultID string) (*Vault, error) {
	vault, err := s.repo.GetVault(ctx, principal.TeamID, vaultID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	activeCredentials, err := s.repo.ListActiveCredentialsForVault(ctx, principal.TeamID, vaultID)
	if err != nil {
		return nil, err
	}
	for _, stored := range activeCredentials {
		credential := stored.Snapshot
		credential.ArchivedAt = timestampPointerOrNil(&now)
		credential.UpdatedAt = nowRFC3339(now)
		if err := s.repo.UpdateCredential(ctx, principal.TeamID, vaultID, credential.ID, &credential, map[string]any{}, &now, now); err != nil {
			return nil, err
		}
	}
	vault.ArchivedAt = timestampPointerOrNil(&now)
	vault.UpdatedAt = nowRFC3339(now)
	if err := s.repo.UpdateVault(ctx, principal.TeamID, vaultID, vault, &now, now); err != nil {
		return nil, err
	}
	return vault, nil
}

func (s *Service) CreateCredential(ctx context.Context, principal Principal, vaultID string, req CreateCredentialRequest) (*Credential, error) {
	if len(req.Auth) == 0 {
		return nil, errors.New("auth is required")
	}
	vault, err := s.repo.GetVault(ctx, principal.TeamID, vaultID)
	if err != nil {
		return nil, err
	}
	if err := requireActiveVault(vault); err != nil {
		return nil, err
	}
	displayName, err := normalizeOptionalText(req.DisplayName, "display_name", 255)
	if err != nil {
		return nil, err
	}
	metadata, err := normalizeMetadataMap(req.Metadata, 16, 64, 512)
	if err != nil {
		return nil, err
	}
	if err := ValidateManagedCredentialMetadata(metadata); err != nil {
		return nil, err
	}
	normalizedAuth, err := normalizeCreateCredentialAuth(req.Auth)
	if err != nil {
		return nil, err
	}
	activeCredentials, err := s.repo.ListActiveCredentialsForVault(ctx, principal.TeamID, vaultID)
	if err != nil {
		return nil, err
	}
	if len(activeCredentials) >= 20 {
		return nil, errors.New("vault supports at most 20 active credentials")
	}
	if err := validateUniqueActiveCredentialURL(activeCredentials, stringValue(normalizedAuth.Public["mcp_server_url"])); err != nil {
		return nil, err
	}
	req.DisplayName = displayName
	req.Metadata = metadata
	now := time.Now().UTC()
	credential := buildCredentialObject(NewID("vcrd"), vaultID, req, now, nil)
	credential.Auth = credentialAuthFromMap(normalizedAuth.Public)
	if err := s.repo.CreateCredential(ctx, principal.TeamID, vaultID, credential, normalizedAuth.Secret, nil, now); err != nil {
		return nil, err
	}
	return &credential, nil
}

func (s *Service) ListCredentials(ctx context.Context, principal Principal, vaultID string, limit int, page string, includeArchived bool) ([]Credential, *string, error) {
	if _, err := s.repo.GetVault(ctx, principal.TeamID, vaultID); err != nil {
		return nil, nil, err
	}
	return s.repo.ListCredentials(ctx, principal.TeamID, vaultID, limit, page, includeArchived)
}

func (s *Service) GetCredential(ctx context.Context, principal Principal, vaultID, credentialID string) (*Credential, error) {
	credential, _, err := s.repo.GetCredential(ctx, principal.TeamID, vaultID, credentialID)
	return credential, err
}

func (s *Service) UpdateCredential(ctx context.Context, principal Principal, vaultID, credentialID string, req UpdateCredentialRequest) (*Credential, error) {
	credential, secret, err := s.repo.GetCredential(ctx, principal.TeamID, vaultID, credentialID)
	if err != nil {
		return nil, err
	}
	if req.DisplayName.Set {
		displayName, err := normalizeOptionalText(req.DisplayName.Value, "display_name", 255)
		if err != nil {
			return nil, err
		}
		credential.DisplayName = displayName
	}
	if req.Metadata.Set {
		metadata, err := mergeMetadataPatch(credential.Metadata, req.Metadata, 16, 64, 512)
		if err != nil {
			return nil, err
		}
		if err := ValidateManagedCredentialMetadata(metadata); err != nil {
			return nil, err
		}
		credential.Metadata = metadata
	}
	if req.Auth != nil {
		normalizedAuth, err := normalizeUpdateCredentialAuth(credentialAuthToMap(credential.Auth), secret, req.Auth)
		if err != nil {
			return nil, err
		}
		secret = normalizedAuth.Secret
		credential.Auth = credentialAuthFromMap(normalizedAuth.Public)
	}
	now := time.Now().UTC()
	credential.UpdatedAt = nowRFC3339(now)
	if err := s.repo.UpdateCredential(ctx, principal.TeamID, vaultID, credentialID, credential, secret, parseTimestampPointer(credential.ArchivedAt), now); err != nil {
		return nil, err
	}
	return credential, nil
}

func (s *Service) DeleteCredential(ctx context.Context, principal Principal, vaultID, credentialID string) (map[string]any, error) {
	if _, _, err := s.repo.GetCredential(ctx, principal.TeamID, vaultID, credentialID); err != nil {
		return nil, err
	}
	count, err := s.repo.CountActiveSessionsForVault(ctx, principal.TeamID, vaultID)
	if err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, ErrVaultInUse
	}
	if err := s.repo.DeleteCredential(ctx, principal.TeamID, vaultID, credentialID); err != nil {
		return nil, err
	}
	return deletedObject("vault_credential_deleted", credentialID), nil
}

func (s *Service) ArchiveCredential(ctx context.Context, principal Principal, vaultID, credentialID string) (*Credential, error) {
	credential, _, err := s.repo.GetCredential(ctx, principal.TeamID, vaultID, credentialID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	credential.ArchivedAt = timestampPointerOrNil(&now)
	credential.UpdatedAt = nowRFC3339(now)
	if err := s.repo.UpdateCredential(ctx, principal.TeamID, vaultID, credentialID, credential, map[string]any{}, &now, now); err != nil {
		return nil, err
	}
	return credential, nil
}

func (s *Service) UploadFile(ctx context.Context, principal Principal, credential RequestCredential, filename, mimeType string, content io.Reader) (FileMetadata, error) {
	if s.assetStore == nil {
		return FileMetadata{}, errors.New("asset store is not configured")
	}
	trimmedName := strings.TrimSpace(filename)
	if trimmedName == "" {
		return FileMetadata{}, errors.New("filename is required")
	}
	resolvedMimeType := strings.TrimSpace(mimeType)
	if resolvedMimeType == "" {
		resolvedMimeType = mime.TypeByExtension(filepath.Ext(trimmedName))
	}
	if resolvedMimeType == "" {
		resolvedMimeType = "application/octet-stream"
	}
	now := time.Now().UTC()
	fileID := NewID("file")
	store, err := s.ensureTeamAssetStore(ctx, credential, principal.TeamID)
	if err != nil {
		return FileMetadata{}, err
	}
	storePath := teamFileAssetStorePath(fileID)
	stored, err := s.assetStore.PutObject(ctx, credential, AssetStorePutObjectRequest{
		TeamID:   principal.TeamID,
		RegionID: store.RegionID,
		VolumeID: store.VolumeID,
		Path:     storePath,
		Content:  content,
	})
	if err != nil {
		return FileMetadata{}, err
	}
	record := &managedFileRecord{
		ID:        fileID,
		TeamID:    principal.TeamID,
		Filename:  trimmedName,
		MimeType:  resolvedMimeType,
		SizeBytes: stored.SizeBytes,
		StorePath: stored.Path,
		SHA256:    stored.SHA256,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.repo.CreateFile(ctx, record); err != nil {
		_ = s.assetStore.DeleteObject(ctx, credential, AssetStoreDeleteObjectRequest{
			TeamID:   principal.TeamID,
			RegionID: store.RegionID,
			VolumeID: store.VolumeID,
			Path:     stored.Path,
		})
		return FileMetadata{}, err
	}
	return buildFileObject(record), nil
}

func (s *Service) ListFiles(ctx context.Context, principal Principal, opts FileListOptions) (FileListResponse, error) {
	files, hasMore, err := s.repo.ListFiles(ctx, principal.TeamID, opts)
	if err != nil {
		return FileListResponse{}, err
	}
	var firstID *string
	var lastID *string
	if len(files) > 0 {
		firstID = &files[0].ID
		lastID = &files[len(files)-1].ID
	}
	return FileListResponse{Data: files, FirstID: firstID, LastID: lastID, HasMore: hasMore}, nil
}

func (s *Service) GetFileMetadata(ctx context.Context, principal Principal, fileID string) (FileMetadata, error) {
	record, err := s.repo.GetFile(ctx, principal.TeamID, fileID)
	if err != nil {
		return FileMetadata{}, err
	}
	return buildFileObject(record), nil
}

func (s *Service) GetFileContent(ctx context.Context, principal Principal, credential RequestCredential, fileID string) (*managedFileRecord, error) {
	record, err := s.repo.GetFile(ctx, principal.TeamID, fileID)
	if err != nil {
		return nil, err
	}
	content, err := s.readFileContent(ctx, credential, record)
	if err != nil {
		return nil, err
	}
	record.Content = content
	return record, nil
}

func (s *Service) DeleteFile(ctx context.Context, principal Principal, credential RequestCredential, fileID string) (map[string]any, error) {
	record, err := s.repo.GetFile(ctx, principal.TeamID, fileID)
	if err != nil {
		return nil, err
	}
	if s.assetStore == nil {
		return nil, errors.New("asset store is not configured")
	}
	store, err := s.getTeamAssetStore(ctx, credential, principal.TeamID)
	if err != nil {
		return nil, err
	}
	if err := s.assetStore.DeleteObject(ctx, credential, AssetStoreDeleteObjectRequest{
		TeamID:   principal.TeamID,
		RegionID: store.RegionID,
		VolumeID: store.VolumeID,
		Path:     record.StorePath,
	}); err != nil {
		return nil, err
	}
	if err := s.repo.DeleteFile(ctx, principal.TeamID, fileID); err != nil {
		return nil, err
	}
	return deletedObject("file_deleted", fileID), nil
}

func (s *Service) readFileContent(ctx context.Context, credential RequestCredential, record *managedFileRecord) ([]byte, error) {
	if record == nil {
		return nil, ErrFileNotFound
	}
	if s.assetStore == nil {
		return nil, errors.New("asset store is not configured")
	}
	store, err := s.getTeamAssetStore(ctx, credential, record.TeamID)
	if err != nil {
		return nil, err
	}
	return s.assetStore.ReadObject(ctx, credential, AssetStoreReadObjectRequest{
		TeamID:   record.TeamID,
		RegionID: store.RegionID,
		VolumeID: store.VolumeID,
		Path:     record.StorePath,
	})
}

func (s *Service) ArchiveSession(ctx context.Context, principal Principal, sessionID string) (*Session, error) {
	var archived *Session
	err := s.repo.WithSessionLock(ctx, sessionID, func(ctx context.Context) error {
		var err error
		archived, err = s.archiveSessionLocked(ctx, principal, sessionID)
		return err
	})
	return archived, err
}

func (s *Service) archiveSessionLocked(ctx context.Context, principal Principal, sessionID string) (*Session, error) {
	record, engine, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := ensureSessionAccess(record, principal); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	archivedEvent := stampEvent(map[string]any{"type": "session.archived"}, now)
	if err := s.repo.AppendEvents(ctx, sessionID, []map[string]any{archivedEvent}); err != nil {
		return nil, err
	}
	record = applySessionBatch(record, now, Usage{}, []map[string]any{archivedEvent})
	if err := s.repo.UpdateSession(ctx, record, engine); err != nil {
		return nil, err
	}
	return record.toAPI(now), nil
}

func (s *Service) ListSessionResources(ctx context.Context, principal Principal, sessionID string, limit int, page string) ([]map[string]any, *string, error) {
	record, _, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}
	if err := ensureSessionAccess(record, principal); err != nil {
		return nil, nil, err
	}
	resources := cloneMapSlice(record.Resources)
	sort.SliceStable(resources, func(i, j int) bool {
		leftTime := parseResourceCreatedAt(resources[i])
		rightTime := parseResourceCreatedAt(resources[j])
		if leftTime.Equal(rightTime) {
			return stringValue(resources[i]["id"]) < stringValue(resources[j]["id"])
		}
		return leftTime.Before(rightTime)
	})
	cursor, err := decodePageCursor(page)
	if err != nil {
		return nil, nil, err
	}
	if cursor != nil {
		cursorTime, _ := time.Parse(time.RFC3339, cursor.CreatedAt)
		filtered := make([]map[string]any, 0, len(resources))
		for _, resource := range resources {
			resourceTime := parseResourceCreatedAt(resource)
			resourceID := stringValue(resource["id"])
			if resourceTime.After(cursorTime) || (resourceTime.Equal(cursorTime) && resourceID > cursor.ID) {
				filtered = append(filtered, resource)
			}
		}
		resources = filtered
	}
	if limit <= 0 || limit > 1000 {
		limit = len(resources)
	}
	var nextPage *string
	if limit < len(resources) {
		last := resources[limit-1]
		nextPage = encodePageCursor(parseResourceCreatedAt(last), stringValue(last["id"]))
		resources = resources[:limit]
	}
	return resources, nextPage, nil
}

func (s *Service) AddSessionResource(ctx context.Context, principal Principal, sessionID string, req AddSessionResourceRequest) (map[string]any, error) {
	var resource map[string]any
	err := s.repo.WithSessionLock(ctx, sessionID, func(ctx context.Context) error {
		var err error
		resource, err = s.addSessionResourceLocked(ctx, principal, sessionID, req)
		return err
	})
	return resource, err
}

func (s *Service) addSessionResourceLocked(ctx context.Context, principal Principal, sessionID string, req AddSessionResourceRequest) (map[string]any, error) {
	record, engine, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := ensureSessionAccess(record, principal); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	validated, err := s.validateAndNormalizeResource(ctx, principal, normalizeSessionResource(req, now), false)
	if err != nil {
		return nil, err
	}
	record.Resources = append(cloneMapSlice(record.Resources), validated.Public)
	record.UpdatedAt = now
	if err := s.repo.UpdateSession(ctx, record, engine); err != nil {
		return nil, err
	}
	return validated.Public, nil
}

func (s *Service) GetSessionResource(ctx context.Context, principal Principal, sessionID, resourceID string) (map[string]any, error) {
	record, _, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := ensureSessionAccess(record, principal); err != nil {
		return nil, err
	}
	resource, _, found := findResource(record.Resources, resourceID)
	if !found {
		return nil, ErrResourceNotFound
	}
	return cloneMap(resource), nil
}

func (s *Service) UpdateSessionResource(ctx context.Context, principal Principal, sessionID, resourceID string, req UpdateSessionResourceRequest) (map[string]any, error) {
	var resource map[string]any
	err := s.repo.WithSessionLock(ctx, sessionID, func(ctx context.Context) error {
		var err error
		resource, err = s.updateSessionResourceLocked(ctx, principal, sessionID, resourceID, req)
		return err
	})
	return resource, err
}

func (s *Service) updateSessionResourceLocked(ctx context.Context, principal Principal, sessionID, resourceID string, req UpdateSessionResourceRequest) (map[string]any, error) {
	record, engine, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := ensureSessionAccess(record, principal); err != nil {
		return nil, err
	}
	current, index, found := findResource(record.Resources, resourceID)
	if !found {
		return nil, ErrResourceNotFound
	}
	if stringValue(current["type"]) != "github_repository" {
		return nil, errors.New("only github_repository resources support updates")
	}
	if err := validateAllowedFields(map[string]any(req), []string{"authorization_token"}); err != nil {
		return nil, err
	}
	authorizationToken := strings.TrimSpace(stringValue(req["authorization_token"]))
	if authorizationToken == "" {
		return nil, errors.New("authorization_token is required")
	}
	now := time.Now().UTC()
	updated := cloneMap(current)
	updated["updated_at"] = nowRFC3339(now)
	if err := s.repo.UpsertSessionResourceSecret(ctx, sessionID, resourceID, map[string]any{"authorization_token": authorizationToken}); err != nil {
		return nil, err
	}
	resources := cloneMapSlice(record.Resources)
	resources[index] = updated
	record.Resources = resources
	record.UpdatedAt = now
	if err := s.repo.UpdateSession(ctx, record, engine); err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *Service) DeleteSessionResource(ctx context.Context, principal Principal, sessionID, resourceID string) (map[string]any, error) {
	var deleted map[string]any
	err := s.repo.WithSessionLock(ctx, sessionID, func(ctx context.Context) error {
		var err error
		deleted, err = s.deleteSessionResourceLocked(ctx, principal, sessionID, resourceID)
		return err
	})
	return deleted, err
}

func (s *Service) deleteSessionResourceLocked(ctx context.Context, principal Principal, sessionID, resourceID string) (map[string]any, error) {
	record, engine, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := ensureSessionAccess(record, principal); err != nil {
		return nil, err
	}
	_, index, found := findResource(record.Resources, resourceID)
	if !found {
		return nil, ErrResourceNotFound
	}
	resources := cloneMapSlice(record.Resources)
	record.Resources = append(resources[:index], resources[index+1:]...)
	record.UpdatedAt = time.Now().UTC()
	if err := s.repo.UpdateSession(ctx, record, engine); err != nil {
		return nil, err
	}
	if err := s.repo.DeleteSessionResourceSecret(ctx, sessionID, resourceID); err != nil {
		return nil, err
	}
	return deletedObject("session_resource_deleted", resourceID), nil
}

func (s *Service) validateAndNormalizeResource(ctx context.Context, principal Principal, resource map[string]any, allowGitHub bool) (*validatedSessionResource, error) {
	resourceType := strings.TrimSpace(stringValue(resource["type"]))
	switch resourceType {
	case "file":
		if err := validateAllowedFields(resource, []string{"type", "id", "file_id", "mount_path", "created_at", "updated_at"}); err != nil {
			return nil, err
		}
		fileID := strings.TrimSpace(stringValue(resource["file_id"]))
		if fileID == "" {
			return nil, errors.New("file_id is required")
		}
		if _, err := s.repo.GetFile(ctx, principal.TeamID, fileID); err != nil {
			return nil, err
		}
		resource["file_id"] = fileID
		resource["mount_path"] = defaultFileMountPath(fileID, stringValue(resource["mount_path"]))
		return &validatedSessionResource{Public: resource}, nil
	case "github_repository":
		if !allowGitHub {
			return nil, errors.New("only file resources can be added after session creation")
		}
		if err := validateAllowedFields(resource, []string{"type", "id", "url", "authorization_token", "mount_path", "checkout", "created_at", "updated_at"}); err != nil {
			return nil, err
		}
		repoURL := strings.TrimSpace(stringValue(resource["url"]))
		if repoURL == "" {
			return nil, errors.New("url is required")
		}
		authorizationToken := strings.TrimSpace(stringValue(resource["authorization_token"]))
		if authorizationToken == "" {
			return nil, errors.New("authorization_token is required")
		}
		parsedURL, err := url.Parse(repoURL)
		if err != nil || !strings.EqualFold(parsedURL.Hostname(), "github.com") {
			return nil, errors.New("url must be a valid github repository url")
		}
		repoName := repositoryNameFromURL(parsedURL)
		if repoName == "" {
			return nil, errors.New("url must point to a github repository")
		}
		checkout, err := normalizeRepositoryCheckout(resource["checkout"])
		if err != nil {
			return nil, err
		}
		resource["url"] = repoURL
		resource["mount_path"] = defaultGitHubMountPath(repoName, stringValue(resource["mount_path"]))
		if checkout == nil {
			delete(resource, "checkout")
		} else {
			resource["checkout"] = checkout
		}
		delete(resource, "authorization_token")
		return &validatedSessionResource{
			Public: resource,
			Secret: map[string]any{"authorization_token": authorizationToken},
		}, nil
	default:
		return nil, errors.New("invalid resource type")
	}
}

func ensureEnvironmentConfig(config map[string]any) map[string]any {
	if len(config) != 0 {
		return cloneMap(config)
	}
	return defaultEnvironmentConfig()
}

func mapFromValue(value any) map[string]string {
	if value == nil {
		return map[string]string{}
	}
	result := map[string]string{}
	switch typed := value.(type) {
	case map[string]string:
		return cloneStringMap(typed)
	case map[string]any:
		for key, entry := range typed {
			if text, ok := entry.(string); ok {
				result[key] = text
			}
		}
	}
	return result
}

func mergeNullableMetadata(existing map[string]string, updates map[string]*string) map[string]string {
	out := cloneStringMap(existing)
	for key, value := range updates {
		if value == nil || strings.TrimSpace(*value) == "" {
			delete(out, key)
			continue
		}
		out[key] = strings.TrimSpace(*value)
	}
	return out
}

func findResource(resources []map[string]any, resourceID string) (map[string]any, int, bool) {
	for index, resource := range resources {
		if strings.TrimSpace(stringValue(resource["id"])) == strings.TrimSpace(resourceID) {
			return resource, index, true
		}
	}
	return nil, -1, false
}

func parseResourceCreatedAt(resource map[string]any) time.Time {
	text := stringValue(resource["created_at"])
	if text == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func deletedObject(objectType, id string) map[string]any {
	return map[string]any{"type": objectType, "id": strings.TrimSpace(id)}
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func timePointerFromValue(value any) *time.Time {
	text := strings.TrimSpace(stringValue(value))
	if text == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return nil
	}
	return &parsed
}

func (s *Service) resolveSessionAgentReference(ctx context.Context, principal Principal, input any) (string, map[string]any, error) {
	switch value := input.(type) {
	case string:
		agentID, err := normalizeRequiredText(value, "agent", 128)
		if err != nil {
			return "", nil, err
		}
		agent, vendor, err := s.repo.GetAgent(ctx, principal.TeamID, agentID, 0)
		if err != nil {
			return "", nil, err
		}
		if err := ensureSupportedVendor(vendor); err != nil {
			return "", nil, err
		}
		return vendor, agentToSnapshot(*agent), nil
	case map[string]any:
		if err := validateAllowedFields(value, []string{"type", "id", "version"}); err != nil {
			return "", nil, err
		}
		if strings.TrimSpace(stringValue(value["type"])) != "agent" {
			return "", nil, errors.New("agent.type must be agent")
		}
		agentID, err := normalizeRequiredText(stringValue(value["id"]), "agent.id", 128)
		if err != nil {
			return "", nil, err
		}
		version := intValue(value["version"])
		if rawVersion, exists := value["version"]; exists {
			switch rawVersion.(type) {
			case int, int32, int64, float64:
			default:
				return "", nil, errors.New("agent.version must be an integer")
			}
			if version < 1 {
				return "", nil, errors.New("agent.version must be at least 1")
			}
		}
		agent, vendor, err := s.repo.GetAgent(ctx, principal.TeamID, agentID, version)
		if err != nil {
			return "", nil, err
		}
		if err := ensureSupportedVendor(vendor); err != nil {
			return "", nil, err
		}
		return vendor, agentToSnapshot(*agent), nil
	default:
		return "", nil, errors.New("agent is required")
	}
}

func ensureSupportedVendor(vendor string) error {
	if IsSupportedManagedAgentsEngine(vendor) {
		return nil
	}
	return errors.New("unsupported managed-agent engine")
}

func (s *Service) resolveSessionVendorFromVaults(ctx context.Context, principal Principal, fallback string, vaultIDs []string) (string, error) {
	vendor := normalizeManagedMetadataValue(fallback)
	if vendor == "" {
		vendor = ManagedAgentsEngineClaude
	}
	selected := ""
	for _, vaultID := range vaultIDs {
		vault, err := s.repo.GetVault(ctx, principal.TeamID, vaultID)
		if err != nil {
			return "", err
		}
		config := ManagedVaultConfigFromMetadata(vault.Metadata)
		if config.Role != ManagedAgentsVaultRoleLLM {
			continue
		}
		if !IsSupportedManagedAgentsEngine(config.Engine) {
			return "", fmt.Errorf("llm vault %s uses unsupported engine %q", vault.ID, config.Engine)
		}
		if selected != "" && selected != config.Engine {
			return "", fmt.Errorf("session can attach exactly one %s=%q engine, got %q and %q", ManagedAgentsVaultRoleKey, ManagedAgentsVaultRoleLLM, selected, config.Engine)
		}
		selected = config.Engine
	}
	if selected != "" {
		vendor = selected
	}
	if err := ensureSupportedVendor(vendor); err != nil {
		return "", err
	}
	return vendor, nil
}

func (s *Service) validateSessionDependencies(ctx context.Context, principal Principal, environmentID string, vaultIDs []string, resources []map[string]any) ([]map[string]any, map[string]map[string]any, error) {
	if _, err := s.repo.GetEnvironment(ctx, principal.TeamID, environmentID); err != nil {
		return nil, nil, err
	}
	for _, vaultID := range vaultIDs {
		vault, err := s.repo.GetVault(ctx, principal.TeamID, vaultID)
		if err != nil {
			return nil, nil, err
		}
		if err := requireActiveVault(vault); err != nil {
			return nil, nil, err
		}
	}
	now := time.Now().UTC()
	normalized := make([]map[string]any, 0, len(resources))
	secrets := make(map[string]map[string]any)
	for _, resource := range resources {
		validated, err := s.validateAndNormalizeResource(ctx, principal, normalizeSessionResource(resource, now), true)
		if err != nil {
			return nil, nil, err
		}
		normalized = append(normalized, validated.Public)
		if len(validated.Secret) > 0 {
			secrets[stringValue(validated.Public["id"])] = validated.Secret
		}
	}
	return normalized, secrets, nil
}

func requireActiveVault(vault *Vault) error {
	if vault == nil {
		return ErrVaultNotFound
	}
	if vault.ArchivedAt != nil && strings.TrimSpace(*vault.ArchivedAt) != "" {
		return errors.New("vault is archived")
	}
	return nil
}

func validateUniqueActiveCredentialURL(activeCredentials []StoredCredential, serverURL string) error {
	if strings.TrimSpace(serverURL) == "" {
		return nil
	}
	canonicalURL, err := CanonicalMCPServerURL(serverURL)
	if err != nil {
		return err
	}
	for _, existing := range activeCredentials {
		existingURL := strings.TrimSpace(existing.Snapshot.Auth.MCPServerURL)
		if existingURL == "" {
			existingURL = strings.TrimSpace(stringValue(existing.Secret["mcp_server_url"]))
		}
		if existingURL == "" {
			continue
		}
		existingCanonicalURL, err := CanonicalMCPServerURL(existingURL)
		if err != nil {
			continue
		}
		if existingCanonicalURL == canonicalURL {
			return fmt.Errorf("active credential for mcp_server_url %q already exists", serverURL)
		}
	}
	return nil
}

func validateAllowedFields(input map[string]any, allowed []string) error {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		allowedSet[field] = struct{}{}
	}
	for field := range input {
		if _, ok := allowedSet[field]; !ok {
			return errors.New("invalid resource field: " + field)
		}
	}
	return nil
}

func defaultFileMountPath(fileID, mountPath string) string {
	if trimmed := strings.TrimSpace(mountPath); trimmed != "" {
		return trimmed
	}
	return "/mnt/session/uploads/" + fileID
}

func teamFileAssetStorePath(fileID string) string {
	return path.Join("/managed-agents-assets/files", strings.TrimSpace(fileID), "content")
}

func defaultGitHubMountPath(repoName, mountPath string) string {
	if trimmed := strings.TrimSpace(mountPath); trimmed != "" {
		return trimmed
	}
	return "/workspace/" + repoName
}

func repositoryNameFromURL(repoURL *url.URL) string {
	if repoURL == nil {
		return ""
	}
	name := strings.TrimSuffix(path.Base(strings.TrimSpace(repoURL.Path)), ".git")
	if name == "." || name == "/" {
		return ""
	}
	return strings.TrimSpace(name)
}

func normalizeRepositoryCheckout(input any) (map[string]any, error) {
	if input == nil {
		return nil, nil
	}
	checkout, ok := input.(map[string]any)
	if !ok {
		return nil, errors.New("checkout must be an object")
	}
	checkout = cloneMap(checkout)
	switch strings.TrimSpace(stringValue(checkout["type"])) {
	case "branch":
		if err := validateAllowedFields(checkout, []string{"type", "name"}); err != nil {
			return nil, err
		}
		name := strings.TrimSpace(stringValue(checkout["name"]))
		if name == "" {
			return nil, errors.New("checkout.name is required")
		}
		return map[string]any{"type": "branch", "name": name}, nil
	case "commit":
		if err := validateAllowedFields(checkout, []string{"type", "sha"}); err != nil {
			return nil, err
		}
		sha := strings.TrimSpace(stringValue(checkout["sha"]))
		if len(sha) < 7 {
			return nil, errors.New("checkout.sha is required")
		}
		return map[string]any{"type": "commit", "sha": sha}, nil
	case "":
		return nil, errors.New("checkout.type is required")
	default:
		return nil, errors.New("invalid checkout type")
	}
}
