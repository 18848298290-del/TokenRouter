package transfer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/egress"
)

// archive.go defines provider archive data and validates its outer format and
// required collections.
// Provider archives identify a versioned format shared with proxy import and
// export. Version two accepts the current and legacy product identifiers,
// while the outer payload rejects unknown fields so a retired accounts
// collection cannot silently bypass provider import validation.
//
// Individual provider entries permit extension fields and represent optional
// values with pointers. Their importer converts omission into the creation
// API's defaults and resolves proxy keys after importing proxies. Exports can
// include plaintext credentials and omit Spark shadows, whose parent
// relationship and independent state cannot be reconstructed from this entry
// format; the skipped count is informational.

// Archive validation accepts these product identifiers only at the supported
// version, keeping historical naming compatible while requiring the
// current provider collection format.
const (
	DataType = "tokenrouter-data" // current archive product identifier
	// LegacyDataType accepts same-version exports made under the former product name.
	LegacyDataType = "sub2api-data"
	DataVersion    = 2 // supported provider archive version
)

type (
	// DataProxy aliases the egress proxy entry shared by provider archives.
	DataProxy = egress.TransferProxy
	// DataImportError aliases the per-item failures reported by egress imports.
	DataImportError = egress.TransferError
)

// DataPayload is the versioned provider/proxy archive exchanged by
// administrator import and export.
type DataPayload struct {
	// Type and Version identify the supported archive together: ValidateHeader
	// accepts current or legacy product naming with version two and rejects omitted
	// identifiers or other versions before provider processing.
	Type       string `json:"type,omitempty"`    // archive product type identifier
	Version    int    `json:"version,omitempty"` // archive schema version number
	ExportedAt string `json:"exported_at"`       // UTC RFC3339 export timestamp
	// Proxies and Providers must both be nonnil for header validation; empty
	// arrays are valid archives. Archive.Import processes proxies before providers
	// so each provider can resolve its proxy key against that import result.
	Proxies   []DataProxy    `json:"proxies"`   // exported proxy connection entries
	Providers []DataProvider `json:"providers"` // exported root provider entries
	// SkippedShadows reports how many Spark shadow providers export excluded.
	// Import ignores this informational count; zero is omitted for compatibility.
	SkippedShadows int `json:"skipped_shadows,omitempty"`
}

// DataProvider is an administrator archive entry with plaintext credential values.
// Its format omits parent and quota-dimension links, so importing these entries
// cannot reconstruct Spark shadows. Export excludes their separate priority,
// concurrency, group, and health state together with the shadow entry.
type DataProvider struct {
	Name string `json:"name"` // provider display name text
	// Notes is passed through archive cloning and normalized by provider
	// creation; nil or blank input yields no stored provider note.
	Notes    *string `json:"notes,omitempty"` // optional provider note text
	Platform string  `json:"platform"`        // destination provider platform identifier
	Type     string  `json:"type"`            // destination provider authentication type
	// Credentials and Extra retain decoded mutable maps until Archive clones the
	// entry for creation. Credentials are required and contain plaintext
	// secrets; creation normalizes configuration and removes imported managed
	// observations.
	Credentials map[string]any `json:"credentials"` // plaintext provider credential object
	// Extra: provider configuration extension object
	Extra map[string]any `json:"extra,omitempty"`
	// ProxyKey binds to a successfully imported or reused proxy entry. Nil or
	// empty selects no proxy; an unresolved nonempty key fails that provider
	// item.
	ProxyKey *string `json:"proxy_key,omitempty"` // imported proxy lookup key
	// Concurrency and Priority use pointers so exported zero values remain
	// present. Archive converts nil input to zero for creation; concurrency then
	// follows platform normalization while priority retains its supplied value.
	// Concurrency: configured simultaneous request capacity
	Concurrency *int `json:"concurrency,omitempty"`
	// Priority: provider scheduler priority value
	Priority *int `json:"priority,omitempty"`
	// RateMultiplier is optional creation input. Nil leaves the schema default,
	// zero is accepted, and negative values fail archive validation before the
	// provider is created.
	// RateMultiplier: provider cost multiplier value
	RateMultiplier *float64 `json:"rate_multiplier,omitempty"`
	// ExpiresAt becomes a provider expiration time only when its Unix-seconds
	// value is positive. Nil or nonpositive input creates a provider with no
	// expiration.
	ExpiresAt *int64 `json:"expires_at,omitempty"` // Unix seconds
	// AutoPauseOnExpired overrides the creation switch when present. Omitted or
	// null values leave creation to default this switch to true.
	// AutoPauseOnExpired: expiration scheduling switch value
	AutoPauseOnExpired *bool `json:"auto_pause_on_expired,omitempty"`
}

// DataImportRequest wraps the administrator archive payload accepted by the
// import handler.
type DataImportRequest struct {
	Data DataPayload `json:"data"` // submitted provider archive payload
}

// DataImportResult reports per-kind success and failure counts after partial
// archive processing.
type DataImportResult struct {
	ProxyCreated    int `json:"proxy_created"`    // new proxy entries persisted
	ProxyReused     int `json:"proxy_reused"`     // existing proxy entries matched
	ProxyFailed     int `json:"proxy_failed"`     // proxy entries rejected individually
	ProviderCreated int `json:"provider_created"` // new provider entries persisted
	// ProviderFailed: provider entries rejected individually
	ProviderFailed int `json:"provider_failed"`
	// Errors combines proxy-import failures with provider validation,
	// proxy-binding and creation failures in processing order; a successful
	// partial import may still return these entries.
	Errors []DataImportError `json:"errors,omitempty"` // per-item import failure details
}

// UnmarshalJSON decodes a provider entry through an alias while permitting
// unknown extension fields. It assigns the receiver only after successful
// decoding; omitted or null optional pointers remain nil and retain the
// archive importer's creation-default behavior.
func (a *DataProvider) UnmarshalJSON(data []byte) error {
	// dataProviderAlias prevents recursive entry decoding while retaining
	// the same field tags.
	type dataProviderAlias DataProvider

	var decoded dataProviderAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	*a = DataProvider(decoded)
	return nil
}

// ValidateHeader accepts version-two archives using either supported product
// identifier and requires nonnil proxy/provider slices. Allocated empty
// collections are valid, while omitted or null collections fail; nested
// provider validation and creation remain with the importing service.
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

// UnmarshalJSON decodes the outer archive through an alias with unknown-field
// rejection. It assigns the receiver only after successful decoding; header
// values and required collection presence are checked separately by
// ValidateHeader after decoding.
func (p *DataPayload) UnmarshalJSON(data []byte) error {
	// payload uses the archive fields directly while bypassing this
	// custom decoder method.
	type payload DataPayload
	var value payload
	decoder := json.NewDecoder(bytes.NewReader(data))
	// Reject retired or misspelled outer collections before import consumes them;
	// provider-entry decoding remains extensible through its separate
	// custom JSON method.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	*p = DataPayload(value)
	return nil
}
