package identity

import (
	"context"
	"fmt"
	"slices"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Turnstile actions pass the selected secret and challenge token to an external
// verifier. Challenge success determines authentication acceptance, while the
// administrator secret probe examines invalid-input-secret specifically and
// accepts other decoded codes because its dummy challenge is intentionally
// unsuitable for a normal verification.

// These errors distinguish rejected challenges, missing verification credentials
// and a secret that the administrator probe identifies as invalid for configuration.
var (
	ErrTurnstileVerificationFailed = infraerrors.BadRequest("TURNSTILE_VERIFICATION_FAILED", "turnstile verification failed")
	ErrTurnstileNotConfigured      = infraerrors.ServiceUnavailable("TURNSTILE_NOT_CONFIGURED", "turnstile not configured")
	ErrTurnstileInvalidSecretKey   = infraerrors.BadRequest("TURNSTILE_INVALID_SECRET_KEY", "invalid turnstile secret key")
)

// TurnstileVerifier returns decoded challenge decisions from the external validation service.
type TurnstileVerifier interface {
	// VerifyToken submits the secret, challenge token and client IP for checking.
	// A nil error requires a non-nil decoded response because the service reads
	// success and error codes directly from that response.
	VerifyToken(ctx context.Context, secretKey, token, remoteIP string) (*TurnstileVerifyResponse, error)
}

// TurnstileService validates challenges using runtime settings and an external verifier.
type TurnstileService struct {
	observer       Observer          // configured service diagnostic callback
	settingService CaptchaSettings   // current verification settings reader
	verifier       TurnstileVerifier // external challenge verification adapter
}

// TurnstileVerifyResponse holds the decoded Cloudflare challenge decision and response metadata.
type TurnstileVerifyResponse struct {
	Success     bool     `json:"success"`      // accepted browser challenge result
	ChallengeTS string   `json:"challenge_ts"` // vendor challenge timestamp text
	Hostname    string   `json:"hostname"`     // vendor reported challenge hostname
	ErrorCodes  []string `json:"error-codes"`  // vendor verification error codes
	Action      string   `json:"action"`       // vendor returned action label
	CData       string   `json:"cdata"`        // vendor returned application data
}

// NewTurnstileService retains the settings reader and external Turnstile verifier.
func NewTurnstileService(settingService CaptchaSettings, verifier TurnstileVerifier) *TurnstileService {
	return &TurnstileService{
		settingService: settingService,
		verifier:       verifier,
	}
}

// VerifyTokenWithSecret checks a challenge using the supplied server secret.
// Callers provide a non-nil service and pass the selected settings; the method
// requires nonempty secret and token strings, then returns adapter failures or
// ErrTurnstileVerificationFailed for a decoded rejection. A configured verifier
// returns a non-nil response whenever its error is nil.
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

// IsEnabled reads the current Turnstile switch through the settings reader.
// The service requires that reader to be configured; RuntimeSettings reports
// disabled when its stored switch is missing or cannot be read.
func (s *TurnstileService) IsEnabled(ctx context.Context) bool {
	return s.settingService.IsTurnstileEnabled(ctx)
}

// ValidateSecretKey probes whether the supplied secret is rejected by Turnstile.
// A configured verifier and non-nil response on nil error are required; the
// method rejects invalid-input-secret and accepts other decoded response codes
// from its dummy challenge, with adapter errors returned to the administrator.
func (s *TurnstileService) ValidateSecretKey(ctx context.Context, secretKey string) error {
	// The dummy challenge lets administrator settings distinguish an invalid
	// secret from a failed user challenge by inspecting the returned error codes.
	result, err := s.verifier.VerifyToken(ctx, secretKey, "test-validation", "")
	if err != nil {
		return fmt.Errorf("validate secret key: %w", err)
	}

	if slices.Contains(result.ErrorCodes, "invalid-input-secret") {
		return ErrTurnstileInvalidSecretKey
	}

	// A decoded response lacking invalid-input-secret permits the settings save,
	// even when its dummy challenge fails with invalid-input-response or another code.
	return nil
}

// SetObserver installs logging for subsequent Turnstile verification operations.
// Callers configure it before serving requests because challenge checks read
// the observer directly and provide no synchronization for changing its callback.
func (s *TurnstileService) SetObserver(logf LogFunc) { s.observer = Observer{Log: logf} }
