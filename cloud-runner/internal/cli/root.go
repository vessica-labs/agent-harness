package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/events"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/sandbox"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/scheduler"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/secure"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/server"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/store"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/ui"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/worker"
)

// Version is set from the release tag at build time.
var Version = "dev"

func Run(args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "server":
		return runServer(ctx)
	case "worker":
		return runWorker(ctx)
	case "cloud":
		return runCloud(ctx, args[1:])
	case "railway":
		return runRailway(ctx, args[1:])
	case "ui":
		return runUI(ctx, args[1:])
	case "version", "--version", "-v":
		fmt.Println(versionString())
		return nil
	case "help", "--help", "-h":
		fmt.Println(usage())
		return nil
	default:
		return usageError()
	}
}

func versionString() string {
	return "agent-harness " + Version
}

func runServer(ctx context.Context) error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	config, err := server.ConfigFromEnv()
	if err != nil {
		return err
	}
	box, err := secure.NewBox(os.Getenv("HARNESS_CREDENTIAL_KEY"))
	if err != nil {
		return err
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	values, err := store.OpenPostgres(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer values.Close()
	if err := values.Migrate(ctx); err != nil {
		return err
	}
	broker := events.NewBroker()
	control := server.New(config, values, box, broker, logger)
	schedulerEnabled := !strings.EqualFold(os.Getenv("HARNESS_SCHEDULER_ENABLED"), "false")
	if schedulerEnabled {
		project, environment, token := os.Getenv("HARNESS_RAILWAY_PROJECT"), os.Getenv("HARNESS_RAILWAY_ENVIRONMENT"), os.Getenv("RAILWAY_API_TOKEN")
		if project == "" || environment == "" || token == "" || config.PublicURL == "" {
			return errors.New("scheduler requires HARNESS_RAILWAY_PROJECT, HARNESS_RAILWAY_ENVIRONMENT, RAILWAY_API_TOKEN, and HARNESS_PUBLIC_URL")
		}
		if !strings.HasPrefix(strings.ToLower(config.PublicURL), "https://") {
			return errors.New("HARNESS_PUBLIC_URL must use HTTPS when the scheduler is enabled")
		}
		provider := sandbox.RailwayCLI{Binary: envDefault("HARNESS_RAILWAY_BINARY", "railway"), Project: project,
			Environment: environment, APIToken: token, WorkerPath: envDefault("HARNESS_WORKER_PATH", "agent-harness")}
		schedule := scheduler.New(values, provider, box, broker, scheduler.Config{
			Owner: envDefault("RAILWAY_REPLICA_ID", "control-plane"), ControlPlaneURL: config.PublicURL,
			Checkpoint: os.Getenv("HARNESS_SANDBOX_CHECKPOINT"), MaxActiveRuns: envInt("HARNESS_MAX_ACTIVE_RUNS", 3),
			CodexModel:        envDefault("HARNESS_CODEX_MODEL", "gpt-5.3-codex"),
			PlaywrightWorkers: envInt("HARNESS_PLAYWRIGHT_WORKERS", 2),
		}, logger)
		go schedule.Run(ctx)
	}
	errChannel := make(chan error, 1)
	go func() { errChannel <- control.ListenAndServe() }()
	select {
	case err := <-errChannel:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return control.Shutdown(shutdown)
	}
}

func runWorker(ctx context.Context) error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	config, err := worker.ConfigFromEnv()
	if err != nil {
		return err
	}
	return worker.New(config, logger).Run(ctx)
}

func runUI(ctx context.Context, args []string) error {
	address, profile := "127.0.0.1:7373", ""
	for index := 0; index < len(args); index++ {
		if args[index] == "--address" && index+1 < len(args) {
			index++
			address = args[index]
		}
		if args[index] == "--profile" && index+1 < len(args) {
			index++
			profile = args[index]
		}
	}
	url, token, err := loadProfile(profile)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	local, err := ui.New(address, url, token, logger)
	if err != nil {
		return err
	}
	errChannel := make(chan error, 1)
	go func() { errChannel <- local.ListenAndServe() }()
	select {
	case err := <-errChannel:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return local.Shutdown(shutdown)
	}
}

func envDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(key))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func usageError() error { return errors.New(usage()) }
func usage() string     { return `usage: agent-harness <server|worker|cloud|railway|ui> [arguments]` }
