package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type QoderCredentialValidation struct {
	Normalize   func(string, string) (string, error)
	ValidatePAT func(context.Context, *Record, string, string) error
}

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
		// Site-only edits retain the credentials; the connection test checks compatibility with the selected site.
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
