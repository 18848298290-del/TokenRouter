package openai

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestValidateImagesJSONResponse checks all native JSON consumers' acceptance
// rule. Invalid success envelopes cannot acquire a positive image count or be
// delivered as successful JSON merely because the upstream sent HTTP 200.
func TestValidateImagesJSONResponse(t *testing.T) {
	for _, body := range []string{
		`{"error":{"message":"blocked","code":"content_policy_violation"}}`,
		`{"error":{"message":"failed"},"data":[{"b64_json":"image"}]}`,
		`{"data":[]}`, `{"data":null}`, `{"data":[null]}`, `null`, `[]`, `invalid`,
		`{"data":[{"b64_json":"first"},{"url":9}]}`,
		`{"data":[{"b64_json":" "}]}`,
		`{"data":[{"b64_json":"image"}],"usage":[]}`,
	} {
		_, err := validateImagesJSONResponse([]byte(body))
		require.Error(t, err, body)
	}
	for _, body := range []string{
		`{"created":123,"data":[{"b64_json":"image"}]}`,
		`{"error":null,"data":[{"url":"https://example.invalid/image.png"}],"usage":null}`,
	} {
		root, err := validateImagesJSONResponse([]byte(body))
		require.NoError(t, err)
		require.Len(t, root.Get("data").Array(), 1)
	}
}
