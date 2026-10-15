//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// assembly_notification_wire.go registers notification delivery, mail, and
// identity email-queue constructors with Wire.
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
