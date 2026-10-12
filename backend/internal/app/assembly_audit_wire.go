//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// auditAssemblyProviders groups Wire providers for the audit module.
var auditAssemblyProviders = wire.NewSet(
	provideAuditSettings,
	provideAuditRepository,
	provideAuditService,
	provideAuditHTTP,
	provideAuditMiddleware,
	provideAuditRedactor,
)
