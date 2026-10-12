package authconfig

import "errors"

// DingTalk authorization requires an internal application kind whenever login is
// enabled. The internal_only organization policy also requires the internal app
// type; disabled settings can remain incomplete while administrators prepare them.

var (
	// These errors identify separate application-kind and organization-policy
	// failures so startup and runtime settings validation can report the violated rule.
	ErrDingTalkV1AppTypeMismatch = errors.New("dingtalk: internal_only requires app_type=internal")
	ErrDingTalkV4InvalidAppKind  = errors.New("dingtalk: dingtalk_app_kind must be internal_app")
)

// ValidateDingTalkConfig checks the application rules used by enabled DingTalk
// login. Disabled values are accepted; enabled values require internal_app, and
// internal_only additionally requires AppType internal. OAuthSettings checks
// credentials and endpoint shape; DingTalkHandler checks organization membership
// through the application-token directory lookup.
func ValidateDingTalkConfig(cfg DingTalkConnectConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if cfg.DingTalkAppKind != "internal_app" {
		return ErrDingTalkV4InvalidAppKind
	}
	if cfg.CorpRestrictionPolicy == "internal_only" {
		if cfg.AppType != "internal" {
			return ErrDingTalkV1AppTypeMismatch
		}
	}
	return nil
}
