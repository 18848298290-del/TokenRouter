package httpapi

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// unreadableMediaBody counts body reads and returns a fixed admission-test
// failure.
type unreadableMediaBody struct {
	reads int // observed request body reads
}

// videoResolutionProbe overrides parsed video facts while sharing ordinary
// authenticated HTTP test ports. The handler can reject invalid labels before
// any provider selection without requiring a network fixture.
type videoResolutionProbe struct {
	*mediaHTTPProbe        // authenticated base HTTP ports
	resolution      string // parsed video price label
}

// Read counts unexpected admission body access and returns a fixed error.
func (b *unreadableMediaBody) Read([]byte) (int, error) {
	b.reads++
	return 0, errors.New("must not read")
}

// Close finishes the counter-only media body without another error.
func (b *unreadableMediaBody) Close() error { return nil }

// mediaHTTPContext creates a request and Gin recorder for the supplied media
// route. Returned storage belongs to that test invocation, allowing callers
// to replace the body and inspect the resulting response.
func mediaHTTPContext(method, path string, body io.Reader) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, path, body)
	return c, rec
}

// TestMediaHTTPAuthenticationAndPlatformChecksDoNotReadBody invokes
// unauthenticated Images, Embeddings, AlphaSearch and Voice entry. Each
// expected authentication or unsupported response arrives before any read of
// the failing request body.
func TestMediaHTTPAuthenticationAndPlatformChecksDoNotReadBody(t *testing.T) {
	for _, kind := range []string{"images", "embeddings", "alpha", "voice"} {
		t.Run(kind, func(t *testing.T) {
			p := &mediaHTTPProbe{t: t}
			body := &unreadableMediaBody{}
			c, rec := mediaHTTPContext(http.MethodPost, "/v1/test", nil)
			c.Request.Body = body
			switch kind {
			case "images":
				NewMediaHandler(p).Images(c)
			case "embeddings":
				NewAuxiliaryHandler(p).Embeddings(c)
			case "alpha":
				NewAuxiliaryHandler(p).AlphaSearch(c)
			case "voice":
				NewAuxiliaryHandler(p).GrokVoice(c, "tts")
			}
			expected := 401
			if kind == "voice" {
				expected = 404
			}
			require.Equal(t, expected, rec.Code)
			require.Zero(t, body.reads)
		})
	}
}

// TestImagesHTTPValidatesMappedModelBeforeFeatureDenial supplies an image
// alias with a group mapping while image permission is absent. The recorded
// call order plans routing before presenting feature denial.
func TestImagesHTTPValidatesMappedModelBeforeFeatureDenial(t *testing.T) {
	p := &mediaHTTPProbe{t: t, access: &MediaAccess{ID: 2}, mapping: routing.GroupMappingResult{Mapped: true, MappedModel: "gpt-image-2"}}
	c, rec := mediaHTTPContext(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"alias","prompt":"cat"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	NewMediaHandler(p).Images(c)
	require.Equal(t, 403, rec.Code)
	require.Equal(t, []string{"auth", "plan", "denied"}, p.steps)
}

// TestGrokVideoLookupSkipsBodyAndRequiresTaskID invokes video status with a
// missing task identifier and unreadable body. The entry returns its
// required-ID error and records zero body reads.
func TestGrokVideoLookupSkipsBodyAndRequiresTaskID(t *testing.T) {
	p := &mediaHTTPProbe{t: t, access: &MediaAccess{ID: 2}}
	body := &unreadableMediaBody{}
	c, rec := mediaHTTPContext(http.MethodGet, "/v1/videos/", nil)
	c.Request.Body = body
	NewMediaHandler(p).GrokMedia(c, "video_status", "")
	require.Equal(t, 400, rec.Code)
	require.Contains(t, rec.Body.String(), "request_id is required")
	require.Zero(t, body.reads)
}

// ParseGrok supplies a model and the scripted video label for validation.
func (p videoResolutionProbe) ParseGrok(string, []byte) GrokMediaInput {
	return GrokMediaInput{Model: "custom-video", VideoResolution: p.resolution}
}

// TestVideoHTTPRejectsUnpersistableResolution submits oversized and control-bearing
// labels to the creation endpoint. Both fail with a client error before routing
// or funding can start work that would later fail usage persistence.
func TestVideoHTTPRejectsUnpersistableResolution(t *testing.T) {
	for _, resolution := range []string{strings.Repeat("a", 51), "a\x00b"} {
		p := videoResolutionProbe{mediaHTTPProbe: &mediaHTTPProbe{t: t, access: &MediaAccess{ID: 2}}, resolution: resolution}
		c, rec := mediaHTTPContext(http.MethodPost, "/v1/videos/generations", strings.NewReader(`{"model":"custom-video"}`))
		NewMediaHandler(p).GrokMedia(c, "videos_generations", "")
		require.Equal(t, 400, rec.Code)
		require.Contains(t, rec.Body.String(), "video resolution")
		require.NotContains(t, p.steps, "plan")
	}
}
