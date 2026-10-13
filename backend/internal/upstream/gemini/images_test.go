package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGenerateImagesLocalWireAndLastOutput returns two inline image spellings
// from a local TLS generation endpoint. It checks controls,
// post-authentication header override, selection of the last image, zero
// indexing, and response/lifecycle release.
func TestGenerateImagesLocalWireAndLastOutput(t *testing.T) {
	first, last := []byte("\x89PNG\r\n\x1a\nfirst"), []byte("\x89PNG\r\n\x1a\nlast")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1beta/models/gemini-image:generateContent", r.URL.Path)
		require.Equal(t, "overridden-fixture", r.Header.Get("x-goog-api-key"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		config, ok := body["generationConfig"].(map[string]any)
		require.True(t, ok)
		imageConfig, ok := config["imageConfig"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "2K", imageConfig["imageSize"])
		thinkingConfig, ok := config["thinkingConfig"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, false, thinkingConfig["includeThoughts"])
		_, _ = fmt.Fprintf(w, `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":%q}},{"inline_data":{"mime_type":"image/png","data":%q}}]}}]}`, base64.StdEncoding.EncodeToString(first), base64.StdEncoding.EncodeToString(last))
	}))
	defer server.Close()
	var closes, releases atomic.Int32
	options := ImageOptions{Mode: APIKeyCredential, Model: "gemini-image", APIKey: func() string { return "fixture" }, BaseURL: func() string { return server.URL }, ValidateGeminiBaseURL: func(v string) (string, error) { return v, nil }, ApplyHeaders: func(h http.Header) { h.Set("x-goog-api-key", "overridden-fixture") }, Do: func(req *http.Request) (*http.Response, error) {
		res, err := server.Client().Do(req)
		if res != nil {
			res.Body = &executeBody{ReadCloser: res.Body, closes: &closes}
		}
		return res, err
	}, HTTPError: func(status int, message string) error { return fmt.Errorf("http %d: %s", status, message) }, Invalid: fmt.Errorf, ErrorMessage: func(b []byte) string { return string(b) }, Enter: func() (func(), error) { return func() { releases.Add(1) }, nil }}
	outputs, err := GenerateImages(context.Background(), BuildImageRequest(ImageRequestInput{Prompt: "fixture", ImageSize: "2K", ThinkingLevel: "low"}), options)
	require.NoError(t, err)
	require.Len(t, outputs, 1)
	require.Equal(t, last, outputs[0].Bytes)
	require.Equal(t, 0, outputs[0].Index)
	require.EqualValues(t, 1, closes.Load())
	require.EqualValues(t, 1, releases.Load())
}
