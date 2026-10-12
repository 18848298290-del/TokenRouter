package transfer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/egress"
)

const (
	DataType = "tokenrouter-data"
	// LegacyDataType accepts same-version exports made under the former product name.
	LegacyDataType = "sub2api-data"
	DataVersion    = 2
)

type (
	DataProxy       = egress.TransferProxy
	DataImportError = egress.TransferError
)

type DataPayload struct {
	Type       string         `json:"type,omitempty"`
	Version    int            `json:"version,omitempty"`
	ExportedAt string         `json:"exported_at"`
	Proxies    []DataProxy    `json:"proxies"`
	Providers  []DataProvider `json:"providers"`
	// SkippedShadows reports how many Spark shadow providers export excluded.
	// Import ignores this informational count; zero is omitted for compatibility.
	SkippedShadows int `json:"skipped_shadows,omitempty"`
}

// DataProvider is an administrator backup entry whose Credentials contain plaintext secrets.
// It has no parent_provider_id or quota_dimension; imports require credentials and cannot reconstruct shadow links.
// Spark shadows and their independent priority, concurrency, groups, and state are excluded from export.
type DataProvider struct {
	Name               string         `json:"name"`
	Notes              *string        `json:"notes,omitempty"`
	Platform           string         `json:"platform"`
	Type               string         `json:"type"`
	Credentials        map[string]any `json:"credentials"`
	Extra              map[string]any `json:"extra,omitempty"`
	ProxyKey           *string        `json:"proxy_key,omitempty"`
	Concurrency        *int           `json:"concurrency,omitempty"`
	Priority           *int           `json:"priority,omitempty"`
	RateMultiplier     *float64       `json:"rate_multiplier,omitempty"`
	ExpiresAt          *int64         `json:"expires_at,omitempty"`
	AutoPauseOnExpired *bool          `json:"auto_pause_on_expired,omitempty"`
}

type DataImportRequest struct {
	Data DataPayload `json:"data"`
}
type DataImportResult struct {
	ProxyCreated    int               `json:"proxy_created"`
	ProxyReused     int               `json:"proxy_reused"`
	ProxyFailed     int               `json:"proxy_failed"`
	ProviderCreated int               `json:"provider_created"`
	ProviderFailed  int               `json:"provider_failed"`
	Errors          []DataImportError `json:"errors,omitempty"`
}

// UnmarshalJSON decodes an entry while allowing unknown extension fields.
func (a *DataProvider) UnmarshalJSON(data []byte) error {
	type dataProviderAlias DataProvider

	var decoded dataProviderAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	*a = DataProvider(decoded)
	return nil
}

// ValidateHeader checks archive type, version, and required collections, accepting the legacy brand name.
// @project-doc docs/interfaces/http_api.md#product_name_compatibility
func ValidateHeader(payload DataPayload) error {
	if payload.Type != DataType && payload.Type != LegacyDataType {
		return fmt.Errorf("unsupported data type: %s", payload.Type)
	}
	if payload.Version != DataVersion {
		return fmt.Errorf("unsupported data version: %d", payload.Version)
	}
	if payload.Proxies == nil {
		return errors.New("proxies is required")
	}
	if payload.Providers == nil {
		return errors.New("providers is required")
	}
	return nil
}

// UnmarshalJSON rejects unknown payload fields, including the legacy accounts collection.
func (p *DataPayload) UnmarshalJSON(data []byte) error {
	type payload DataPayload
	var value payload
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	*p = DataPayload(value)
	return nil
}
