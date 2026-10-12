package provider

import (
	"context"
	"time"
)

const (
	googleConfigErrorCooldown = time.Minute
	emptyResponseCooldown     = time.Minute
)

// TemporaryFailureStore writes temporary scheduling suspensions.
type TemporaryFailureStore interface {
	SetTempUnschedulable(context.Context, int64, time.Time, string) error
}

// TempUnscheduleGoogleConfigError attempts a one-minute suspension after a Google configuration error.
// Persistence failures are logged; successful suspensions exclude the provider from scheduling.
func TempUnscheduleGoogleConfigError(ctx context.Context, repo TemporaryFailureStore, providerID int64, logPrefix string, logf func(string, ...any)) {
	until := time.Now().Add(googleConfigErrorCooldown)
	reason := "400: invalid project resource name (auto temp-unschedule 1m)"
	if err := repo.SetTempUnschedulable(ctx, providerID, until, reason); err != nil {
		logf("%s temp_unschedule_failed provider=%d error=%v", logPrefix, providerID, err)
	} else {
		logf("%s temp_unscheduled provider=%d until=%v reason=%q", logPrefix, providerID, until.Format("15:04:05"), reason)
	}
}

// TempUnscheduleEmptyResponse attempts a one-minute suspension after an empty stream response.
// Persistence failures are logged; successful suspensions exclude the provider from scheduling.
func TempUnscheduleEmptyResponse(ctx context.Context, repo TemporaryFailureStore, providerID int64, logPrefix string, logf func(string, ...any)) {
	until := time.Now().Add(emptyResponseCooldown)
	reason := "empty stream response (auto temp-unschedule 1m)"
	if err := repo.SetTempUnschedulable(ctx, providerID, until, reason); err != nil {
		logf("%s temp_unschedule_failed provider=%d error=%v", logPrefix, providerID, err)
	} else {
		logf("%s temp_unscheduled provider=%d until=%v reason=%q", logPrefix, providerID, until.Format("15:04:05"), reason)
	}
}
