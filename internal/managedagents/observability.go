package managedagents

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// Observability records managed-agent operation spans, metrics, and structured phase logs.
type Observability struct {
	disabled bool
	tracer   trace.Tracer
	logger   *zap.Logger

	operationTotal    *prometheus.CounterVec
	operationDuration *prometheus.HistogramVec
	activeOperations  *prometheus.GaugeVec
	phaseTotal        *prometheus.CounterVec
	phaseDuration     *prometheus.HistogramVec
}

type ObservabilityConfig struct {
	ServiceName string
	Tracer      trace.Tracer
	Logger      *zap.Logger
	Registry    prometheus.Registerer
	Disabled    bool
}

type Operation struct {
	obs       *Observability
	name      string
	vendor    string
	start     time.Time
	span      trace.Span
	logFields []zap.Field
}

// NewObservability creates the service-level observability recorder.
func NewObservability(cfg ObservabilityConfig) *Observability {
	if cfg.Tracer == nil {
		cfg.Tracer = otel.Tracer("managed-agents")
	}
	if cfg.Logger == nil {
		cfg.Logger = zap.NewNop()
	}
	obs := &Observability{disabled: cfg.Disabled, tracer: cfg.Tracer, logger: cfg.Logger}
	if cfg.Disabled || cfg.Registry == nil {
		return obs
	}
	prefix := metricPrefix(cfg.ServiceName)
	if prefix == "" {
		prefix = "managed_agents"
	}
	obs.operationTotal = registerCounterVec(cfg.Registry, prometheus.CounterOpts{
		Name: prefix + "_operation_total",
		Help: "Total managed-agent operations by operation, vendor, and status.",
	}, []string{"operation", "vendor", "status"})
	obs.operationDuration = registerHistogramVec(cfg.Registry, prometheus.HistogramOpts{
		Name:    prefix + "_operation_duration_seconds",
		Help:    "Managed-agent operation duration by operation, vendor, and status.",
		Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120},
	}, []string{"operation", "vendor", "status"})
	obs.activeOperations = registerGaugeVec(cfg.Registry, prometheus.GaugeOpts{
		Name: prefix + "_active_operations",
		Help: "Current managed-agent operations in progress by operation and vendor.",
	}, []string{"operation", "vendor"})
	obs.phaseTotal = registerCounterVec(cfg.Registry, prometheus.CounterOpts{
		Name: prefix + "_operation_phase_total",
		Help: "Total managed-agent operation phases by operation, phase, vendor, and status.",
	}, []string{"operation", "phase", "vendor", "status"})
	obs.phaseDuration = registerHistogramVec(cfg.Registry, prometheus.HistogramOpts{
		Name:    prefix + "_operation_phase_duration_seconds",
		Help:    "Managed-agent operation phase duration by operation, phase, vendor, and status.",
		Buckets: []float64{.0005, .001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120},
	}, []string{"operation", "phase", "vendor", "status"})
	return obs
}

func (o *Observability) StartOperation(ctx context.Context, name, vendor string, fields ...zap.Field) (context.Context, *Operation) {
	if o == nil || o.disabled {
		return ctx, nil
	}
	vendor = normalizeMetricLabel(vendor)
	if vendor == "" {
		vendor = "unknown"
	}
	attrs := []attribute.KeyValue{
		attribute.String("managed_agent.operation", name),
		attribute.String("managed_agent.vendor", vendor),
	}
	ctx, span := o.tracer.Start(ctx, "managed-agent."+name, trace.WithAttributes(attrs...))
	if o.activeOperations != nil {
		o.activeOperations.WithLabelValues(name, vendor).Inc()
	}
	return ctx, &Operation{obs: o, name: name, vendor: vendor, start: time.Now(), span: span, logFields: fields}
}

func (op *Operation) End(err error) {
	if op == nil || op.obs == nil {
		return
	}
	status := statusLabel(err)
	duration := time.Since(op.start)
	if op.obs.activeOperations != nil {
		op.obs.activeOperations.WithLabelValues(op.name, op.vendor).Dec()
	}
	if op.obs.operationTotal != nil {
		op.obs.operationTotal.WithLabelValues(op.name, op.vendor, status).Inc()
	}
	if op.obs.operationDuration != nil {
		op.obs.operationDuration.WithLabelValues(op.name, op.vendor, status).Observe(duration.Seconds())
	}
	if err != nil {
		op.span.RecordError(err)
		op.span.SetStatus(codes.Error, err.Error())
	} else {
		op.span.SetStatus(codes.Ok, "")
	}
	op.span.End()
	fields := append([]zap.Field{
		zap.String("operation", op.name),
		zap.String("vendor", op.vendor),
		zap.String("status", status),
		zap.Duration("duration", duration),
		zap.String("trace_id", op.span.SpanContext().TraceID().String()),
	}, op.logFields...)
	if err != nil {
		fields = append(fields, zap.Error(err))
		op.obs.logger.Warn("managed-agent operation completed", fields...)
		return
	}
	op.obs.logger.Info("managed-agent operation completed", fields...)
}

