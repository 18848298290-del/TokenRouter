package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// qoder_validation.go validates Qoder COSY credentials and controls whether
// PAT validation performs an exchange.
// Qoder accepts COSY records backed by a PAT or a direct security token with
// machine and user identity fields. Site/mode validation precedes either path,
// and editing can defer PAT exchange so a later connection test evaluates the
// retained credentials.

// QoderCredentialValidation supplies site normalization and optional PAT
// exchange validation callbacks.
type QoderCredentialValidation struct {
	// Normalize validates site and refresh mode and returns the site passed
	// to
	// ValidatePAT; its result is not written back into the record here.
	Normalize func(string, string) (string, error)
	// ValidatePAT verifies a nonblank PAT with the selected site and
	// provider
	// context when deferPATExchange is false; direct-token validation does
	// not
	// call it.
	ValidatePAT func(context.Context, *Record, string, string) error
}

// ValidateQoderCredentials validates platform/type correspondence and the
// selected Qoder authentication form. Nil and ordinary non-Qoder records pass;
// COSY requires Qoder, and Qoder requires COSY. Callers supply usable callbacks
// for the paths reached.
//
// Site and refresh mode are normalized before authentication checks. A trimmed
// PAT takes precedence and invokes ValidatePAT unless exchange is deferred for
// an edit. Direct tokens require a machine ID plus UID or AID; this path checks
// stored shape and leaves connectivity validation to other callers.
func ValidateQoderCredentials(ctx context.Context, provider *Record, deferPATExchange bool, ports QoderCredentialValidation) error {
	if provider == nil {
		return nil
	}
	if provider.Platform != PlatformQoder {
		if provider.Type == ProviderTypeCosy {
			return fmt.Errorf("%s provider type requires %s platform", ProviderTypeCosy, PlatformQoder)
		}
		return nil
	}
	if provider.Type != ProviderTypeCosy {
		return fmt.Errorf("qoder providers require %s provider type", ProviderTypeCosy)
	}
	if provider.Credentials == nil {
		return errors.New("qoder cosy credentials are required")
	}
	site, err := ports.Normalize(provider.GetCredential("site"), provider.GetCredential("refresh_mode"))
	if err != nil {
		return err
	}

	pat := strings.TrimSpace(provider.GetCredential("pat"))
	if pat != "" {
		// The edit caller can defer PAT exchange after a site-only
		// change;
		// its later connection test checks the retained credentials
		// against
		// the selected site.
		if deferPATExchange {
			return nil
		}
		return ports.ValidatePAT(ctx, provider, site, pat)
	}

	token := strings.TrimSpace(provider.GetCredential("security_oauth_token"))
	machineID := strings.TrimSpace(provider.GetCredential("machine_id"))
	if token != "" {
		if machineID == "" {
			return errors.New("qoder cosy credentials require machine_id with security_oauth_token")
		}
		if strings.TrimSpace(provider.GetCredential("uid")) == "" && strings.TrimSpace(provider.GetCredential("aid")) == "" {
			return errors.New("qoder cosy credentials require uid or aid with security_oauth_token")
		}
		return nil
	}
	return errors.New("qoder cosy credentials require pat or security_oauth_token+machine_id")
}
