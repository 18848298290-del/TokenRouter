package provider

import (
	"encoding/json"
	"errors"
	"strings"
)

// Vertex requests choose location and project separately from service-account
// JSON. A model-specific nonblank location wins over record location fields,
// while an administrator-supplied project bypasses JSON parsing and missing
// project evidence becomes empty text.
//
// Credential selection accepts historical service_account_json and
// service_account keys in scalar text or map form. Both text keys are
// considered before either map, so a usable legacy string wins over a map
// under the newer key. Map encoding errors are discarded at selection; the
// owning parser validates resulting bytes before exchange. Callers validate
// the selected identity and endpoint, and nil records receive the default
// location or a credential error according to the entry point.

// VertexDefaultLocation supplies the regional endpoint fallback when neither
// model nor record location fields provide a usable value.
const VertexDefaultLocation = "us-central1"

// VertexLocation selects a trimmed model override, location, vertex_location,
// then the default region. Nil records select the default; model overrides
// require map[string]any with a nonblank string, and endpoint callers validate
// the returned location.
func (r *Record) VertexLocation(model string) string {
	if r == nil {
		return VertexDefaultLocation
	}
	// Exact model locations are checked before common record fields so a regional
	// exception applies only to that selected model while other requests retain
	// their usual location.
	if model != "" && r.Credentials != nil {
		if raw, ok := r.Credentials["vertex_model_locations"].(map[string]any); ok {
			if loc, ok := raw[model].(string); ok && strings.TrimSpace(loc) != "" {
				return strings.TrimSpace(loc)
			}
		}
	}
	if v := strings.TrimSpace(r.GetCredential("location")); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.GetCredential("vertex_location")); v != "" {
		return v
	}
	return VertexDefaultLocation
}

// VertexServiceAccountJSON returns selected service-account credential bytes
// or a missing-configuration error. Scalar credential text is trimmed and
// preferred in newer-key then legacy-key order before either map form.
//
// Map forms are JSON-encoded into fresh bytes, with marshal errors discarded,
// so invalid map values can produce nil bytes and nil error. This helper
// selects representations; the platform parser validates them before token
// exchange. Keep the provider stable during selection and separately handle
// the returned parsing result.
func VertexServiceAccountJSON(provider *Record) ([]byte, error) {
	if provider == nil || provider.Credentials == nil {
		return nil, errors.New("service account credentials not configured")
	}

	if raw := strings.TrimSpace(provider.GetCredential("service_account_json")); raw != "" {
		return []byte(raw), nil
	}
	if raw := strings.TrimSpace(provider.GetCredential("service_account")); raw != "" {
		return []byte(raw), nil
	}
	// Scalar text under either historical key has already won. Encode map forms
	// next and discard encoding errors here. The platform parser receives the
	// resulting bytes and performs its own credential validation.
	if nested, ok := provider.Credentials["service_account_json"].(map[string]any); ok {
		b, _ := json.Marshal(nested)
		return b, nil
	}
	if nested, ok := provider.Credentials["service_account"].(map[string]any); ok {
		b, _ := json.Marshal(nested)
		return b, nil
	}
	return nil, errors.New("service_account_json not found in credentials")
}

// VertexProjectID returns a trimmed explicit project or one extracted by
// parseProject. Nil records return empty text, and the parser callback is
// required when no explicit project exists. Credential or parsing errors
// become an empty result for caller validation.
func (r *Record) VertexProjectID(parseProject func([]byte) (string, error)) string {
	if r == nil {
		return ""
	}
	if v := strings.TrimSpace(r.GetCredential("project_id")); v != "" {
		return v
	}
	raw, err := VertexServiceAccountJSON(r)
	if err != nil {
		// Missing credential bytes cannot establish a discovered project. Return the
		// empty identity for endpoint callers to validate before exchange.
		return ""
	}
	value, err := parseProject(raw)
	// Project parsing is best effort at this getter; its failure leaves a blank
	// identity that request preparation can reject or replace according to its
	// platform rules.
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}
