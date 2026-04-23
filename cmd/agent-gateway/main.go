package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sandbox0-ai/managed-agent/internal/dbpool"
	"github.com/sandbox0-ai/managed-agent/internal/httpauth"
	"github.com/sandbox0-ai/managed-agent/internal/managedagents"
	managedagentmigrations "github.com/sandbox0-ai/managed-agent/internal/managedagents/migrations"
	"github.com/sandbox0-ai/managed-agent/internal/managedagentsruntime"
	"github.com/sandbox0-ai/managed-agent/internal/migrate"
	httpobs "github.com/sandbox0-ai/sandbox0/pkg/observability/http"
	pgxobs "github.com/sandbox0-ai/sandbox0/pkg/observability/pgx"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type config struct {
	RunMode                 string
	HTTPAddr                string
	LogLevel                string
	DatabaseURL             string
	DatabaseSchema          string
	DatabaseMaxConns        int32
	DatabaseMinConns        int32
	Sandbox0BaseURL         string
	Sandbox0AuthBaseURL     string
	Sandbox0ExposureBaseURL string
	Sandbox0AdminAPIKey     string
	RuntimeCallbackBaseURL  string
	Sandbox0Timeout         time.Duration
	RuntimeEnabled          bool
	RuntimeAllowedDomains   []string
	TemplateID              string
	TemplateManifestPath    string
	TemplateMainImage       string
	WrapperPort             int
	WorkspaceMountPath      string
	SandboxTTLSeconds       int
	ObservabilityEnabled    bool
	MetricsEnabled          bool
	TraceExporter           string
	TraceOTLPEndpoint       string
	TraceOTLPInsecure       bool
	TraceSampleRate         float64
	BackfillDryRun          bool
	BackfillFiles           bool
	BackfillSkills          bool
	BackfillBatchSize       int
	BackfillMaxItems        int
	BackfillTeamID          string
}

