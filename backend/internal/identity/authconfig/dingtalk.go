package authconfig

import "errors"

var (
	ErrDingTalkV1AppTypeMismatch = errors.New("dingtalk: internal_only requires app_type=internal")
	ErrDingTalkV4InvalidAppKind  = errors.New("dingtalk: dingtalk_app_kind must be internal_app")
)

// ValidateDingTalkConfig accepts disabled configurations and validates the app kind
// and organization restriction of enabled configurations.
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
