package identity

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

const (
	// TencentCaptchaRegionCN selects cloud.tencent.com; TencentCaptchaRegionINTL selects tencentcloud.com.
	// The region selects both the browser SDK and the server verification endpoint.
	// Both must match the captcha application's region for verification to succeed.
	TencentCaptchaRegionCN   = "cn"
	TencentCaptchaRegionINTL = "intl"

	TencentCaptchaEndpointCN   = "captcha.tencentcloudapi.com"
	TencentCaptchaEndpointINTL = "captcha.intl.tencentcloudapi.com"
)

var (
	ErrTencentCaptchaVerificationFailed = infraerrors.BadRequest("TENCENT_CAPTCHA_VERIFICATION_FAILED", "tencent captcha verification failed")
	ErrTencentCaptchaNotConfigured      = infraerrors.ServiceUnavailable("TENCENT_CAPTCHA_NOT_CONFIGURED", "tencent captcha not configured")
)

type TencentCaptchaProof struct {
	Ticket  string
	Randstr string
}

type TencentCaptchaCredentials struct {
	AppID          uint64
	AppSecretKey   string
	CloudSecretID  string
	CloudSecretKey string
	// Verification endpoint derived from the region and passed to the provider adapter.
	Endpoint string
}

type TencentCaptchaVerifyResponse struct {
	CaptchaCode int64
	CaptchaMsg  string
	RequestID   string
}

type TencentCaptchaVerifier interface {
	VerifyTicket(context.Context, TencentCaptchaCredentials, TencentCaptchaProof, string) (*TencentCaptchaVerifyResponse, error)
}

type TencentCaptchaService struct {
	observer       Observer
	settingService CaptchaSettings
	verifier       TencentCaptchaVerifier
}

// TencentCaptchaEndpoint returns the region's verification endpoint, defaulting to China for unknown values.
func TencentCaptchaEndpoint(region string) string {
	if region == TencentCaptchaRegionINTL {
		return TencentCaptchaEndpointINTL
	}
	return TencentCaptchaEndpointCN
}

// NormalizeTencentCaptchaRegion maps unknown values to the China region.
func NormalizeTencentCaptchaRegion(value string) string {
	if value == TencentCaptchaRegionINTL {
		return TencentCaptchaRegionINTL
	}
	return TencentCaptchaRegionCN
}

// NewTencentCaptchaService creates a verifier backed by runtime settings.
func NewTencentCaptchaService(settingService CaptchaSettings, verifier TencentCaptchaVerifier) *TencentCaptchaService {
	return &TencentCaptchaService{settingService: settingService, verifier: verifier}
}

func (s *TencentCaptchaService) VerifyTicket(ctx context.Context, ticket, randstr, remoteIP string) error {
	if s == nil || s.settingService == nil {
		return ErrTencentCaptchaNotConfigured
	}
	providerConfig, err := s.settingService.GetCaptchaProviderConfig(ctx)
	if err != nil {
		s.observer.Printf("service.tencent_captcha", "%s", "[TencentCaptcha] failed to read captcha provider settings")
		return ErrServiceUnavailable
	}
	config := providerConfig.Tencent
	if !config.Enabled {
		return nil
	}
	return s.VerifyTicketWithConfig(ctx, config, ticket, randstr, remoteIP)
}

func (s *TencentCaptchaService) VerifyTicketWithConfig(ctx context.Context, config TencentCaptchaConfig, ticket, randstr, remoteIP string) error {
	// Read one configuration snapshot so the switch and credentials agree within this request.
	credentials, ok := ParseTencentCaptchaCredentials(config)
	if !ok || s.verifier == nil {
		return ErrTencentCaptchaNotConfigured
	}

	proof := TencentCaptchaProof{
		Ticket:  strings.TrimSpace(ticket),
		Randstr: strings.TrimSpace(randstr),
	}
	if proof.Ticket == "" || proof.Randstr == "" || strings.HasPrefix(proof.Ticket, "trerror_") {
		return ErrTencentCaptchaVerificationFailed
	}

	result, err := s.verifier.VerifyTicket(ctx, credentials, proof, remoteIP)
	if err != nil {
		s.observer.Printf(
			"service.tencent_captcha",
			"[TencentCaptcha] verification request failed region=%s endpoint=%s error=%v",
			NormalizeTencentCaptchaRegion(config.Region),
			credentials.Endpoint,
			err,
		)
		return fmt.Errorf("%w: verifier request failed", ErrTencentCaptchaVerificationFailed)
	}
	if result == nil || result.CaptchaCode != 1 {
		if result != nil {
			s.observer.Printf(
				"service.tencent_captcha",
				"[TencentCaptcha] rejected region=%s code=%d message=%q request_id=%q",
				NormalizeTencentCaptchaRegion(config.Region),
				result.CaptchaCode,
				result.CaptchaMsg,
				result.RequestID,
			)
		} else {
			s.observer.Printf(
				"service.tencent_captcha",
				"[TencentCaptcha] rejected region=%s empty_response=true",
				NormalizeTencentCaptchaRegion(config.Region),
			)
		}
		return ErrTencentCaptchaVerificationFailed
	}
	return nil
}

func ParseTencentCaptchaCredentials(config TencentCaptchaConfig) (TencentCaptchaCredentials, bool) {
	appID, err := strconv.ParseUint(strings.TrimSpace(config.AppID), 10, 64)
	if err != nil || appID == 0 {
		return TencentCaptchaCredentials{}, false
	}
	credentials := TencentCaptchaCredentials{
		AppID:          appID,
		AppSecretKey:   strings.TrimSpace(config.AppSecretKey),
		CloudSecretID:  strings.TrimSpace(config.CloudSecretID),
		CloudSecretKey: strings.TrimSpace(config.CloudSecretKey),
		Endpoint:       TencentCaptchaEndpoint(config.Region),
	}
	if credentials.AppSecretKey == "" || credentials.CloudSecretID == "" || credentials.CloudSecretKey == "" {
		return TencentCaptchaCredentials{}, false
	}
	return credentials, true
}

// SetObserver installs the logger before the service begins handling requests.
func (s *TencentCaptchaService) SetObserver(logf LogFunc) { s.observer = Observer{Log: logf} }
