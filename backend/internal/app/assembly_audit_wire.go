//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// Administrator operations and session binding mismatches feed the same
// AuditLogService. Provider credential names and sensitive payment
// configuration fields contribute to the HTTP middleware's shared redaction
// rules, and administrator queries read records from that service.

// auditAssemblyProviders supplies audit recording, query handlers, settings,
// and redaction dependencies to initializeApplication.
var auditAssemblyProviders = wire.NewSet(
	provideAuditSettings,
	provideAuditRepository,
	provideAuditService,
	provideAuditHTTP,
	provideAuditMiddleware,
	provideAuditRedactor,
)
