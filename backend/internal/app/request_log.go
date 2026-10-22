package app

import (
	"database/sql"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/requestlog"
	"github.com/TokenFlux/TokenRouter/internal/requestlog/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/requestlog/postgres"
	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
)

// request_log.go constructs request recording, registers its writer lifecycle,
// and mounts administrator lookup routes. Request records use application SQL
// storage for batch writes and diagnostics. Lifecycle registration starts the
// writer and invokes its shutdown drain with the manager's stop context, while
// administrator query routes receive authentication, audit, and rate middleware
// through their route group.

// provideRequestLog constructs request recording over the supplied SQL pool
// and registers its Start and Stop hooks. The pool stays owned by the
// application; this registration manages writer work using cfg's retention
// setting and the request-record log component. Call before manager startup or
// shutdown, while hook registration remains available.
func provideRequestLog(db *sql.DB, cfg *config.Config, manager *lifecycle.Manager) *requestlog.Service {
	service := requestlog.NewService(postgres.NewStore(db), cfg.RequestLog.RetentionDays,
		func(format string, args ...any) { logging.LegacyPrintf("request.records", format, args...) })
	manager.Register(lifecycle.Hook{Name: "RequestRecords", StartOrder: 185, StopOrder: shutdownStageRequestAndSystemLogs, Start: service.Start, Stop: service.Stop})
	return service
}

// registerRequestLogRoutes mounts record lookup and writer health below
// /admin/requests. The supplied adminAuth, auditLog, and limiter run in that
// order for all three routes; the handler also checks the authenticated role.
func registerRequestLogRoutes(v1 *gin.RouterGroup, requests *requestlog.Service, adminAuth, auditLog, limiter gin.HandlerFunc) {
	handler := httpapi.NewHandler(requests)
	group := v1.Group("/admin/requests", adminAuth, auditLog, limiter)
	adminapi.Register(group, ManagementOperations["requests.list"], handler.Find)
	adminapi.Register(group, ManagementOperations["requests.health"], handler.Health)
	adminapi.Register(group, ManagementOperations["requests.get"], handler.Find)
}
