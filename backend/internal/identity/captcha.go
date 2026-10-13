package identity

import (
	"context"
)

// Authentication actions receive CAPTCHA switches and server credentials as
// one configuration value. AuthService chooses a provider from that value and
// rejects conflicting enabled providers; public settings expose a separate
// selection of browser parameters so server secrets remain in verification
// calls.

// TencentCaptchaConfig holds runtime credentials for server-side Tencent verification.
type TencentCaptchaConfig struct {
	// Enabled selects Tencent verification in AuthService. AppID, AppSecretKey,
	// CloudSecretID and CloudSecretKey are required together by credential parsing
	// before the verifier can receive a challenge.
	Enabled        bool   // Tencent challenge verification switch
	AppID          string // decimal CAPTCHA application identifier
	AppSecretKey   string // CAPTCHA application secret key
	CloudSecretID  string // cloud API credential identifier
	CloudSecretKey string // cloud API credential secret
	// Region selects the matching browser and verification sites. Normalization
	// maps missing or unrecognized values to the mainland China selection.
	Region string
}

// AliyunCaptchaConfig holds runtime credentials for server-side Aliyun challenge checks.
type AliyunCaptchaConfig struct {
	// Enabled selects Aliyun verification in AuthService. AccessKeyID,
	// AccessKeySecret and SceneID must all be nonblank after trimming before
	// a verification request can be dispatched.
	Enabled         bool   // Aliyun challenge verification switch
	AccessKeyID     string // cloud API credential identifier
	AccessKeySecret string // cloud API credential secret
	SceneID         string // configured CAPTCHA scene identifier
	// Region selects the verification endpoint used with the scene credentials.
	// Missing or unrecognized values normalize to the mainland China region.
	Region string
}

// CaptchaProviderConfig contains the settings snapshot for selecting one CAPTCHA provider.
type CaptchaProviderConfig struct {
	// TurnstileEnabled and TurnstileSecretKey are read with Tencent and Aliyun
	// from one batch. AuthService rejects enabled-provider conflicts and requires
	// the selected provider's server credentials before dispatch.
	TurnstileEnabled   bool                 // Turnstile challenge verification switch
	TurnstileSecretKey string               // server challenge verification secret
	Tencent            TencentCaptchaConfig // Tencent switch and credentials
	Aliyun             AliyunCaptchaConfig  // Aliyun switch and credentials
}

// CaptchaSettings reads the current configuration consumed by authentication actions.
type CaptchaSettings interface {
	// GetCaptchaProviderConfig reads provider switches and credentials as one
	// batch value. Read failures are returned so AuthService can reject the action
	// before selecting or invoking a verifier.
	GetCaptchaProviderConfig(context.Context) (CaptchaProviderConfig, error)
	// IsTurnstileEnabled reports whether the stored Turnstile switch is enabled.
	// RuntimeSettings treats missing values and read failures as disabled; callers
	// use this read for feature checks outside batch verification.
	IsTurnstileEnabled(context.Context) bool
	// GetTurnstileSecretKey reads the server secret for Turnstile verification.
	// RuntimeSettings returns an empty string on read failure, allowing consumers
	// to reject an enabled provider whose secret is unavailable.
	GetTurnstileSecretKey(context.Context) string
}

// LogFunc sends identity diagnostics to the logger selected by app.
type LogFunc func(component, format string, args ...any)

// Observer emits diagnostics when a logging callback is configured.
type Observer struct { /* Log receives configured diagnostics. Install it before serving
	requests because Printf reads this callback directly and supplies no
	synchronization when the callback changes. */Log LogFunc
}

// Printf passes an identity diagnostic to the configured logging callback.
// An unset callback discards the message; callers install logging before serving
// requests because this observer provides no synchronization for callback changes.
func (o Observer) Printf(component, format string, args ...any) {
	if o.Log != nil {
		o.Log(component, format, args...)
	}
}
