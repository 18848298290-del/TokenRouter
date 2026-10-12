//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// notificationAssemblyProviders groups Wire providers for the notification module.
var notificationAssemblyProviders = wire.NewSet(
	provideRiskDelivery,
	provideAlertDelivery,
	provideMailer,
	provideNotification,
	provideEmailQueue,
	provideNotificationHTTP,
)
