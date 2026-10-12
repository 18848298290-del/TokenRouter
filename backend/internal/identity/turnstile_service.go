package identity

import (
	"context"
	"fmt"
	"slices"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

var (
	ErrTurnstileVerificationFailed = infraerrors.BadRequest("TURNSTILE_VERIFICATION_FAILED", "turnstile verification failed")
	ErrTurnstileNotConfigured      = infraerrors.ServiceUnavailable("TURNSTILE_NOT_CONFIGURED", "turnstile not configured")
	ErrTurnstileInvalidSecretKey   = infraerrors.BadRequest("TURNSTILE_INVALID_SECRET_KEY", "invalid turnstile secret key")
)

// TurnstileVerifier verifies Turnstile challenge tokens.
type TurnstileVerifier interface {
	VerifyToken(ctx context.Context, secretKey, token, remoteIP string) (*TurnstileVerifyResponse, error)
}

// TurnstileService validates challenges using runtime settings and an external verifier.
type TurnstileService struct {
	observer       Observer
	settingService CaptchaSettings
	verifier       TurnstileVerifier
}

// TurnstileVerifyResponse contains the Cloudflare verification response.
type TurnstileVerifyResponse struct {
	Success     bool     `json:"success"`
	ChallengeTS string   `json:"challenge_ts"`
	Hostname    string   `json:"hostname"`
	ErrorCodes  []string `json:"error-codes"`
	Action      string   `json:"action"`
	CData       string   `json:"cdata"`
}

// NewTurnstileService creates a Turnstile verification service.
func NewTurnstileService(settingService CaptchaSettings, verifier TurnstileVerifier) *TurnstileService {
	return &TurnstileService{
		settingService: settingService,
		verifier:       verifier,
	}
}

func (s *TurnstileService) VerifyTokenWithSecret(ctx context.Context, secretKey, token, remoteIP string) error {
	if secretKey == "" {
		s.observer.Printf("service.turnstile", "%s", "[Turnstile] Secret key not configured")
		return ErrTurnstileNotConfigured
	}

	if token == "" {
		s.observer.Printf("service.turnstile", "%s", "[Turnstile] Token is empty")
		return ErrTurnstileVerificationFailed
	}

	s.observer.Printf("service.turnstile", "[Turnstile] Verifying token for IP: %s", remoteIP)
	if s == nil || s.verifier == nil {
		return ErrTurnstileNotConfigured
	}
	result, err := s.verifier.VerifyToken(ctx, secretKey, token, remoteIP)
	if err != nil {
		s.observer.Printf("service.turnstile", "[Turnstile] Request failed: %v", err)
		return fmt.Errorf("send request: %w", err)
	}

	if !result.Success {
		s.observer.Printf("service.turnstile", "[Turnstile] Verification failed, error codes: %v", result.ErrorCodes)
		return ErrTurnstileVerificationFailed
	}

	s.observer.Printf("service.turnstile", "%s", "[Turnstile] Verification successful")
	return nil
}

// IsEnabled reads the Turnstile enablement setting.
func (s *TurnstileService) IsEnabled(ctx context.Context) bool {
	return s.settingService.IsTurnstileEnabled(ctx)
}

// ValidateSecretKey probes the verifier and rejects invalid-input-secret responses.
func (s *TurnstileService) ValidateSecretKey(ctx context.Context, secretKey string) error {
	// Use a dummy challenge to probe the secret key.
	result, err := s.verifier.VerifyToken(ctx, secretKey, "test-validation", "")
	if err != nil {
		return fmt.Errorf("validate secret key: %w", err)
	}

	if slices.Contains(result.ErrorCodes, "invalid-input-secret") {
		return ErrTurnstileInvalidSecretKey
	}

	// Other response codes, including invalid-input-response, are accepted by this probe.
	return nil
}

// SetObserver installs the logger before the service begins handling requests.
func (s *TurnstileService) SetObserver(logf LogFunc) { s.observer = Observer{Log: logf} }
