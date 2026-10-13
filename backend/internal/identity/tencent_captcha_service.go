package identity

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Tencent challenge verification couples the application credentials, browser
// proof and regional server endpoint. VerifyTicket reads one settings batch
// before dispatch, while AuthService can supply its own selected snapshot to
// VerifyTicketWithConfig so provider selection and verification use the same
// credential values.

const (
	// TencentCaptchaRegionCN selects cloud.tencent.com; TencentCaptchaRegionINTL selects tencentcloud.com.
	// The region selects both the browser SDK and the server verification endpoint.
	// Both must match the captcha application's region for verification to succeed.
	TencentCaptchaRegionCN   = "cn"
	TencentCaptchaRegionINTL = "intl"

	// These verification hosts are supplied to the adapter according to the
	// application region, keeping server requests aligned with the browser site.
	TencentCaptchaEndpointCN   = "captcha.tencentcloudapi.com"
	TencentCaptchaEndpointINTL = "captcha.intl.tencentcloudapi.com"
)

// These errors distinguish unusable Tencent verification configuration from a
// challenge that the local proof check or external verifier rejects for an action.
var (
	ErrTencentCaptchaVerificationFailed = infraerrors.BadRequest("TENCENT_CAPTCHA_VERIFICATION_FAILED", "tencent captcha verification failed")
	ErrTencentCaptchaNotConfigured      = infraerrors.ServiceUnavailable("TENCENT_CAPTCHA_NOT_CONFIGURED", "tencent captcha not configured")
)

// TencentCaptchaProof carries the two browser values submitted for one verification.
type TencentCaptchaProof struct {
	Ticket  string // browser challenge ticket text
	Randstr string // browser challenge random string
}

// TencentCaptchaCredentials is the server credential set supplied to the adapter.
type TencentCaptchaCredentials struct {
	AppID          uint64 // positive CAPTCHA application identifier
	AppSecretKey   string // CAPTCHA application secret key
	CloudSecretID  string // cloud API credential identifier
	CloudSecretKey string // cloud API credential secret
	// Endpoint is derived from the selected region by credential parsing. The
	// external adapter uses this hostname for the application's verification site.
	Endpoint string
}

// TencentCaptchaVerifyResponse holds the verifier's decision and diagnostic response fields.
type TencentCaptchaVerifyResponse struct {
	CaptchaCode int64  // verifier challenge decision code
	CaptchaMsg  string // verifier decision message text
	RequestID   string // vendor verification request identifier
}

// TencentCaptchaVerifier submits a browser proof with its server-side application credentials.
type TencentCaptchaVerifier interface {
	// VerifyTicket verifies the proof at the credential endpoint with the
	// supplied client IP. Adapter errors are returned, while decoded results let
	// the service interpret the vendor decision code and diagnostic fields.
	VerifyTicket(context.Context, TencentCaptchaCredentials, TencentCaptchaProof, string) (*TencentCaptchaVerifyResponse, error)
}

// TencentCaptchaService checks runtime configuration and interprets external challenge decisions.
type TencentCaptchaService struct {
	observer       Observer               // configured service diagnostic callback
	settingService CaptchaSettings        // current verification settings reader
	verifier       TencentCaptchaVerifier // external challenge verification adapter
}

// TencentCaptchaEndpoint returns the verification hostname for the selected region.
// Only the exact intl value selects the international site; empty or unrecognized
// values select the mainland China hostname used by default configuration.
func TencentCaptchaEndpoint(region string) string {
	if region == TencentCaptchaRegionINTL {
		return TencentCaptchaEndpointINTL
	}
	return TencentCaptchaEndpointCN
}

// NormalizeTencentCaptchaRegion returns a supported Tencent site selection value.
// The exact intl value selects the international site; all other values select
// cn, including empty and whitespace-padded strings supplied without trimming.
func NormalizeTencentCaptchaRegion(value string) string {
	if value == TencentCaptchaRegionINTL {
		return TencentCaptchaRegionINTL
	}
	return TencentCaptchaRegionCN
}

// NewTencentCaptchaService retains the settings reader and external Tencent verifier.
func NewTencentCaptchaService(settingService CaptchaSettings, verifier TencentCaptchaVerifier) *TencentCaptchaService {
	return &TencentCaptchaService{settingService: settingService, verifier: verifier}
}

// VerifyTicket reads current Tencent settings and checks a browser challenge.
// Disabled Tencent verification succeeds immediately; an absent settings reader
// or failed batch read rejects the action before the external verifier is called.
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

// VerifyTicketWithConfig verifies a browser proof using the selected Tencent
// settings snapshot. Callers select an enabled provider first; this method
// checks credentials and proof but does not inspect config.Enabled.
// Ticket and randstr are trimmed, missing values and the trerror_ ticket prefix
// are rejected locally, and only a non-nil response with CaptchaCode one succeeds.
// The supplied remoteIP reaches the external verifier as provided.
func (s *TencentCaptchaService) VerifyTicketWithConfig(ctx context.Context, config TencentCaptchaConfig, ticket, randstr, remoteIP string) error {
	credentials, ok := ParseTencentCaptchaCredentials(config)
	if !ok || s.verifier == nil {
		return ErrTencentCaptchaNotConfigured
	}

	proof := TencentCaptchaProof{
		Ticket:  strings.TrimSpace(ticket),
		Randstr: strings.TrimSpace(randstr),
	}
	// Incomplete proofs and browser error tickets cannot establish an accepted
	// challenge, so reject them before dispatching an external verification request.
	if proof.Ticket == "" || proof.Randstr == "" || strings.HasPrefix(proof.Ticket, "trerror_") {
		return ErrTencentCaptchaVerificationFailed
	}

	result, err := s.verifier.VerifyTicket(ctx, credentials, proof, remoteIP)
	if err != nil {
		// A failed verifier call cannot accept this challenge. Report the public
		// action error while keeping regional endpoint details in service diagnostics.
		s.observer.Printf(
			"service.tencent_captcha",
			"[TencentCaptcha] verification request failed region=%s endpoint=%s error=%v",
			NormalizeTencentCaptchaRegion(config.Region),
			credentials.Endpoint,
			err,
		)
		return fmt.Errorf("%w: verifier request failed", ErrTencentCaptchaVerificationFailed)
	}
	// Code one is the accepted challenge decision here.
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

// ParseTencentCaptchaCredentials trims credentials and selects the regional endpoint.
// AppID must parse as a positive uint64, and each secret must be nonblank;
// unusable input returns a zero credential value with false.
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

// SetObserver installs logging for subsequent Tencent verification operations.
// Callers configure it before serving requests because challenge checks read
// the observer directly and provide no synchronization for changing its callback.
func (s *TencentCaptchaService) SetObserver(logf LogFunc) { s.observer = Observer{Log: logf} }
