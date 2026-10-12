package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

const (
	// AliyunCaptchaRegionCN selects mainland China; AliyunCaptchaRegionSGP selects Singapore.
	// The frontend AliyunCaptchaConfig.region must select the same region.
	AliyunCaptchaRegionCN  = "cn"
	AliyunCaptchaRegionSGP = "sgp"

	AliyunCaptchaEndpointCN  = "captcha.cn-shanghai.aliyuncs.com"
	AliyunCaptchaEndpointSGP = "captcha.ap-southeast-1.aliyuncs.com"

	// AliyunCredentialValidationParam is a dummy challenge used when validating saved credentials.
	AliyunCredentialValidationParam = "tokenrouter-credential-validation"
)

var (
	ErrAliyunCaptchaVerificationFailed = infraerrors.BadRequest("ALIYUN_CAPTCHA_VERIFICATION_FAILED", "aliyun captcha verification failed")
	ErrAliyunCaptchaNotConfigured      = infraerrors.ServiceUnavailable("ALIYUN_CAPTCHA_NOT_CONFIGURED", "aliyun captcha not configured")
	// ErrCaptchaInvalidCredentials reports invalid credentials when saving administrator settings.
	ErrCaptchaInvalidCredentials = infraerrors.BadRequest("CAPTCHA_INVALID_CREDENTIALS", "invalid aliyun captcha credentials")

	// AliyunInvalidCredentialCodes identifies API errors treated as invalid access credentials.
	// Other API errors are returned to the administrator for diagnosis.
	AliyunInvalidCredentialCodes = map[string]struct{}{
		"InvalidAccessKeyId.NotFound":  {},
		"InvalidAccessKeyId.Inactive":  {},
		"SignatureDoesNotMatch":        {},
		"Forbidden.AccessKeyDisabled":  {},
		"IncompleteSignature":          {},
		"InvalidSecurityToken.Expired": {},
	}
)

// AliyunCaptchaCredentials contains credentials for server-side Aliyun CAPTCHA 2.0 verification.
type AliyunCaptchaCredentials struct {
	AccessKeyID     string
	AccessKeySecret string
	SceneID         string
	Endpoint        string
}

// AliyunCaptchaVerifyResult is the normalized VerifyIntelligentCaptcha result.
type AliyunCaptchaVerifyResult struct {
	VerifyResult bool
	VerifyCode   string // Detailed Aliyun result code for logging.
}

// AliyunCaptchaAPIError represents an Aliyun OpenAPI error.
// The provider adapter converts SDK errors into this type.
type AliyunCaptchaAPIError struct {
	Code    string
	Message string
}

// AliyunCaptchaVerifier verifies Aliyun CAPTCHA 2.0 challenges.
type AliyunCaptchaVerifier interface {
	VerifyCaptcha(ctx context.Context, cred AliyunCaptchaCredentials, captchaVerifyParam string) (*AliyunCaptchaVerifyResult, error)
}

// AliyunCaptchaService validates Aliyun CAPTCHA 2.0 challenges on the server.
type AliyunCaptchaService struct {
	observer       Observer
	settingService CaptchaSettings
	verifier       AliyunCaptchaVerifier
}

func (e *AliyunCaptchaAPIError) Error() string {
	return fmt.Sprintf("aliyun captcha api error: %s: %s", e.Code, e.Message)
}

// AliyunCaptchaEndpoint returns the regional endpoint, defaulting to mainland China for unknown regions.
func AliyunCaptchaEndpoint(region string) string {
	if region == AliyunCaptchaRegionSGP {
		return AliyunCaptchaEndpointSGP
	}
	return AliyunCaptchaEndpointCN
}

// NormalizeAliyunCaptchaRegion defaults unknown values to mainland China.
func NormalizeAliyunCaptchaRegion(value string) string {
	if value == AliyunCaptchaRegionSGP {
		return AliyunCaptchaRegionSGP
	}
	return AliyunCaptchaRegionCN
}

