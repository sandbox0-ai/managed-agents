package managedagents

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	tracetest "go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/zap"
)

func TestObservabilityRecordsOperationAndPhaseMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	obs := NewObservability(ObservabilityConfig{
		ServiceName: "managed-agents",
		Logger:      zap.NewNop(),
		Registry:    registry,
	})

	_, op := obs.StartOperation(context.Background(), "session_create", "claude")
	op.ObservePhase("ensure_runtime", 10*time.Millisecond, nil)
	op.End(nil)

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	assertMetricWithLabels(t, families, "managed_agents_operation_total", map[string]string{
		"operation": "session_create",
		"vendor":    "claude",
		"status":    "success",
	})
	assertMetricWithLabels(t, families, "managed_agents_operation_phase_total", map[string]string{
		"operation": "session_create",
		"phase":     "ensure_runtime",
		"vendor":    "claude",
		"status":    "success",
	})
}

func TestObservabilityDisabledSkipsBusinessMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	obs := NewObservability(ObservabilityConfig{
		ServiceName: "managed-agents",
		Logger:      zap.NewNop(),
		Registry:    registry,
		Disabled:    true,
	})

	_, op := obs.StartOperation(context.Background(), "session_create", "claude")
	if op != nil {
		t.Fatal("expected disabled observability to return no operation")
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() == "managed_agents_operation_total" || family.GetName() == "managed_agents_operation_phase_total" {
			t.Fatalf("unexpected metric family %q", family.GetName())
		}
	}
}

func TestObservabilityAnnotatesSpanWithOperationAndPhaseFields(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider()
	provider.RegisterSpanProcessor(recorder)

	obs := NewObservability(ObservabilityConfig{
		ServiceName: "managed-agents",
		Logger:      zap.NewNop(),
		Tracer:      provider.Tracer("managed-agents-test"),
	})

	_, op := obs.StartOperation(context.Background(), "session_create", "claude",
		zap.String("team_id", "team_123"),
	)
	op.AddFields(
		zap.String("session_id", "sesn_123"),
		zap.Int("resource_count", 2),
	)
	op.ObservePhase("ensure_runtime", 10*time.Millisecond, nil,
		zap.String("sandbox_id", "sbox_123"),
	)
	op.End(nil)

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 ended span, got %d", len(spans))
	}
	span := spans[0]
	assertSpanAttribute(t, span.Attributes(), "managed_agent.field.team_id", "team_123")
	assertSpanAttribute(t, span.Attributes(), "managed_agent.field.session_id", "sesn_123")
	assertSpanAttribute(t, span.Attributes(), "managed_agent.field.resource_count", int64(2))
	if len(span.Events()) != 1 {
		t.Fatalf("expected 1 span event, got %d", len(span.Events()))
	}
	assertSpanAttribute(t, span.Events()[0].Attributes, "managed_agent.phase.field.sandbox_id", "sbox_123")
}

func assertMetricWithLabels(t *testing.T, families []*dto.MetricFamily, name string, labels map[string]string) {
	t.Helper()
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			if metricHasLabels(metric, labels) {
				return
			}
		}
		t.Fatalf("metric family %q did not contain labels %#v", name, labels)
	}
	t.Fatalf("metric family %q not found", name)
}

func metricHasLabels(metric *dto.Metric, labels map[string]string) bool {
	for expectedName, expectedValue := range labels {
		found := false
		for _, label := range metric.GetLabel() {
			if label.GetName() == expectedName && label.GetValue() == expectedValue {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func assertSpanAttribute(t *testing.T, attrs []attribute.KeyValue, key string, expected any) {
	t.Helper()
	for _, attr := range attrs {
		if string(attr.Key) != key {
			continue
		}
		switch want := expected.(type) {
		case string:
			if attr.Value.AsString() != want {
				t.Fatalf("attribute %s = %q, want %q", key, attr.Value.AsString(), want)
			}
			return
		case int64:
			if attr.Value.AsInt64() != want {
				t.Fatalf("attribute %s = %d, want %d", key, attr.Value.AsInt64(), want)
			}
			return
		default:
			t.Fatalf("unsupported expected attribute type %T", expected)
		}
	}
	t.Fatalf("attribute %s not found", key)
}
