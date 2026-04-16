package managedagents

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
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