func NewAliyunCaptchaService(settingService CaptchaSettings, verifier AliyunCaptchaVerifier) *AliyunCaptchaService {
	return &AliyunCaptchaService{settingService: settingService, verifier: verifier}
}

func ParseAliyunCaptchaCredentials(config AliyunCaptchaConfig) (AliyunCaptchaCredentials, bool) {
	cred := AliyunCaptchaCredentials{
		AccessKeyID:     strings.TrimSpace(config.AccessKeyID),
		AccessKeySecret: strings.TrimSpace(config.AccessKeySecret),
		SceneID:         strings.TrimSpace(config.SceneID),
		Endpoint:        AliyunCaptchaEndpoint(config.Region),
	}
	if cred.AccessKeyID == "" || cred.AccessKeySecret == "" || cred.SceneID == "" {
		return AliyunCaptchaCredentials{}, false
	}
	return cred, true
}

// VerifyParamWithConfig validates an Aliyun CAPTCHA 2.0 captchaVerifyParam.
// Verification errors reject the request.
func (s *AliyunCaptchaService) VerifyParamWithConfig(ctx context.Context, config AliyunCaptchaConfig, captchaVerifyParam string) error {
	if s == nil || s.verifier == nil {
		return ErrAliyunCaptchaNotConfigured
	}
	cred, ok := ParseAliyunCaptchaCredentials(config)
	if !ok {
		s.observer.Printf("service.aliyun_captcha", "%s", "[AliyunCaptcha] credentials not configured")
		return ErrAliyunCaptchaNotConfigured
	}

	if strings.TrimSpace(captchaVerifyParam) == "" {
		s.observer.Printf("service.aliyun_captcha", "%s", "[AliyunCaptcha] captchaVerifyParam is empty")
		return ErrAliyunCaptchaVerificationFailed
	}

	result, err := s.verifier.VerifyCaptcha(ctx, cred, captchaVerifyParam)
	if err != nil {
		s.observer.Printf("service.aliyun_captcha", "[AliyunCaptcha] verify request failed: %v", err)
		return fmt.Errorf("%w: verifier request failed", ErrAliyunCaptchaVerificationFailed)
	}

	if result == nil || !result.VerifyResult {
		if result != nil {
			s.observer.Printf("service.aliyun_captcha", "[AliyunCaptcha] rejected, verify code: %s", result.VerifyCode)
		}
		return ErrAliyunCaptchaVerificationFailed
	}
	return nil
}

// ValidateCredentials probes access credentials with a dummy challenge when saving settings.
// Credential error codes become ErrCaptchaInvalidCredentials. A response with no API error
// is accepted even when VerifyResult is false; other errors are returned with context.
func (s *AliyunCaptchaService) ValidateCredentials(ctx context.Context, accessKeyID, accessKeySecret, sceneID, region string) error {
	if s.verifier == nil {
		return ErrAliyunCaptchaNotConfigured
	}
	cred := AliyunCaptchaCredentials{
		AccessKeyID:     accessKeyID,
		AccessKeySecret: accessKeySecret,
		SceneID:         sceneID,
		Endpoint:        AliyunCaptchaEndpoint(region),
	}

	_, err := s.verifier.VerifyCaptcha(ctx, cred, AliyunCredentialValidationParam)
	if err != nil {
		if apiErr, ok := errors.AsType[*AliyunCaptchaAPIError](err); ok {
			if _, invalid := AliyunInvalidCredentialCodes[apiErr.Code]; invalid {
				return ErrCaptchaInvalidCredentials
			}
		}
		return fmt.Errorf("validate aliyun captcha credentials: %w", err)
	}

	return nil
}

// SetObserver sets the logger before requests are served.
func (s *AliyunCaptchaService) SetObserver(logf LogFunc) { s.observer = Observer{Log: logf} }
