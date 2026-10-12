package provider

import (
	"context"
	"strings"
	"time"
)

// SetPrivacy applies Antigravity privacy settings and verifies the result.
// The API sequence is:
//  1. Clear settings with setUserSettings and require an empty userSettings response.
//  2. Verify privacy with fetchUserInfo, which requires project_id.
//
// It returns privacy_set on success, privacy_set_failed on failure, or an empty string when accessToken is empty.
func (s *AntigravityAuthorization) SetPrivacy(ctx context.Context, accessToken, projectID, proxyURL string) string {
	if accessToken == "" {
		return ""
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	client, err := s.Options.NewClient(proxyURL)
	if err != nil {
		s.Options.Warn("antigravity_privacy_client_error", "error", err.Error())
		return AntigravityPrivacyFailed
	}

	// Require a successful empty-settings response before verifying privacy.
	setResp, err := client.SetUserSettings(ctx, accessToken)
	if err != nil {
		s.Options.Warn("antigravity_privacy_set_failed", "error", err.Error())
		return AntigravityPrivacyFailed
	}
	if !setResp.IsSuccess() {
		s.Options.Warn("antigravity_privacy_set_response_not_empty",
			"user_settings", setResp.UserSettings,
		)
		return AntigravityPrivacyFailed
	}

	// Verify the applied settings using the account's project ID.
	if strings.TrimSpace(projectID) == "" {
		s.Options.Warn("antigravity_privacy_missing_project_id")
		return AntigravityPrivacyFailed
	}
	userInfo, err := client.FetchUserInfo(ctx, accessToken, projectID)
	if err != nil {
		s.Options.Warn("antigravity_privacy_verify_failed", "error", err.Error())
		return AntigravityPrivacyFailed
	}
	if !userInfo.IsPrivate() {
		s.Options.Warn("antigravity_privacy_verify_not_private",
			"user_settings", userInfo.UserSettings,
		)
		return AntigravityPrivacyFailed
	}

	s.Options.Info("antigravity_privacy_set_success")
	return AntigravityPrivacySet
}
