package identity

import (
	"context"
)

// TencentCaptchaConfig contains server-side verification credentials. Public APIs must not expose it.
type TencentCaptchaConfig struct {
	Enabled        bool
	AppID          string
	AppSecretKey   string
	CloudSecretID  string
	CloudSecretKey string
	Region         string
}

// AliyunCaptchaConfig contains server-side CAPTCHA 2.0 credentials. Public APIs must not expose it.
type AliyunCaptchaConfig struct {
	Enabled         bool
	AccessKeyID     string
	AccessKeySecret string
	SceneID         string
	Region          string
}

type CaptchaProviderConfig struct {
	TurnstileEnabled   bool
	TurnstileSecretKey string
	Tencent            TencentCaptchaConfig
	Aliyun             AliyunCaptchaConfig
}

// CaptchaSettings reads current CAPTCHA configuration for each action.
type CaptchaSettings interface {
	GetCaptchaProviderConfig(context.Context) (CaptchaProviderConfig, error)
	IsTurnstileEnabled(context.Context) bool
	GetTurnstileSecretKey(context.Context) string
}

// LogFunc sends identity diagnostics to the logger selected by app.
type LogFunc func(component, format string, args ...any)

// Observer emits diagnostics when a logging callback is configured.
type Observer struct{ Log LogFunc }

func (o Observer) Printf(component, format string, args ...any) {
	if o.Log != nil {
		o.Log(component, format, args...)
	}
}
