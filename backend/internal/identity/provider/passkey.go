package provider

import (
	"fmt"
	"io"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// PasskeyOptions contains relying-party settings fixed at startup.
type PasskeyOptions struct {
	Enabled       bool
	RPDisplayName string
	RPID          string
	RPOrigins     []string
}

// WebAuthnVerifier parses ceremony responses and delegates verification to the WebAuthn SDK.
type WebAuthnVerifier struct{ *webauthn.WebAuthn }

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

func (v *WebAuthnVerifier) FinishRegistration(user webauthn.User, session webauthn.SessionData, response io.Reader) (*webauthn.Credential, error) {
	parsed, err := protocol.ParseCredentialCreationResponseBody(response)
	if err != nil {
		return nil, err
	}
	return v.CreateCredential(user, session, parsed)
}

func (v *WebAuthnVerifier) FinishPasskeyLogin(handler webauthn.DiscoverableUserHandler, session webauthn.SessionData, response io.Reader) (webauthn.User, *webauthn.Credential, error) {
	parsed, err := protocol.ParseCredentialRequestResponseBody(response)
	if err != nil {
		return nil, nil, err
	}
	return v.ValidatePasskeyLogin(handler, session, parsed)
}