const defaultSandbox0BaseURL = "https://api.sandbox0.ai"
const observabilityServiceName = "managed-agents"

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	logger, err := initLogger(cfg.LogLevel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tracer, shutdownTracing, err := initTracing(ctx, cfg, logger)
	if err != nil {
		logger.Fatal("init tracing", zap.Error(err))
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := shutdownTracing(shutdownCtx); err != nil {
			logger.Warn("shutdown tracing failed", zap.Error(err))
		}
	}()

	metricsEnabled := cfg.ObservabilityEnabled && cfg.MetricsEnabled
	var metricsRegistry prometheus.Registerer
	if metricsEnabled {
		metricsRegistry = prometheus.DefaultRegisterer
	}
	httpAdapter := httpobs.NewAdapter(httpobs.AdapterConfig{
		ServiceName:    observabilityServiceName,
		Tracer:         tracer,
		Logger:         logger,
		Registry:       metricsRegistry,
		DisableMetrics: !metricsEnabled,
		Disabled:       !cfg.ObservabilityEnabled,
	})
	pgxAdapter := pgxobs.NewAdapter(pgxobs.AdapterConfig{
		ServiceName:    observabilityServiceName,
		Tracer:         tracer,
		Logger:         logger,
		Registry:       metricsRegistry,
		DisableMetrics: !metricsEnabled,
		Disabled:       !cfg.ObservabilityEnabled,
	})
	managedObservability := managedagents.NewObservability(managedagents.ObservabilityConfig{
		ServiceName: observabilityServiceName,
		Tracer:      tracer,
		Logger:      logger,
		Registry:    metricsRegistry,
		Disabled:    !cfg.ObservabilityEnabled,
	})

	pool, err := dbpool.New(ctx, dbpool.Options{
		DatabaseURL:    cfg.DatabaseURL,
		MaxConns:       cfg.DatabaseMaxConns,
		MinConns:       cfg.DatabaseMinConns,
		Schema:         cfg.DatabaseSchema,
		ConfigModifier: pgxAdapter.ConfigModifier(),
	})
	if err != nil {
		logger.Fatal("connect database", zap.Error(err))
	}
	defer pool.Close()

	if err := runMigrations(ctx, pool, cfg.DatabaseSchema, logger); err != nil {
		logger.Fatal("run migrations", zap.Error(err))
	}

	if strings.EqualFold(cfg.RunMode, "asset-backfill") {
		if err := runAssetBackfill(ctx, cfg, pool, logger, managedObservability); err != nil {
			logger.Fatal("run asset backfill", zap.Error(err))
		}
		return
	}

	authenticator, err := httpauth.NewSandbox0Authenticator(httpauth.Sandbox0AuthenticatorConfig{
		BaseURL: cfg.Sandbox0AuthBaseURL,
		Timeout: cfg.Sandbox0Timeout,
		Logger:  logger,
	})
	if err != nil {
		logger.Fatal("create authenticator", zap.Error(err))
	}

	repo := managedagents.NewRepository(pool)
	serviceOpts := make([]managedagents.ServiceOption, 0, 1)
	runtimeCfg := managedagentsruntime.Config{
		Enabled:                cfg.RuntimeEnabled,
		TemplateID:             cfg.TemplateID,
		TemplateManifestPath:   cfg.TemplateManifestPath,
		TemplateMainImage:      cfg.TemplateMainImage,
		WrapperPort:            cfg.WrapperPort,
		WorkspaceMountPath:     cfg.WorkspaceMountPath,
		SandboxTTLSeconds:      cfg.SandboxTTLSeconds,
		SandboxRequestTimeout:  cfg.Sandbox0Timeout,
		SandboxBaseURL:         cfg.Sandbox0BaseURL,
		SandboxExposureBaseURL: cfg.Sandbox0ExposureBaseURL,
		SandboxAdminAPIKey:     cfg.Sandbox0AdminAPIKey,
		RuntimeCallbackBaseURL: cfg.RuntimeCallbackBaseURL,
		RuntimeAllowedDomains:  cfg.RuntimeAllowedDomains,
	}.WithDefaults(0)
	observableHTTPClient := httpAdapter.NewClient(httpobs.Config{Timeout: cfg.Sandbox0Timeout})
	runtimeManager, err := managedagentsruntime.NewSDKRuntimeManager(repo, runtimeCfg, logger,
		managedagentsruntime.WithObservability(managedObservability),
		managedagentsruntime.WithHTTPClient(observableHTTPClient),
	)
	if err != nil {
		logger.Fatal("create runtime manager", zap.Error(err))
	}
	runtimeManager.StartManagedTemplateSync(ctx)
	runtimeManager.StartRuntimeLifecycleWorker(ctx)
	serviceOpts = append(serviceOpts, managedagents.WithAssetStore(managedagentsruntime.NewVolumeAssetStore(cfg.Sandbox0BaseURL, cfg.Sandbox0Timeout, cfg.Sandbox0AdminAPIKey)))
	serviceOpts = append(serviceOpts, managedagents.WithObservability(managedObservability))
	service := managedagents.NewService(repo, runtimeManager, logger, serviceOpts...)
	service.StartRuntimeWebhookWorker(ctx)
	handler := managedagents.NewHandler(service, logger)

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(httpobs.GinMiddleware(httpAdapter.ServerConfig(logger)))
	router.Use(gin.Recovery())
	router.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	router.GET("/readyz", func(c *gin.Context) {
		if err := pool.Ping(c.Request.Context()); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	if metricsEnabled {
		router.GET("/metrics", gin.WrapH(promhttp.HandlerFor(prometheus.DefaultGatherer, promhttp.HandlerOpts{})))
	}
	managedagents.MountRoutes(router, authenticator, handler)

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 15 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("starting agent-gateway",
			zap.String("addr", cfg.HTTPAddr),
			zap.String("sandbox0_base_url", cfg.Sandbox0BaseURL),
			zap.String("sandbox0_auth_base_url", cfg.Sandbox0AuthBaseURL),
			zap.String("sandbox0_exposure_base_url", cfg.Sandbox0ExposureBaseURL),
			zap.Bool("observability_enabled", cfg.ObservabilityEnabled),
			zap.Bool("metrics_enabled", metricsEnabled),
			zap.String("trace_exporter", cfg.TraceExporter),
		)
		errCh <- server.ListenAndServe()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logger.Info("received shutdown signal", zap.String("signal", sig.String()))
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			logger.Fatal("server exited", zap.Error(err))
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown failed", zap.Error(err))
	}
}

func loadConfig() (config, error) {
	sandbox0BaseURL := trimURL(envOrDefault("MANAGED_AGENT_SANDBOX0_BASE_URL", defaultSandbox0BaseURL))
	cfg := config{
		RunMode:                 strings.ToLower(envOrDefault("MANAGED_AGENT_RUN_MODE", "server")),
		HTTPAddr:                envOrDefault("MANAGED_AGENT_HTTP_ADDR", ":8080"),
		LogLevel:                envOrDefault("MANAGED_AGENT_LOG_LEVEL", "info"),
		DatabaseURL:             strings.TrimSpace(os.Getenv("MANAGED_AGENT_DATABASE_URL")),
		DatabaseSchema:          envOrDefault("MANAGED_AGENT_DATABASE_SCHEMA", "managed_agent"),
		DatabaseMaxConns:        int32(envInt("MANAGED_AGENT_DATABASE_MAX_CONNS", 10)),
		DatabaseMinConns:        int32(envInt("MANAGED_AGENT_DATABASE_MIN_CONNS", 1)),
		Sandbox0BaseURL:         sandbox0BaseURL,
		Sandbox0AuthBaseURL:     trimURL(envOrDefault("MANAGED_AGENT_SANDBOX0_AUTH_BASE_URL", sandbox0BaseURL)),
		Sandbox0ExposureBaseURL: trimURL(os.Getenv("MANAGED_AGENT_SANDBOX0_EXPOSURE_BASE_URL")),
		Sandbox0AdminAPIKey:     strings.TrimSpace(os.Getenv("MANAGED_AGENT_SANDBOX0_ADMIN_API_KEY")),
		RuntimeCallbackBaseURL:  strings.TrimRight(strings.TrimSpace(os.Getenv("MANAGED_AGENT_RUNTIME_CALLBACK_BASE_URL")), "/"),
		Sandbox0Timeout:         envDuration("MANAGED_AGENT_SANDBOX0_TIMEOUT", 90*time.Second),
		RuntimeEnabled:          !strings.EqualFold(strings.TrimSpace(os.Getenv("MANAGED_AGENT_RUNTIME_ENABLED")), "false"),
		RuntimeAllowedDomains:   envStringList("MANAGED_AGENT_RUNTIME_ALLOWED_DOMAINS"),
		TemplateID:              firstEnv("MANAGED_AGENT_TEMPLATE_ID", "MANAGED_AGENT_CLAUDE_TEMPLATE"),
		TemplateManifestPath:    strings.TrimSpace(os.Getenv("MANAGED_AGENT_TEMPLATE_MANIFEST_PATH")),
		TemplateMainImage:       strings.TrimSpace(os.Getenv("MANAGED_AGENT_TEMPLATE_MAIN_IMAGE")),
		WrapperPort:             envInt("MANAGED_AGENT_WRAPPER_PORT", 8080),
		WorkspaceMountPath:      strings.TrimSpace(os.Getenv("MANAGED_AGENT_WORKSPACE_MOUNT_PATH")),
		SandboxTTLSeconds:       envInt("MANAGED_AGENT_SANDBOX_TTL_SECONDS", managedagentsruntime.DefaultSandboxTTLSeconds),
		ObservabilityEnabled:    envBool("MANAGED_AGENT_OBSERVABILITY_ENABLED", true),
		MetricsEnabled:          envBool("MANAGED_AGENT_METRICS_ENABLED", true),
		TraceExporter:           strings.ToLower(envOrDefault("MANAGED_AGENT_TRACE_EXPORTER", "noop")),
		TraceOTLPEndpoint:       strings.TrimSpace(os.Getenv("MANAGED_AGENT_TRACE_OTLP_ENDPOINT")),
		TraceOTLPInsecure:       envBool("MANAGED_AGENT_TRACE_OTLP_INSECURE", true),
		TraceSampleRate:         envFloat("MANAGED_AGENT_TRACE_SAMPLE_RATE", 1),
		BackfillDryRun:          envBool("MANAGED_AGENT_BACKFILL_DRY_RUN", true),
		BackfillFiles:           envBool("MANAGED_AGENT_BACKFILL_FILES", true),
		BackfillSkills:          envBool("MANAGED_AGENT_BACKFILL_SKILLS", true),
		BackfillBatchSize:       envInt("MANAGED_AGENT_BACKFILL_BATCH_SIZE", 100),
		BackfillMaxItems:        envInt("MANAGED_AGENT_BACKFILL_MAX_ITEMS", 0),
		BackfillTeamID:          strings.TrimSpace(os.Getenv("MANAGED_AGENT_BACKFILL_TEAM_ID")),
	}
	if cfg.TraceExporter == "noop" && cfg.TraceOTLPEndpoint != "" {
		cfg.TraceExporter = "otlp"
	}
	if cfg.DatabaseURL == "" {
		return config{}, fmt.Errorf("MANAGED_AGENT_DATABASE_URL is required")
	}
	switch cfg.RunMode {
	case "", "server", "asset-backfill":
	default:
		return config{}, fmt.Errorf("unsupported MANAGED_AGENT_RUN_MODE %q", cfg.RunMode)
	}
	if cfg.RunMode == "asset-backfill" {
		if cfg.Sandbox0AdminAPIKey == "" {
			return config{}, fmt.Errorf("MANAGED_AGENT_SANDBOX0_ADMIN_API_KEY is required for asset backfill")
		}
		if cfg.BackfillBatchSize <= 0 {
			return config{}, fmt.Errorf("MANAGED_AGENT_BACKFILL_BATCH_SIZE must be greater than zero")
		}
		if cfg.BackfillMaxItems < 0 {
			return config{}, fmt.Errorf("MANAGED_AGENT_BACKFILL_MAX_ITEMS must be zero or greater")
		}
		if !cfg.BackfillFiles && !cfg.BackfillSkills {
			return config{}, fmt.Errorf("at least one of MANAGED_AGENT_BACKFILL_FILES or MANAGED_AGENT_BACKFILL_SKILLS must be true")
		}
	}
	return cfg, nil
}

func runAssetBackfill(ctx context.Context, cfg config, pool *pgxpool.Pool, logger *zap.Logger, observability *managedagents.Observability) error {
	repo := managedagents.NewRepository(pool)
	service := managedagents.NewService(
		repo,
		nil,
		logger,
		managedagents.WithAssetStore(managedagentsruntime.NewVolumeAssetStore(cfg.Sandbox0BaseURL, cfg.Sandbox0Timeout, cfg.Sandbox0AdminAPIKey)),
		managedagents.WithObservability(observability),
	)
	summary, err := service.BackfillTeamAssetStore(ctx, managedagents.RequestCredential{}, managedagents.AssetBackfillOptions{
		DryRun:    cfg.BackfillDryRun,
		Files:     cfg.BackfillFiles,
		Skills:    cfg.BackfillSkills,
		TeamID:    cfg.BackfillTeamID,
		BatchSize: cfg.BackfillBatchSize,
		MaxItems:  cfg.BackfillMaxItems,
	})
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("encode asset backfill summary: %w", err)
	}
	logger.Info("asset backfill completed", zap.ByteString("summary", encoded))
	if summary.Files.Failed > 0 || summary.Skills.Failed > 0 {
		return errors.New("asset backfill completed with failures")
	}
	return nil
}

