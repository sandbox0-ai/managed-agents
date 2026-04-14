package main

import (
	"context"
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
	"github.com/sandbox0-ai/managed-agent/internal/dbpool"
	"github.com/sandbox0-ai/managed-agent/internal/httpauth"
	"github.com/sandbox0-ai/managed-agent/internal/managedagents"
	managedagentmigrations "github.com/sandbox0-ai/managed-agent/internal/managedagents/migrations"
	"github.com/sandbox0-ai/managed-agent/internal/managedagentsruntime"
	"github.com/sandbox0-ai/managed-agent/internal/migrate"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type config struct {
	HTTPAddr               string
	LogLevel               string
	DatabaseURL            string
	DatabaseSchema         string
	DatabaseMaxConns       int32
	DatabaseMinConns       int32
	Sandbox0BaseURL        string
	Sandbox0AdminAPIKey    string
	RuntimeCallbackBaseURL string
	Sandbox0Timeout        time.Duration
	RuntimeEnabled         bool
	RuntimeAllowedDomains  []string
	ClaudeTemplate         string
	TemplateManifestPath   string
	TemplateMainImage      string
	WrapperPort            int
	WorkspaceMountPath     string
	EngineStateMountPath   string
	SandboxTTLSeconds      int
	SandboxHardTTLSeconds  int
}

const defaultSandbox0BaseURL = "https://api.sandbox0.ai"

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

	pool, err := dbpool.New(ctx, dbpool.Options{
		DatabaseURL: cfg.DatabaseURL,
		MaxConns:    cfg.DatabaseMaxConns,
		MinConns:    cfg.DatabaseMinConns,
		Schema:      cfg.DatabaseSchema,
	})
	if err != nil {
		logger.Fatal("connect database", zap.Error(err))
	}
	defer pool.Close()

	if err := runMigrations(ctx, pool, cfg.DatabaseSchema, logger); err != nil {
		logger.Fatal("run migrations", zap.Error(err))
	}

	authenticator, err := httpauth.NewSandbox0Authenticator(httpauth.Sandbox0AuthenticatorConfig{
		BaseURL: cfg.Sandbox0BaseURL,
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
		ClaudeTemplate:         cfg.ClaudeTemplate,
		TemplateManifestPath:   cfg.TemplateManifestPath,
		TemplateMainImage:      cfg.TemplateMainImage,
		WrapperPort:            cfg.WrapperPort,
		WorkspaceMountPath:     cfg.WorkspaceMountPath,
		EngineStateMountPath:   cfg.EngineStateMountPath,
		SandboxTTLSeconds:      cfg.SandboxTTLSeconds,
		SandboxHardTTLSeconds:  cfg.SandboxHardTTLSeconds,
		SandboxRequestTimeout:  cfg.Sandbox0Timeout,
		SandboxBaseURL:         cfg.Sandbox0BaseURL,
		SandboxAdminAPIKey:     cfg.Sandbox0AdminAPIKey,
		RuntimeCallbackBaseURL: cfg.RuntimeCallbackBaseURL,
		RuntimeAllowedDomains:  cfg.RuntimeAllowedDomains,
	}.WithDefaults(0)
	runtimeManager, err := managedagentsruntime.NewSDKRuntimeManager(repo, runtimeCfg, logger)
	if err != nil {
		logger.Fatal("create runtime manager", zap.Error(err))
	}
	runtimeManager.StartManagedTemplateSync(ctx)
	serviceOpts = append(serviceOpts, managedagents.WithFileStore(managedagentsruntime.NewVolumeFileStore(cfg.Sandbox0BaseURL, cfg.Sandbox0Timeout)))
	service := managedagents.NewService(repo, runtimeManager, logger, serviceOpts...)
	service.StartRuntimeWebhookWorker(ctx)
	handler := managedagents.NewHandler(service, logger)

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())
	router.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	router.GET("/readyz", func(c *gin.Context) {
		if err := pool.Ping(c.Request.Context()); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
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
	cfg := config{
		HTTPAddr:               envOrDefault("MANAGED_AGENT_HTTP_ADDR", ":8080"),
		LogLevel:               envOrDefault("MANAGED_AGENT_LOG_LEVEL", "info"),
		DatabaseURL:            strings.TrimSpace(os.Getenv("MANAGED_AGENT_DATABASE_URL")),
		DatabaseSchema:         envOrDefault("MANAGED_AGENT_DATABASE_SCHEMA", "managed_agent"),
		DatabaseMaxConns:       int32(envInt("MANAGED_AGENT_DATABASE_MAX_CONNS", 10)),
		DatabaseMinConns:       int32(envInt("MANAGED_AGENT_DATABASE_MIN_CONNS", 1)),
		Sandbox0BaseURL:        strings.TrimRight(envOrDefault("MANAGED_AGENT_SANDBOX0_BASE_URL", defaultSandbox0BaseURL), "/"),
		Sandbox0AdminAPIKey:    strings.TrimSpace(os.Getenv("MANAGED_AGENT_SANDBOX0_ADMIN_API_KEY")),
		RuntimeCallbackBaseURL: strings.TrimRight(strings.TrimSpace(os.Getenv("MANAGED_AGENT_RUNTIME_CALLBACK_BASE_URL")), "/"),
		Sandbox0Timeout:        envDuration("MANAGED_AGENT_SANDBOX0_TIMEOUT", 90*time.Second),
		RuntimeEnabled:         !strings.EqualFold(strings.TrimSpace(os.Getenv("MANAGED_AGENT_RUNTIME_ENABLED")), "false"),
		RuntimeAllowedDomains:  envStringList("MANAGED_AGENT_RUNTIME_ALLOWED_DOMAINS"),
		ClaudeTemplate:         strings.TrimSpace(os.Getenv("MANAGED_AGENT_CLAUDE_TEMPLATE")),
		TemplateManifestPath:   strings.TrimSpace(os.Getenv("MANAGED_AGENT_TEMPLATE_MANIFEST_PATH")),
		TemplateMainImage:      strings.TrimSpace(os.Getenv("MANAGED_AGENT_TEMPLATE_MAIN_IMAGE")),
		WrapperPort:            envInt("MANAGED_AGENT_WRAPPER_PORT", 8080),
		WorkspaceMountPath:     strings.TrimSpace(os.Getenv("MANAGED_AGENT_WORKSPACE_MOUNT_PATH")),
		EngineStateMountPath:   strings.TrimSpace(os.Getenv("MANAGED_AGENT_ENGINE_STATE_MOUNT_PATH")),
		SandboxTTLSeconds:      envInt("MANAGED_AGENT_SANDBOX_TTL_SECONDS", managedagentsruntime.DefaultSandboxTTLSeconds),
		SandboxHardTTLSeconds:  envInt("MANAGED_AGENT_SANDBOX_HARD_TTL_SECONDS", managedagentsruntime.DefaultSandboxHardTTLSeconds),
	}
	if cfg.DatabaseURL == "" {
		return config{}, fmt.Errorf("MANAGED_AGENT_DATABASE_URL is required")
	}
	return cfg, nil
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
