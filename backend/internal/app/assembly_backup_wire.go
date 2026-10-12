//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// Backup HTTP operations use the application backup service and its archive
// resources. Data-management HTTP constructs a separate service; grouping both
// constructors lets the administration routes resolve their respective
// dependencies through the same application graph.

// backupAssemblyProviders supplies the backup service and the constructors for
// backup and data-management HTTP administration.
var backupAssemblyProviders = wire.NewSet(
	provideBackup,
	provideBackupHTTP,
	provideDataManagementHTTP,
)
