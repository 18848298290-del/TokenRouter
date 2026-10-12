package main

//go:generate go run github.com/google/wire/cmd/wire ../../internal/app

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	"github.com/TokenFlux/TokenRouter/internal/app"
	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/setup"
	"github.com/TokenFlux/TokenRouter/internal/web"
)

var (
	//go:embed VERSION
	embeddedVersion string

	// Build-time variables (can be set by ldflags).
	Version   = ""
	Commit    = "unknown"
	Date      = "unknown"
	BuildType = "source" // "source" for manual builds, "release" for CI builds (set by ldflags)
)

func init() {
	// Keep the version supplied through ldflags, such as -X main.Version=...
	if strings.TrimSpace(Version) != "" {
		return
	}

	// Use the VERSION file embedded at build time when ldflags supplies no version.
	Version = strings.TrimSpace(embeddedVersion)
	if Version == "" {
		Version = "0.0.0-dev"
	}
}

// main initializes bootstrap logging and exits with a nonzero status when the server fails.
func main() {
	logging.InitBootstrap()
	err := run()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Server failed: %v\n", err)
		os.Exit(1)
	}
}

// run returns to main after cleanup so the main goroutine controls process exit.
func run() (err error) {
	syncOnReturn := true
	defer func() {
		if syncOnReturn {
			err = errors.Join(err, syncBootstrapLogs())
		}
	}()
	setupMode := flag.Bool("setup", false, "Run setup wizard in CLI mode")
	showVersion := flag.Bool("version", false, "Show version information")
	flag.Parse()
	if *showVersion {
		log.Printf("TokenRouter %s (commit: %s, built: %s)\n", Version, Commit, Date)
		return nil
	}
	if *setupMode {
		return setup.RunCLI()
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	restarter := lifecycle.NewRestarter(runtime.GOOS, stop)
	defer restarter.Close()
	if setup.NeedsSetup() {
		if setup.AutoSetupEnabled() {
			log.Println("Auto setup mode enabled...")
			if err := setup.AutoSetupFromEnv(); err != nil {
				return fmt.Errorf("auto setup failed: %w", err)
			}
		} else {
			log.Println("First run detected, starting setup wizard...")
			syncOnReturn = false
			return runSetupServer(ctx, restarter)
		}
	}
	syncOnReturn = false
	return runMainServer(ctx, restarter)
}

func runSetupServer(ctx context.Context, restarter *lifecycle.Restarter) (err error) {
	manager := lifecycle.New()
	manager.Register(lifecycle.Hook{Name: "SetupLogs", StopOrder: 1000, Stop: func(context.Context) error { logging.Sync(); return nil }})
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err = errors.Join(err, manager.Stop(cleanupCtx))
	}()
	r := gin.New()
	r.Use(middleware.Recovery())
	r.Use(middleware.CORS(config.CORSConfig{}))
	r.Use(middleware.SecurityHeaders(config.CSPConfig{Enabled: true, Policy: config.DefaultCSPPolicy}, nil))

	setup.RegisterRoutes(r, restarter)

	if web.HasEmbeddedFrontend() {
		r.Use(web.ServeEmbeddedFrontend())
	}

	// Setup uses config.yaml or SERVER_HOST and SERVER_PORT for its listen address.
	addr := config.GetServerAddress()
	log.Printf("Setup wizard available at http://%s", addr)
	log.Println("Complete the setup wizard to configure TokenRouter")

	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	server := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
		Protocols:         protocols,
	}

	lifecycle.TrackRequests(server, manager)
	return lifecycle.Serve(ctx, server)
}

// runMainServer constructs the application and coordinates startup and shutdown.
// @project-doc docs/architecture/system_architecture.md#startup_and_shutdown
func runMainServer(ctx context.Context, restarter *lifecycle.Restarter) (err error) {
	syncOnReturn := true
	defer func() {
		if syncOnReturn {
			err = errors.Join(err, syncBootstrapLogs())
		}
	}()
	cfg, err := config.LoadForBootstrap()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := logging.Init(app.OptionsFromConfig(cfg.Log)); err != nil {
		return fmt.Errorf("initialize logger: %w", err)
	}
	syncOnReturn = false
	application, err := app.Initialize(ctx, cfg, app.BuildInfo{Version: Version, BuildType: BuildType}, restarter)
	if err != nil {
		return fmt.Errorf("initialize application: %w", err)
	}
	return application.Run(ctx)
}

// syncBootstrapLogs bounds log flushing for entry points that do not construct the full application.
func syncBootstrapLogs() error {
	manager := lifecycle.New()
	manager.Register(lifecycle.Hook{Name: "BootstrapLogs", Stop: func(context.Context) error { logging.Sync(); return nil }})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return manager.Stop(ctx)
}
