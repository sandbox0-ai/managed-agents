package managedagentsruntime

import (
	"strings"
	"testing"

	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
	apispec "github.com/sandbox0-ai/sdk-go/pkg/apispec"
)

func TestMergeManagedGitHubCredentialPolicy(t *testing.T) {
	base := apispec.SandboxNetworkPolicy{
		Mode: apispec.SandboxNetworkPolicyModeAllowAll,
		CredentialBindings: []apispec.CredentialBinding{{
			Ref:       "existing-bind",
			SourceRef: "existing-source",
		}},
		Egress: apispec.NewOptNetworkEgressPolicy(apispec.NetworkEgressPolicy{
			CredentialRules: []apispec.EgressCredentialRule{{
				Name:          apispec.NewOptString("existing-rule"),
				CredentialRef: "existing-bind",
				Domains:       []string{"example.com"},
			}},
		}),
	}

	policy := mergeManagedCredentialPolicy(base, "sesn_123", []managedCredentialBinding{{
		key:        "rsc_123",
		sourceName: "managed-agent-sesn-123-rsc-123",
		domains:    []string{"github.com"},
		protocol:   apispec.EgressAuthProtocolHTTPS,
		header:     "{{ .authorization }}",
	}})

	if len(policy.CredentialBindings) != 2 {
		t.Fatalf("credential bindings = %d, want 2", len(policy.CredentialBindings))
	}
	if policy.CredentialBindings[1].SourceRef != "managed-agent-sesn-123-rsc-123" {
		t.Fatalf("managed source ref = %q", policy.CredentialBindings[1].SourceRef)
	}
	egress, ok := policy.Egress.Get()
	if !ok {
		t.Fatal("egress not set")
	}
	if len(egress.CredentialRules) != 2 {
		t.Fatalf("credential rules = %d, want 2", len(egress.CredentialRules))
	}
	if egress.CredentialRules[1].CredentialRef == "" {
		t.Fatal("managed credential ref is empty")
	}
	protocol, ok := egress.CredentialRules[1].Protocol.Get()
	if !ok || protocol != apispec.EgressAuthProtocolHTTPS {
		t.Fatalf("protocol = %q, want https", protocol)
	}
	projection := policy.CredentialBindings[1].Projection
	headers, ok := projection.HttpHeaders.Get()
	if !ok || len(headers.Headers) != 1 || headers.Headers[0].ValueTemplate != "{{ .authorization }}" {
		t.Fatalf("projection headers = %#v", headers.Headers)
	}
}

func TestGitHubAuthorizationHeaderUsesBasicAuth(t *testing.T) {
	header := githubAuthorizationHeader("token-123")
	if !strings.HasPrefix(header, "Basic ") {
		t.Fatalf("header = %q, want Basic prefix", header)
	}
}

func TestManagedBindingFromVaultCredentialUsesBearerAuth(t *testing.T) {
	binding, err := managedBindingFromVaultCredential("sesn_123", gatewaymanagedagents.StoredCredential{
		Snapshot: gatewaymanagedagents.Credential{
			ID: "vcrd_123",
			Auth: gatewaymanagedagents.CredentialAuth{
				Type:         "static_bearer",
				MCPServerURL: "https://mcp.example.com/sse",
			},
		},
		Secret: map[string]any{
			"type":           "static_bearer",
			"token":          "secret-token",
			"mcp_server_url": "https://mcp.example.com/sse",
		},
	}, map[string]struct{}{"https://mcp.example.com/sse": {}})
	if err != nil {
		t.Fatalf("managedBindingFromVaultCredential: %v", err)
	}
	if binding == nil {
		t.Fatal("expected binding")
	}
	if binding.secret != "Bearer secret-token" {
		t.Fatalf("binding secret = %q, want bearer token", binding.secret)
	}
	if binding.protocol != apispec.EgressAuthProtocolHTTPS {
		t.Fatalf("binding protocol = %q, want https", binding.protocol)
	}
	if binding.sourceName == "" {
		t.Fatal("binding source name is empty")
	}
}

func TestNormalizedStoredSkillRelativePathPreservesTopLevelDirectory(t *testing.T) {
	got := normalizedStoredSkillRelativePath("demo-skill", "demo-skill/SKILL.md")
	if got != "demo-skill/SKILL.md" {
		t.Fatalf("relative path = %q, want demo-skill/SKILL.md", got)
	}
}

func TestNormalizedStoredSkillRelativePathPrefixesDirectoryWhenUploadWasFlat(t *testing.T) {
	got := normalizedStoredSkillRelativePath("demo-skill", "SKILL.md")
	if got != "demo-skill/SKILL.md" {
		t.Fatalf("relative path = %q, want demo-skill/SKILL.md", got)
	}
}

func TestSkillFileTargetPathPlacesFilesUnderProjectSkillsDirectory(t *testing.T) {
	got := skillFileTargetPath("/workspace", "demo-skill", "docs/guide.md")
	if got != "/workspace/.claude/skills/demo-skill/docs/guide.md" {
		t.Fatalf("target path = %q", got)
	}
}
