package main

import (
	"context"
	"reflect"
	"testing"

	"go.uber.org/zap"
)

func TestLoadConfigDefaultsSandbox0BaseURLToGlobal(t *testing.T) {
	t.Setenv("MANAGED_AGENT_DATABASE_URL", "postgres://example")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Sandbox0BaseURL != defaultSandbox0BaseURL {
		t.Fatalf("Sandbox0BaseURL = %q, want %q", cfg.Sandbox0BaseURL, defaultSandbox0BaseURL)
	}
	if cfg.Sandbox0AuthBaseURL != defaultSandbox0BaseURL {
		t.Fatalf("Sandbox0AuthBaseURL = %q, want %q", cfg.Sandbox0AuthBaseURL, defaultSandbox0BaseURL)
	}
}

func TestLoadConfigTrimsSandbox0BaseURL(t *testing.T) {
	t.Setenv("MANAGED_AGENT_DATABASE_URL", "postgres://example")
	t.Setenv("MANAGED_AGENT_SANDBOX0_BASE_URL", "https://api.sandbox0.ai/")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Sandbox0BaseURL != "https://api.sandbox0.ai" {
		t.Fatalf("Sandbox0BaseURL = %q, want trimmed URL", cfg.Sandbox0BaseURL)
	}
	if cfg.Sandbox0AuthBaseURL != "https://api.sandbox0.ai" {
		t.Fatalf("Sandbox0AuthBaseURL = %q, want trimmed URL", cfg.Sandbox0AuthBaseURL)
	}
}

func TestLoadConfigUsesSandbox0AuthBaseURL(t *testing.T) {
	t.Setenv("MANAGED_AGENT_DATABASE_URL", "postgres://example")
	t.Setenv("MANAGED_AGENT_SANDBOX0_BASE_URL", "https://gcp-ue4.sandbox0.ai/")
	t.Setenv("MANAGED_AGENT_SANDBOX0_AUTH_BASE_URL", "https://api.sandbox0.ai/")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Sandbox0BaseURL != "https://gcp-ue4.sandbox0.ai" {
		t.Fatalf("Sandbox0BaseURL = %q, want region URL", cfg.Sandbox0BaseURL)
	}
	if cfg.Sandbox0AuthBaseURL != "https://api.sandbox0.ai" {
		t.Fatalf("Sandbox0AuthBaseURL = %q, want global URL", cfg.Sandbox0AuthBaseURL)
	}
}

func TestLoadConfigUsesSandbox0ExposureBaseURL(t *testing.T) {
	t.Setenv("MANAGED_AGENT_DATABASE_URL", "postgres://example")
	t.Setenv("MANAGED_AGENT_SANDBOX0_EXPOSURE_BASE_URL", "http://cluster-gateway.svc.cluster.local:30080/")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Sandbox0ExposureBaseURL != "http://cluster-gateway.svc.cluster.local:30080" {
		t.Fatalf("Sandbox0ExposureBaseURL = %q, want trimmed URL", cfg.Sandbox0ExposureBaseURL)
	}
}

func TestLoadConfigParsesRuntimeAllowedDomains(t *testing.T) {
	t.Setenv("MANAGED_AGENT_DATABASE_URL", "postgres://example")
	t.Setenv("MANAGED_AGENT_RUNTIME_ALLOWED_DOMAINS", "api.search.test, https://gateway.example.test/path\nextra.example.test")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	want := []string{"api.search.test", "https://gateway.example.test/path", "extra.example.test"}
	if !reflect.DeepEqual(cfg.RuntimeAllowedDomains, want) {
		t.Fatalf("RuntimeAllowedDomains = %#v, want %#v", cfg.RuntimeAllowedDomains, want)
	}
}

func TestLoadConfigUsesTemplateIDEnv(t *testing.T) {
	t.Setenv("MANAGED_AGENT_DATABASE_URL", "postgres://example")
	t.Setenv("MANAGED_AGENT_TEMPLATE_ID", " managed-agents ")
	t.Setenv("MANAGED_AGENT_CLAUDE_TEMPLATE", "legacy-template")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.TemplateID != "managed-agents" {
		t.Fatalf("TemplateID = %q, want managed-agents", cfg.TemplateID)
	}
}

func TestLoadConfigFallsBackToLegacyClaudeTemplateEnv(t *testing.T) {
	t.Setenv("MANAGED_AGENT_DATABASE_URL", "postgres://example")
	t.Setenv("MANAGED_AGENT_CLAUDE_TEMPLATE", " legacy-template ")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.TemplateID != "legacy-template" {
		t.Fatalf("TemplateID = %q, want legacy-template", cfg.TemplateID)
	}
}

func TestInitTracingNoopExporterCreatesTraceIDs(t *testing.T) {
	cfg := config{
		ObservabilityEnabled: true,
		TraceExporter:        "noop",
		TraceSampleRate:      1,
	}
	tracer, shutdown, err := initTracing(context.Background(), cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("initTracing: %v", err)
	}
	defer func() {
		if err := shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown tracing: %v", err)
		}
	}()

	_, span := tracer.Start(context.Background(), "test")
	defer span.End()
	if !span.SpanContext().IsValid() {
		t.Fatal("expected noop exporter to keep a valid trace context for log correlation")
	}
}

func TestInitTracingDisabledUsesInvalidTraceContext(t *testing.T) {
	cfg := config{
		ObservabilityEnabled: false,
		TraceExporter:        "noop",
		TraceSampleRate:      1,
	}
	tracer, shutdown, err := initTracing(context.Background(), cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("initTracing: %v", err)
	}
	defer func() {
		if err := shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown tracing: %v", err)
		}
	}()

	_, span := tracer.Start(context.Background(), "test")
	defer span.End()
	if span.SpanContext().IsValid() {
		t.Fatal("expected disabled observability to use an invalid noop trace context")
	}
}
