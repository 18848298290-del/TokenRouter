package provider

import (
	"encoding/json"
	"errors"
	"strings"
)

const VertexDefaultLocation = "us-central1"

func (r *Record) VertexLocation(model string) string {
	if r == nil {
		return VertexDefaultLocation
	}
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

// VertexProjectID prefers the configured project ID, then parses service account credentials; failures return
// an empty string.
func (r *Record) VertexProjectID(parseProject func([]byte) (string, error)) string {
	if r == nil {
		return ""
	}
	if v := strings.TrimSpace(r.GetCredential("project_id")); v != "" {
		return v
	}
	raw, err := VertexServiceAccountJSON(r)
	if err != nil {
		return ""
	}
	value, err := parseProject(raw)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}
