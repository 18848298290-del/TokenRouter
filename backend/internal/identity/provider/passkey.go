package provider

import (
	"fmt"
	"io"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// passkey.go configures the WebAuthn verifier and parses browser credential
// responses for registration and login.
// Passkey ceremonies use startup relying-party identity and origin settings to
// validate browser responses. The adapter parses the response body before invoking
// the WebAuthn SDK; the identity service owns session consumption, account lookup,
// and durable credential writes around that verification.

// PasskeyOptions supplies the relying-party settings used to initialize WebAuthn verification.
type PasskeyOptions struct {
	Enabled       bool   // startup Passkey enablement flag
	RPDisplayName string // displayed relying-party service name
	RPID          string // relying-party domain identifier value
	// RPOrigins supplies permitted browser origins. NewPasskeyVerifier retains
	// this slice through the SDK configuration, so callers leave its contents
	// unchanged while the verifier handles ceremonies.
	RPOrigins []string
}

// WebAuthnVerifier parses ceremony responses and exposes the configured SDK verification methods.
type WebAuthnVerifier struct {
	*webauthn.WebAuthn // configured WebAuthn SDK instance
}

// NewPasskeyVerifier initializes the SDK with required resident keys and user
// verification. Disabled options return nil without error; enabled options can
// fail SDK configuration validation. The verifier retains options.RPOrigins,
// which callers keep unchanged throughout its use.
func NewPasskeyVerifier(options PasskeyOptions) (identity.PasskeyVerifier, error) {
	if !options.Enabled {
		return nil, nil
	}
	instance, err := webauthn.New(&webauthn.Config{RPDisplayName: options.RPDisplayName, RPID: options.RPID, RPOrigins: options.RPOrigins, AuthenticatorSelection: protocol.AuthenticatorSelection{ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationRequired}})
	if err != nil {
		return nil, fmt.Errorf("initialize WebAuthn: %w", err)
	}
	return &WebAuthnVerifier{WebAuthn: instance}, nil
}

// FinishRegistration parses the creation response and asks the SDK to verify it
// against user and the saved ceremony session. The caller owns consuming that
// session and persisting the returned credential after success.
func (v *WebAuthnVerifier) FinishRegistration(user webauthn.User, session webauthn.SessionData, response io.Reader) (*webauthn.Credential, error) {
	parsed, err := protocol.ParseCredentialCreationResponseBody(response)
	if err != nil {
		return nil, err
	}
	return v.CreateCredential(user, session, parsed)
}

// FinishPasskeyLogin parses an assertion and delegates discoverable-login
// verification to the SDK. The caller supplies the saved login session and handler
// for credential-to-user lookup; the adapter returns the verified user and credential.
// The caller persists the returned authenticator state after successful verification.
func (v *WebAuthnVerifier) FinishPasskeyLogin(handler webauthn.DiscoverableUserHandler, session webauthn.SessionData, response io.Reader) (webauthn.User, *webauthn.Credential, error) {
	parsed, err := protocol.ParseCredentialRequestResponseBody(response)
	if err != nil {
		return nil, nil, err
	}
	return v.ValidatePasskeyLogin(handler, session, parsed)
}