func initTracing(ctx context.Context, cfg config, logger *zap.Logger) (trace.Tracer, func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	if !cfg.ObservabilityEnabled {
		return noop.NewTracerProvider().Tracer(observabilityServiceName), func(context.Context) error { return nil }, nil
	}

	exporterName := strings.TrimSpace(cfg.TraceExporter)
	if exporterName == "" {
		exporterName = "noop"
	}
	var exporter sdktrace.SpanExporter
	switch exporterName {
	case "noop":
	case "otlp":
		opts := []otlptracegrpc.Option{}
		if cfg.TraceOTLPEndpoint != "" {
			opts = append(opts, otlptracegrpc.WithEndpoint(cfg.TraceOTLPEndpoint))
		}
		if cfg.TraceOTLPInsecure {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}
		created, err := otlptracegrpc.New(ctx, opts...)
		if err != nil {
			return nil, nil, fmt.Errorf("create otlp trace exporter: %w", err)
		}
		exporter = created
	default:
		return nil, nil, fmt.Errorf("unsupported MANAGED_AGENT_TRACE_EXPORTER %q", cfg.TraceExporter)
	}
	sampleRate := cfg.TraceSampleRate
	if sampleRate < 0 {
		sampleRate = 0
	}
	if sampleRate > 1 {
		sampleRate = 1
	}
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		"",
		semconv.ServiceName(observabilityServiceName),
	))
	if err != nil {
		return nil, nil, fmt.Errorf("create trace resource: %w", err)
	}
	providerOpts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sampleRate))),
	}
	if exporter != nil {
		providerOpts = append(providerOpts, sdktrace.WithBatcher(exporter))
	}
	provider := sdktrace.NewTracerProvider(providerOpts...)
	otel.SetTracerProvider(provider)
	logger.Info("tracing configured",
		zap.String("exporter", exporterName),
		zap.Float64("sample_rate", sampleRate),
		zap.String("otlp_endpoint", cfg.TraceOTLPEndpoint),
	)
	return provider.Tracer(observabilityServiceName), provider.Shutdown, nil
}

