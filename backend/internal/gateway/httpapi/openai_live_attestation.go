package httpapi

import (
	"context"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
)

// openai_live_attestation.go pairs Live attestation headers with encrypted
// credentials for saved reconnects.
// Live creation needs an issued DeviceCheck header and an encrypted
// representation of that same header for saved control reconnects. Generation
// and encryption therefore succeed together before the caller receives either
// value; a failure exposes the typed unavailable result used by HTTP creation
// handling.
//
// Reconnects decrypt the ciphertext stored in the call record through the
// current instance's encryptor. A missing ciphertext or encryptor, or
// decryption failure under another key, prevents obtaining a reusable header.
// The plaintext goes to upstream authentication while ciphertext remains with
// session storage, keeping the saved value distinct from the header sent
// during the original call.

// prepareLiveAttestation returns one generated header with its encrypted
// saved representation. It requires an attestation provider and cipher;
// missing services return typed unavailable errors before generation.
// Generation errors retain their reason, while encryption failures use the
// fixed protection-error message.
//
// The cipher encrypts exactly the header returned by the provider, and both
// values are returned only after that succeeds. Creator sends plaintext
// upstream and stores ciphertext for later sideband reuse. Callers therefore
// keep the plaintext out of session serialization and pass the paired
// ciphertext into the saved call record.
func (s *OpenAILiveExecutor) prepareLiveAttestation(ctx context.Context) (string, string, error) {
	if s == nil || s.Attestation == nil {
		return "", "", &session.LiveAttestationUnavailableError{
			Reason: "TokenRouter has no platform DeviceCheck provider",
		}
	}
	if s.AttestationCipher == nil {
		return "", "", &session.LiveAttestationUnavailableError{
			Reason: "JWT secret is required to protect the Sideband attestation",
		}
	}
	header, err := s.Attestation.Generate(ctx)
	if err != nil {
		return "", "", &session.LiveAttestationUnavailableError{Reason: err.Error()}
	}
	// Protect the exact generated header before publishing either return value, so
	// saved reconnect credentials correspond to the attestation sent during creation.
	ciphertext, err := s.AttestationCipher.Encrypt(header)
	if err != nil {
		return "", "", &session.LiveAttestationUnavailableError{
			Reason: "failed to protect the generated DeviceCheck attestation",
		}
	}
	return header, ciphertext, nil
}

// decryptLiveAttestation returns the saved creation header using the current
// cipher. A usable executor is required; missing record, blank ciphertext,
// absent cipher or decryption failure returns a typed unavailable error with
// its supported reconnect reason.
func (s *OpenAILiveExecutor) decryptLiveAttestation(record *session.LiveCallRecord) (string, error) {
	if record == nil || strings.TrimSpace(record.AttestationCiphertext) == "" || s.AttestationCipher == nil {
		return "", &session.LiveAttestationUnavailableError{
			Reason: "the Live call has no reusable DeviceCheck attestation",
		}
	}
	header, err := s.AttestationCipher.Decrypt(record.AttestationCiphertext)
	if err != nil {
		return "", &session.LiveAttestationUnavailableError{
			Reason: "the Live call DeviceCheck attestation cannot be decrypted on this instance",
		}
	}
	return header, nil
}
