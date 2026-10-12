package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/imroc/req/v3"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// ResolveSettingsOIDCMetadata fetches discovery endpoints for the identity settings service.
func ResolveSettingsOIDCMetadata(ctx context.Context, discoveryURL string) (*identity.OAuthProviderMetadata, error) {
	discoveryURL = strings.TrimSpace(discoveryURL)
	if discoveryURL == "" {
		return nil, fmt.Errorf("discovery url is empty")
	}

	resp, err := req.C().
		SetTimeout(15*time.Second).
		R().
		SetContext(ctx).
		SetHeader("Accept", "application/json").
		Get(discoveryURL)
	if err != nil {
		return nil, fmt.Errorf("request discovery document: %w", err)
	}
	if !resp.IsSuccessState() {
		return nil, fmt.Errorf("discovery request failed: status=%d", resp.StatusCode)
	}

	metadata := &identity.OAuthProviderMetadata{}
	if err := json.Unmarshal(resp.Bytes(), metadata); err != nil {
		return nil, fmt.Errorf("parse discovery document: %w", err)
	}
	return metadata, nil
}
