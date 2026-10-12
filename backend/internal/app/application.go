package app

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// BuildInfo carries build metadata from the process entry point to application modules.
type BuildInfo struct {
	Version   string
	BuildType string
}

// Application owns the HTTP server and the lifecycle of its dependencies.
type Application struct {
	Server    *http.Server
	lifecycle *lifecycle.Manager
}

// Initialize constructs application dependencies and releases acquired resources on failure. Run starts the
// workers.
// @project-doc docs/architecture/system_architecture.md#dependency_layers
func Initialize(ctx context.Context, cfg *config.Config, buildInfo BuildInfo, restarter *lifecycle.Restarter) (_ *Application, err error) {
	manager := lifecycle.New(func(name, event string, err error) {
		if name == "LogBackend" && event == "stopped" {
			return
		}
		// Use the operation result for severity; task names are log data.
		if err != nil {
			logging.S().Errorf("[Lifecycle] %s %s err=%v", event, name, err)
		} else {
			logging.S().Infof("[Lifecycle] %s %s err=<nil>", event, name)
		}
	})
	manager.Register(lifecycle.Hook{Name: "LogBackend", StartOrder: -2000, StopOrder: 1000, Stop: func(context.Context) error { logging.Sync(); return logging.CloseFiles() }})
	tasks := installBackgroundTasks(manager)
	defer func() {
		if err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err = errors.Join(err, manager.Rollback(cleanupCtx))
		}
	}()
	return initializeApplication(ctx, cfg, buildInfo, manager, restarter, tasks)
}

// provideApplication registers shutdown hooks for HTTP requests and Ops WebSockets.
func provideApplication(
	server *http.Server,
	manager *lifecycle.Manager,
	_ *runtimeReady,
	opsService *ops.OpsService,
	_ *ops.ErrorLogQueue,
) *Application {
	lifecycle.TrackRequests(server, manager)
	manager.Register(lifecycle.Hook{
		Name:       "OpsWSRuntime",
		StartOrder: 983,
		StopOrder:  17,
		Stop: func(context.Context) error {
			opsService.Realtime().Stop()
			return nil
		},
	})
	return &Application{Server: server, lifecycle: manager}
}

// installBackgroundTasks registers the task tracker shared by application modules.
func installBackgroundTasks(manager *lifecycle.Manager) *lifecycle.Tasks {
	tasks := lifecycle.NewTasks()
	manager.Register(lifecycle.Hook{
		Name:       "ApplicationBackgroundTasks",
		StartOrder: 932,
		StopOrder:  68,
		Stop:       tasks.Stop,
	})
	return tasks
}

// Run starts background tasks before serving HTTP and bounds resource cleanup before returning.
// @project-doc docs/architecture/system_architecture.md#startup_and_shutdown
func (a *Application) Run(ctx context.Context) (err error) {
	started := false
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if started {
			err = errors.Join(err, a.lifecycle.Stop(cleanupCtx))
		} else {
			err = errors.Join(err, a.lifecycle.Rollback(cleanupCtx))
		}
	}()
	result := make(chan error, 1)
	go func() { result <- a.lifecycle.Start(ctx) }()
	select {
	case err = <-result:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	started = true
	return lifecycle.Serve(ctx, a.Server)
}

// Cleanup releases resources for applications that have not run and for external tests.
func (a *Application) Cleanup(ctx context.Context) error { return a.lifecycle.Stop(ctx) }