func (op *Operation) ObservePhase(phase string, duration time.Duration, err error, fields ...zap.Field) {
	if op == nil || op.obs == nil {
		return
	}
	status := statusLabel(err)
	if op.obs.phaseTotal != nil {
		op.obs.phaseTotal.WithLabelValues(op.name, phase, op.vendor, status).Inc()
	}
	if op.obs.phaseDuration != nil {
		op.obs.phaseDuration.WithLabelValues(op.name, phase, op.vendor, status).Observe(duration.Seconds())
	}
	attrs := []attribute.KeyValue{
		attribute.String("managed_agent.phase", phase),
		attribute.String("managed_agent.phase.status", status),
		attribute.Float64("managed_agent.phase.duration_ms", float64(duration.Microseconds())/1000),
	}
	if err != nil {
		attrs = append(attrs, attribute.String("managed_agent.phase.error", err.Error()))
	}
	op.span.AddEvent("managed-agent.phase."+phase, trace.WithAttributes(attrs...))
	logFields := append([]zap.Field{
		zap.String("operation", op.name),
		zap.String("phase", phase),
		zap.String("vendor", op.vendor),
		zap.String("status", status),
		zap.Duration("duration", duration),
		zap.String("trace_id", op.span.SpanContext().TraceID().String()),
	}, fields...)
	if err != nil {
		logFields = append(logFields, zap.Error(err))
		op.obs.logger.Warn("managed-agent phase completed", logFields...)
		return
	}
	op.obs.logger.Debug("managed-agent phase completed", logFields...)
}

func (o *Observability) ObservePhase(ctx context.Context, operation, phase, vendor string, duration time.Duration, err error, fields ...zap.Field) {
	ctx, op := o.StartOperation(ctx, operation, vendor, fields...)
	_ = ctx
	if op == nil {
		return
	}
	op.ObservePhase(phase, duration, err, fields...)
	op.End(err)
}

func (op *Operation) Phase(ctx context.Context, phase string, fields ...zap.Field) func(*error) {
	start := time.Now()
	return func(errp *error) {
		var err error
		if errp != nil {
			err = *errp
		}
		op.ObservePhase(phase, time.Since(start), err, fields...)
	}
}

func statusLabel(err error) string {
	if err == nil {
		return "success"
	}
	return "error"
}

func normalizeMetricLabel(value string) string {
	return strings.TrimSpace(strings.ToLower(value))
}

func metricPrefix(serviceName string) string {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return ""
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range serviceName {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(unicode.ToLower(r))
			lastUnderscore = false
		default:
			if !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

func registerCounterVec(reg prometheus.Registerer, opts prometheus.CounterOpts, labels []string) *prometheus.CounterVec {
	collector := prometheus.NewCounterVec(opts, labels)
	if err := reg.Register(collector); err != nil {
		var already prometheus.AlreadyRegisteredError
		if errors.As(err, &already) {
			if existing, ok := already.ExistingCollector.(*prometheus.CounterVec); ok {
				return existing
			}
		}
	}
	return collector
}

func registerHistogramVec(reg prometheus.Registerer, opts prometheus.HistogramOpts, labels []string) *prometheus.HistogramVec {
	collector := prometheus.NewHistogramVec(opts, labels)
	if err := reg.Register(collector); err != nil {
		var already prometheus.AlreadyRegisteredError
		if errors.As(err, &already) {
			if existing, ok := already.ExistingCollector.(*prometheus.HistogramVec); ok {
				return existing
			}
		}
	}
	return collector
}

func registerGaugeVec(reg prometheus.Registerer, opts prometheus.GaugeOpts, labels []string) *prometheus.GaugeVec {
	collector := prometheus.NewGaugeVec(opts, labels)
	if err := reg.Register(collector); err != nil {
		var already prometheus.AlreadyRegisteredError
		if errors.As(err, &already) {
			if existing, ok := already.ExistingCollector.(*prometheus.GaugeVec); ok {
				return existing
			}
		}
	}
	return collector
}
