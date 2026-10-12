//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// Risk messages and alert templates use the application mail and notification
// services. Identity challenge emails have a separate queue built around
// EmailChallenges; Wire supplies both delivery paths and the mail
// administration handler with their respective dependencies.

// notificationAssemblyProviders supplies risk and alert delivery together with
// mail, notification, email queue, and HTTP dependencies.
var notificationAssemblyProviders = wire.NewSet(
	provideRiskDelivery,
	provideAlertDelivery,
	provideMailer,
	provideNotification,
	provideEmailQueue,
	provideNotificationHTTP,
)
