package provider

import (
	"context"
	"strings"
	"time"
)

// Antigravity privacy setup clears upstream user settings before fetching their
// resulting state for the account's project. Both response checks treat absence
// of telemetryEnabled as the privacy criterion, so unrelated settings may remain
// in an accepted response. The setting response must be nonnil, while the fetch
// predicate also accepts a nil response.
//
// The setting and verification calls share a ten-second context deadline. Project
// eligibility is checked after the setting request, so an upstream change can
// precede a failure to verify when the project ID is blank. Request errors and
// failed predicates become the privacy failure marker. PrivacyService persists
// the mode through identity-checked writes, while OAuth exchange returns it as
// account metadata for its caller.

// SetPrivacy applies Antigravity privacy settings and reports the mode selected
// by the setting and verification response predicates. Empty accessToken returns
// empty. With a token, the receiver needs a
// configured factory returning a usable client, logging callbacks, and a nonnil
// parent context.
//
// SetUserSettings must return a nonnil response when it reports no error; a nil
// response panics in failure logging. Its response must lack telemetryEnabled
// before FetchUserInfo is attempted with a nonblank project ID. Other settings are
// accepted, and the fetch predicate treats a nil response as private. A shared
// ten-second context bounds cooperative requests; failures return
// AntigravityPrivacyFailed after any upstream setting side effect already made.
func (s *AntigravityAuthorization) SetPrivacy(ctx context.Context, accessToken, projectID, proxyURL string) string {
	if accessToken == "" {
		return ""
	}

	// Share one request budget across setting and verification so the second call
	// uses the time remaining after the first, with cancellation released on return.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	client, err := s.Options.NewClient(proxyURL)
	if err != nil {
		// Client construction failed, so no setting request is attempted. Return the
		// failure mode for surrounding privacy maintenance to retain as its observation.
		s.Options.Warn("antigravity_privacy_client_error", "error", err.Error())
		return AntigravityPrivacyFailed
	}

	// Clear settings first, then require the response's telemetryEnabled key to be
	// absent before fetching verification. Other returned settings do not reject this step.
	setResp, err := client.SetUserSettings(ctx, accessToken)
	if err != nil {
		// A setting error leaves the upstream result unconfirmed. Return a failure
		// mode and skip verification rather than declaring the account's privacy accepted.
		s.Options.Warn("antigravity_privacy_set_failed", "error", err.Error())
		return AntigravityPrivacyFailed
	}
	// A telemetryEnabled key rejects the setting result even when its value is
	// false. The client must supply a nonnil response for this diagnostic path.
	if !setResp.IsSuccess() {
		s.Options.Warn("antigravity_privacy_set_response_not_empty",
			"user_settings", setResp.UserSettings,
		)
		return AntigravityPrivacyFailed
	}

	// Verification requires a project even though clearing settings does not.
	// A missing project reports failure after the setting side effect, with no rollback.
	if strings.TrimSpace(projectID) == "" {
		s.Options.Warn("antigravity_privacy_missing_project_id")
		return AntigravityPrivacyFailed
	}
	userInfo, err := client.FetchUserInfo(ctx, accessToken, projectID)
	if err != nil {
		// Settings may already have changed, but a failed fetch cannot confirm the
		// privacy result. Report that incomplete verification through the failure mode.
		s.Options.Warn("antigravity_privacy_verify_failed", "error", err.Error())
		return AntigravityPrivacyFailed
	}
	// Verification also checks key absence, rather than the telemetry value.
	// A present key records a failed observation despite the earlier accepted setting response.
	if !userInfo.IsPrivate() {
		s.Options.Warn("antigravity_privacy_verify_not_private",
			"user_settings", userInfo.UserSettings,
		)
		return AntigravityPrivacyFailed
	}

	s.Options.Info("antigravity_privacy_set_success")
	return AntigravityPrivacySet
}
