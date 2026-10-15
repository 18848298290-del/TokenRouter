package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// aliyun_captcha_service.go verifies Aliyun CAPTCHA challenges and probes
// administrator-supplied credentials.
// Aliyun challenge checks use a scene and regional endpoint together with cloud
// access credentials. Authentication supplies a configuration snapshot and
// accepts the action only when the external verifier returns a successful
// challenge; administrator credential probes use a separate dummy challenge
// whose failed challenge result can still confirm accepted credentials.

const (
	// AliyunCaptchaRegionCN selects mainland China; AliyunCaptchaRegionSGP selects Singapore.
	// The frontend AliyunCaptchaConfig.region must select the same region.
	AliyunCaptchaRegionCN  = "cn"
	AliyunCaptchaRegionSGP = "sgp"

	// These endpoints identify the server site corresponding to the selected
	// scene region and are supplied to the external verification adapter.
	AliyunCaptchaEndpointCN  = "captcha.cn-shanghai.aliyuncs.com"
	AliyunCaptchaEndpointSGP = "captcha.ap-southeast-1.aliyuncs.com"

	// AliyunCredentialValidationParam selects the dummy challenge used to distinguish
	// rejected access credentials from an ordinary failed challenge during settings validation.
	AliyunCredentialValidationParam = "tokenrouter-credential-validation"
)

// These action errors distinguish rejected challenges, missing verification
// configuration and credentials refused during an administrator settings probe.
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
	AccessKeyID     string // cloud API credential identifier
	AccessKeySecret string // cloud API credential secret
	SceneID         string // configured CAPTCHA scene identifier
	Endpoint        string // regional verification server hostname
}

// AliyunCaptchaVerifyResult holds the decoded challenge decision and diagnostic verification code.
type AliyunCaptchaVerifyResult struct {
	VerifyResult bool   // accepted CAPTCHA challenge result
	VerifyCode   string // detailed rejection code for diagnostics
}

// AliyunCaptchaAPIError represents an Aliyun OpenAPI error.
// The provider adapter converts SDK errors into this type.
type AliyunCaptchaAPIError struct {
	Code    string // OpenAPI error code text
	Message string // OpenAPI error description text
}

// AliyunCaptchaVerifier checks serialized browser challenges with Aliyun server credentials.
type AliyunCaptchaVerifier interface {
	// VerifyCaptcha sends the supplied scene credentials and serialized browser
	// challenge to the configured endpoint. Adapter failures remain errors, while
	// a decoded rejection is returned as a result for the service to interpret.
	VerifyCaptcha(ctx context.Context, cred AliyunCaptchaCredentials, captchaVerifyParam string) (*AliyunCaptchaVerifyResult, error)
}

// AliyunCaptchaService validates Aliyun CAPTCHA 2.0 challenges on the server.
type AliyunCaptchaService struct {
	observer       Observer              // configured service diagnostic callback
	settingService CaptchaSettings       // retained optional settings reference
	verifier       AliyunCaptchaVerifier // external challenge verification adapter
}

// Error formats the Aliyun API code and diagnostic message together.
func (e *AliyunCaptchaAPIError) Error() string {
	return fmt.Sprintf("aliyun captcha api error: %s: %s", e.Code, e.Message)
}

// AliyunCaptchaEndpoint returns the hostname for the supplied region selection.
// Only the exact sgp value selects Singapore; missing and unrecognized values
// select the mainland China endpoint used by default configuration.
func AliyunCaptchaEndpoint(region string) string {
	if region == AliyunCaptchaRegionSGP {
		return AliyunCaptchaEndpointSGP
	}
	return AliyunCaptchaEndpointCN
}

// NormalizeAliyunCaptchaRegion returns an accepted Aliyun region configuration value.
// The exact sgp value selects Singapore; every other value selects cn, including
// blank or whitespace-padded values that have not been trimmed by a caller.
func NormalizeAliyunCaptchaRegion(value string) string {
	if value == AliyunCaptchaRegionSGP {
		return AliyunCaptchaRegionSGP
	}
	return AliyunCaptchaRegionCN
}

// NewAliyunCaptchaService binds challenge validation to the supplied external verifier.
func NewAliyunCaptchaService(settingService CaptchaSettings, verifier AliyunCaptchaVerifier) *AliyunCaptchaService {
	return &AliyunCaptchaService{settingService: settingService, verifier: verifier}
}

// ParseAliyunCaptchaCredentials trims server credentials and selects their regional
// endpoint. All three credential strings must be nonblank; otherwise it returns
// a zero credentials value and false for the caller's configuration check.
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

// VerifyParamWithConfig checks a serialized browser challenge with the supplied
// Aliyun credentials. The caller selects an enabled provider before invoking
// this method; its Enabled field is not used during the check.
// Missing adapters or credentials return ErrAliyunCaptchaNotConfigured, while
// empty challenges, adapter failures and rejected results return the public
// verification error. The original nonempty challenge text reaches the adapter.
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
		// An adapter failure provides no accepted challenge for this action. Keep
		// its details in diagnostics while returning the public verification error.
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

// ValidateCredentials probes the supplied credentials for administrator settings.
// The service must have a verifier; the method passes credential strings as
// supplied and selects the endpoint from region. Recognized API credential
// errors become ErrCaptchaInvalidCredentials, and other errors retain context.
// A nil verifier error succeeds regardless of the challenge result because the
// probe asks whether the access credentials are accepted.
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

	// A dummy challenge separates access-credential validation from challenge
	// validity; a rejected challenge with no API error still accepts these settings.
	_, err := s.verifier.VerifyCaptcha(ctx, cred, AliyunCredentialValidationParam)
	if err != nil {
		// Recognized credential codes identify settings the administrator must fix;
		// other API or transport failures retain their diagnostic error for the caller.
		if apiErr, ok := errors.AsType[*AliyunCaptchaAPIError](err); ok {
			if _, invalid := AliyunInvalidCredentialCodes[apiErr.Code]; invalid {
				return ErrCaptchaInvalidCredentials
			}
		}
		return fmt.Errorf("validate aliyun captcha credentials: %w", err)
	}

	return nil
}

// SetObserver installs the logger used by subsequent Aliyun verification operations.
// Callers configure it before serving requests because verification reads the
// observer directly and does not synchronize concurrent changes to its callback.
func (s *AliyunCaptchaService) SetObserver(logf LogFunc) { s.observer = Observer{Log: logf} }
