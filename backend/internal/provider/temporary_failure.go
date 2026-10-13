package provider

import (
	"context"
	"time"
)

// Exhausted configuration or empty-response retries can leave a provider on a
// short temporary pause. Both commands use the wall clock and fixed one-minute
// deadlines, reporting the storage outcome through the supplied logger while
// the caller retains its selected request result.

// These fixed durations govern short retry-exhaustion pauses for Google
// configuration errors and empty stream responses before later scheduling
// reevaluates the provider.
const (
	googleConfigErrorCooldown = time.Minute
	emptyResponseCooldown     = time.Minute
)

// TemporaryFailureStore supplies the persistent provider pause call used by
// retry-exhaustion commands.
type TemporaryFailureStore interface {
	// SetTempUnschedulable attempts the supplied deadline and diagnostic reason
	// for a provider. Its error reports incomplete storage to the caller; the
	// production writer retains an equal or longer existing pause.
	SetTempUnschedulable(context.Context, int64, time.Time, string) error
}

// TempUnscheduleGoogleConfigError attempts a one-minute pause after the caller
// selects a Google configuration failure. Supply a usable repository and
// logger; the wall clock determines the deadline, and storage success or
// failure is reported through logf.
func TempUnscheduleGoogleConfigError(ctx context.Context, repo TemporaryFailureStore, providerID int64, logPrefix string, logf func(string, ...any)) {
	until := time.Now().Add(googleConfigErrorCooldown)
	reason := "400: invalid project resource name (auto temp-unschedule 1m)"
	// Report the store outcome through logf because this helper returns no error;
	// failed pause storage leaves the caller's selected request outcome available
	// for its own response handling.
	if err := repo.SetTempUnschedulable(ctx, providerID, until, reason); err != nil {
		logf("%s temp_unschedule_failed provider=%d error=%v", logPrefix, providerID, err)
	} else {
		logf("%s temp_unscheduled provider=%d until=%v reason=%q", logPrefix, providerID, until.Format("15:04:05"), reason)
	}
}

// TempUnscheduleEmptyResponse attempts a one-minute pause after the caller
// selects an empty stream failure. Supply a usable repository and logger; the
// wall clock determines the deadline, and storage success or failure is
// reported through logf.
func TempUnscheduleEmptyResponse(ctx context.Context, repo TemporaryFailureStore, providerID int64, logPrefix string, logf func(string, ...any)) {
	until := time.Now().Add(emptyResponseCooldown)
	reason := "empty stream response (auto temp-unschedule 1m)"
	// Report the store outcome through logf because this helper returns no error;
	// failed pause storage leaves the caller's selected request outcome available
	// for its own response handling.
	if err := repo.SetTempUnschedulable(ctx, providerID, until, reason); err != nil {
		logf("%s temp_unschedule_failed provider=%d error=%v", logPrefix, providerID, err)
	} else {
		logf("%s temp_unscheduled provider=%d until=%v reason=%q", logPrefix, providerID, until.Format("15:04:05"), reason)
	}
}
