package managedagentsruntime

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
	apispec "github.com/sandbox0-ai/sdk-go/pkg/apispec"
)

var packageManagerDomains = map[string][]string{
	"apt":   {"deb.debian.org", "security.debian.org", "archive.ubuntu.com", "ports.ubuntu.com"},
	"cargo": {"crates.io", "index.crates.io", "static.crates.io", "github.com"},
	"gem":   {"rubygems.org"},
	"go":    {"proxy.golang.org", "sum.golang.org", "github.com"},
	"npm":   {"registry.npmjs.org"},
	"pip":   {"pypi.org", "files.pythonhosted.org"},
}

func (m *SDKRuntimeManager) templateRequestForEnvironment(environment *gatewaymanagedagents.Environment) (*apispec.TemplateCreateRequest, error) {
	if m.templateRequest == nil {
		return nil, nil
	}
	request, err := cloneTemplateRequest(m.templateRequest)
	if err != nil {
		return nil, err
	}
	if environment == nil {
		return request, nil
	}
	request.TemplateID = environmentTemplateID(request.TemplateID, environment.ID)
	displayName := strings.TrimSpace(environment.Name)
	if displayName == "" {
		displayName = environment.ID
	}
	request.Spec.DisplayName = apispec.NewOptString(fmt.Sprintf("Claude Managed Agent - %s", displayName))
	request.Spec.Network = apispec.NewOptSandboxNetworkPolicy(environmentNetworkPolicy(environment))
	envVars := map[string]string{}
	if existing, ok := request.Spec.EnvVars.Get(); ok {
		for key, value := range existing {
			envVars[key] = value
		}
	}
	envVars["MANAGED_AGENT_ENVIRONMENT_ID"] = environment.ID
	request.Spec.EnvVars = apispec.NewOptSandboxTemplateSpecEnvVars(envVars)
	return request, nil
}

func cloneTemplateRequest(request *apispec.TemplateCreateRequest) (*apispec.TemplateCreateRequest, error) {
	if request == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal template request: %w", err)
	}
	var cloned apispec.TemplateCreateRequest
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return nil, fmt.Errorf("clone template request: %w", err)
	}
	return &cloned, nil
}

func environmentTemplateID(baseTemplateID, environmentID string) string {
	base := sanitizeName(baseTemplateID)
	if base == "unknown" {
		base = "managed-agent"
	}
	env := sanitizeName(environmentID)
	if env == "unknown" {
		return base
	}
	return truncateTemplateID(base + "-" + env)
}

func truncateTemplateID(value string) string {
	const maxTemplateIDLength = 255
	if len(value) <= maxTemplateIDLength {
		return value
	}
	return strings.TrimRight(value[:maxTemplateIDLength], "-")
}

func runtimeNetworkPolicy(environment *gatewaymanagedagents.Environment, engine map[string]any) apispec.SandboxNetworkPolicy {
	if policy, ok := decodeNetworkPolicy(engine); ok {
		return policy
	}
	return environmentNetworkPolicy(environment)
}

func environmentNetworkPolicy(environment *gatewaymanagedagents.Environment) apispec.SandboxNetworkPolicy {
	if environment == nil || strings.TrimSpace(environment.Config.Networking.Type) == "unrestricted" {
		return apispec.SandboxNetworkPolicy{Mode: apispec.SandboxNetworkPolicyModeAllowAll}
	}
	domains := append([]string(nil), environment.Config.Networking.AllowedHosts...)
	if environment.Config.Networking.AllowPackageManagers || environmentHasPackages(environment.Config.Packages) {
		domains = append(domains, domainsForPackages(environment.Config.Packages)...)
	}
	domains = normalizeDomains(domains)
	policy := apispec.SandboxNetworkPolicy{Mode: apispec.SandboxNetworkPolicyModeBlockAll}
	if len(domains) > 0 {
		policy.Egress = apispec.NewOptNetworkEgressPolicy(apispec.NetworkEgressPolicy{AllowedDomains: domains})
	}
	return policy
}

func environmentHasPackages(packages gatewaymanagedagents.EnvironmentPackages) bool {
	return len(packages.Apt)+len(packages.Cargo)+len(packages.Gem)+len(packages.Go)+len(packages.NPM)+len(packages.Pip) > 0
}

func domainsForPackages(packages gatewaymanagedagents.EnvironmentPackages) []string {
	domains := []string{}
	appendFor := func(manager string, items []string) {
		if len(items) == 0 {
			return
		}
		domains = append(domains, packageManagerDomains[manager]...)
	}
	appendFor("apt", packages.Apt)
	appendFor("cargo", packages.Cargo)
	appendFor("gem", packages.Gem)
	appendFor("go", packages.Go)
	appendFor("npm", packages.NPM)
	appendFor("pip", packages.Pip)
	return domains
}

func environmentSnapshot(environment *gatewaymanagedagents.Environment) map[string]any {
	if environment == nil {
		return nil
	}
	return map[string]any{
		"type":        environment.Type,
		"id":          environment.ID,
		"name":        environment.Name,
		"description": environment.Description,
		"config":      environmentConfigSnapshot(environment.Config),
		"metadata":    cloneStringMap(environment.Metadata),
		"created_at":  environment.CreatedAt,
		"updated_at":  environment.UpdatedAt,
		"archived_at": environment.ArchivedAt,
	}
}

func environmentConfigSnapshot(config gatewaymanagedagents.CloudConfig) map[string]any {
	return map[string]any{
		"type": config.Type,
		"networking": map[string]any{
			"type":                   config.Networking.Type,
			"allowed_hosts":          append([]string(nil), config.Networking.AllowedHosts...),
			"allow_package_managers": config.Networking.AllowPackageManagers,
			"allow_mcp_servers":      config.Networking.AllowMCPServers,
		},
		"packages": map[string]any{
			"type":  config.Packages.Type,
			"apt":   append([]string(nil), config.Packages.Apt...),
			"cargo": append([]string(nil), config.Packages.Cargo...),
			"gem":   append([]string(nil), config.Packages.Gem...),
			"go":    append([]string(nil), config.Packages.Go...),
			"npm":   append([]string(nil), config.Packages.NPM...),
			"pip":   append([]string(nil), config.Packages.Pip...),
		},
	}
}

func normalizeDomains(domains []string) []string {
	seen := make(map[string]struct{}, len(domains))
	out := make([]string, 0, len(domains))
	for _, domain := range domains {
		trimmed := strings.ToLower(strings.TrimSpace(domain))
		trimmed = strings.TrimPrefix(trimmed, "https://")
		trimmed = strings.TrimPrefix(trimmed, "http://")
		trimmed = strings.Trim(trimmed, "/")
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}

func appendUniqueStrings(base []string, values ...string) []string {
	return normalizeDomains(append(base, values...))
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
