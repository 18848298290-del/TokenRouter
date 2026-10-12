//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// backupAssemblyProviders groups Wire providers for the backup module.
var backupAssemblyProviders = wire.NewSet(
	provideBackup,
	provideBackupHTTP,
	provideDataManagementHTTP,
)