func initLogger(level string) (*zap.Logger, error) {
	var logLevel zapcore.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		logLevel = zapcore.DebugLevel
	case "warn":
		logLevel = zapcore.WarnLevel
	case "error":
		logLevel = zapcore.ErrorLevel
	default:
		logLevel = zapcore.InfoLevel
	}

	config := zap.Config{
		Level:       zap.NewAtomicLevelAt(logLevel),
		Development: false,
		Encoding:    "json",
		EncoderConfig: zapcore.EncoderConfig{
			TimeKey:        "ts",
			LevelKey:       "level",
			NameKey:        "logger",
			CallerKey:      "caller",
			FunctionKey:    zapcore.OmitKey,
			MessageKey:     "msg",
			StacktraceKey:  "stacktrace",
			LineEnding:     zapcore.DefaultLineEnding,
			EncodeLevel:    zapcore.LowercaseLevelEncoder,
			EncodeTime:     zapcore.ISO8601TimeEncoder,
			EncodeDuration: zapcore.SecondsDurationEncoder,
			EncodeCaller:   zapcore.ShortCallerEncoder,
		},
		OutputPaths:      []string{"stdout"},
		ErrorOutputPaths: []string{"stderr"},
	}

	return config.Build()
}

func runMigrations(ctx context.Context, pool *pgxpool.Pool, schema string, logger *zap.Logger) error {
	return migrate.Up(ctx, pool, ".",
		migrate.WithBaseFS(managedagentmigrations.FS),
		migrate.WithSchema(schema),
		migrate.WithLogger(&zapLogger{logger: logger}),
	)
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func trimURL(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), "/")
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func envStringList(key string) []string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil
	}
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\t'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envBool(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	switch value {
	case "1", "true", "t", "yes", "y", "on":
		return true
	case "0", "false", "f", "no", "n", "off":
		return false
	default:
		return fallback
	}
}

func envFloat(key string, fallback float64) float64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

type zapLogger struct {
	logger *zap.Logger
}

func (z *zapLogger) Printf(format string, args ...any) {
	z.logger.Info(fmt.Sprintf(format, args...))
}

func (z *zapLogger) Fatalf(format string, args ...any) {
	z.logger.Fatal(fmt.Sprintf(format, args...))
}
