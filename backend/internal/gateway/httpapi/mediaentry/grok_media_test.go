package mediaentry

import (
	"bytes"
	"context"
	"errors"
	"mime"
	"mime/multipart"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewaymedia "github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// Media query ownership can outlive a composite Key mapping, while image and
// video billing have different completion times. These scenarios inspect
// request-local Key restoration, scheduler capability/model feedback and JSON
// or multipart model rewriting without changing prompt content.

// compositeGrokVideoCacheStub supplies group-scoped provider and optional
// owner lookup fixtures.
type compositeGrokVideoCacheStub struct {
	// groupID and providerID form the one successful provider lookup; other
	// groups return not found regardless of the supplied task or request context.
	groupID    int64 // scripted successful group
	providerID int64 // scripted selected provider
	// ownerID supplies saved ownership only when positive. Nonpositive values make owner
	// lookup return not found so resolution can inspect current composite bindings.
	ownerID int64
}

// TestRecordGrokMediaUsageIgnoresNilResult supplies a nil result with all
// other dependencies absent. It checks recording returns without panic before
// any completion, pricing or request metadata is read.
func TestRecordGrokMediaUsageIgnoresNilResult(t *testing.T) {
	require.NotPanics(t, func() {
		recordGrokMediaUsage(
			nil, nil, nil, nil, authctx.AuthSubject{}, nil, nil, nil,
			"", routing.GroupMappingResult{}, nil, "",
		)
	})
}

// TestApplyGrokMediaGroupMappingRewritesForwardBody rewrites models in JSON
// and multipart bodies containing the old name in prompt text. It checks only
// the model field changes while Content-Type and prompt content remain usable.
func TestApplyGrokMediaGroupMappingRewritesForwardBody(t *testing.T) {
	jsonBody, contentType, err := applyGrokMediaGroupMapping(
		[]byte(`{"model":"key-target","prompt":"keep key-target in text"}`),
		"application/json",
		routing.GroupMappingResult{Mapped: true, MappedModel: "channel-target"},
	)
	require.NoError(t, err)
	require.Equal(t, "application/json", contentType)
	require.JSONEq(t, `{"model":"channel-target","prompt":"keep key-target in text"}`, string(jsonBody))

	var multipartBody bytes.Buffer
	writer := multipart.NewWriter(&multipartBody)
	require.NoError(t, writer.WriteField("model", "key-target"))
	require.NoError(t, writer.WriteField("prompt", "keep key-target in text"))
	require.NoError(t, writer.Close())
	rewritten, rewrittenType, err := applyGrokMediaGroupMapping(
		multipartBody.Bytes(),
		writer.FormDataContentType(),
		routing.GroupMappingResult{Mapped: true, MappedModel: "channel-target"},
	)
	require.NoError(t, err)
	reader, err := multipart.NewReader(bytes.NewReader(rewritten), multipartBoundaryForTest(t, rewrittenType)).ReadForm(1 << 20)
	require.NoError(t, err)
	require.Equal(t, "channel-target", reader.Value["model"][0])
	require.Equal(t, "keep key-target in text", reader.Value["prompt"][0])
}

// multipartBoundaryForTest parses the Content-Type emitted by a multipart
// fixture and returns its boundary. Malformed Content-Type fails the current
// test, allowing the caller to decode the rewritten request form.
func multipartBoundaryForTest(t *testing.T, contentType string) string {
	t.Helper()
	_, params, err := mime.ParseMediaType(contentType)
	require.NoError(t, err)
	return params["boundary"]
}

// TestShouldRecordGrokMediaUsage compares image endpoints with video creation,
// status, content and missing-model cases. It checks immediate recording
// requires image units and rejects nil or zero-unit results in the supplied
// fixtures.
func TestShouldRecordGrokMediaUsage(t *testing.T) {
	tests := []struct {
		name     string                 // scenario subtest label
		endpoint grok.GrokMediaEndpoint // selected media endpoint
		model    string                 // request model identity
		want     bool                   // expected immediate recording result
	}{
		{
			name:     "image generation records usage",
			endpoint: grok.GrokMediaEndpointImagesGenerations,
			model:    "grok-imagine",
			want:     true,
		},
		{
			name:     "image edit records usage",
			endpoint: grok.GrokMediaEndpointImagesEdits,
			model:    "grok-imagine-edit",
			want:     true,
		},
		{
			name:     "video generation defers usage until status",
			endpoint: grok.GrokMediaEndpointVideosGenerations,
			model:    "grok-imagine-video-1.5",
			want:     false,
		},
		{
			name:     "video status skips immediate helper (status path claims separately)",
			endpoint: grok.GrokMediaEndpointVideoStatus,
			model:    "",
			want:     false,
		},
		{
			name:     "video content skips usage",
			endpoint: grok.GrokMediaEndpointVideoContent,
			model:    "",
			want:     false,
		},
		{
			name:     "generation skips usage without model",
			endpoint: grok.GrokMediaEndpointImagesGenerations,
			model:    " ",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A missing result has no billable usage.
			require.False(t, shouldRecordGrokMediaUsage(tt.endpoint, tt.model, nil))
			// Images are billed immediately; asynchronous videos are billed on status queries.
			result := &forwardcore.OpenAIResult{ImageCount: 1, VideoCount: 0}
			if tt.endpoint.IsGenerationRequest() && !isGrokVideoCreateEndpoint(tt.endpoint) && strings.TrimSpace(tt.model) != "" {
				require.Equal(t, tt.want, shouldRecordGrokMediaUsage(tt.endpoint, tt.model, result))
			} else {
				require.False(t, shouldRecordGrokMediaUsage(tt.endpoint, tt.model, result))
			}
			// Zero billable units skip settlement.
			empty := &forwardcore.OpenAIResult{}
			require.False(t, shouldRecordGrokMediaUsage(tt.endpoint, tt.model, empty))
		})
	}
}

// TestGrokMediaRequiredCapability compares creation endpoints with task status
// and content queries. It checks only creation requests require new-media
// scheduler capability while old-task lookups receive no such capability
// requirement.
func TestGrokMediaRequiredCapability(t *testing.T) {
	tests := []struct {
		name     string                                // scenario subtest label
		endpoint grok.GrokMediaEndpoint                // selected media endpoint
		want     providercore.OpenAIEndpointCapability // expected scheduler capability result
	}{
		{name: "image generation", endpoint: grok.GrokMediaEndpointImagesGenerations, want: providercore.OpenAIEndpointCapabilityGrokMediaGeneration},
		{name: "image edit", endpoint: grok.GrokMediaEndpointImagesEdits, want: providercore.OpenAIEndpointCapabilityGrokMediaGeneration},
		{name: "video generation", endpoint: grok.GrokMediaEndpointVideosGenerations, want: providercore.OpenAIEndpointCapabilityGrokMediaGeneration},
		{name: "video edit", endpoint: grok.GrokMediaEndpointVideosEdits, want: providercore.OpenAIEndpointCapabilityGrokMediaGeneration},
		{name: "video extension", endpoint: grok.GrokMediaEndpointVideosExtensions, want: providercore.OpenAIEndpointCapabilityGrokMediaGeneration},
		{name: "video status preserves lookup", endpoint: grok.GrokMediaEndpointVideoStatus, want: ""},
		{name: "video content preserves lookup", endpoint: grok.GrokMediaEndpointVideoContent, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, grokMediaRequiredCapability(tt.endpoint))
		})
	}
}

// TestGrokMediaScheduleModelUsesNormalizedMappedUpstream varies returned
// upstream model, empty result and missing provider. It checks observed
// identity wins, then provider mapping, with trimmed routing identity used for
// the provider-free case.
func TestGrokMediaScheduleModelUsesNormalizedMappedUpstream(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok,
			Credentials: map[string]any{
				"model_mapping": map[string]any{
					"grok-imagine-video-1.5": "wrong-raw-model",
					"grok-imagine-video":     "mapped-video-model",
				},
			},
		},
	}

	require.Equal(t, "mapped-video-model", grokMediaScheduleModel(provider, "grok-imagine-video", nil))
	require.Equal(t, "actual-upstream-model", grokMediaScheduleModel(provider, "grok-imagine-video", &forwardcore.OpenAIResult{
		UpstreamModel: "actual-upstream-model",
	}))
	require.Equal(t, "mapped-video-model", grokMediaScheduleModel(provider, "grok-imagine-video", &forwardcore.OpenAIResult{}))
	require.Equal(t, "grok-imagine-video", grokMediaScheduleModel(nil, " grok-imagine-video ", nil))
}

// GetSessionProviderID returns the configured provider for the one matching
// group. Other groups report not found; request context and task key are
// ignored by this ownership fixture.
func (s *compositeGrokVideoCacheStub) GetSessionProviderID(_ context.Context, groupID int64, _ string) (int64, error) {
	if groupID == s.groupID {
		return s.providerID, nil
	}
	return 0, errors.New("not found")
}

// SetSessionProviderID accepts fixture binding writes and returns success
// without retaining data.
func (s *compositeGrokVideoCacheStub) SetSessionProviderID(context.Context, int64, string, int64, time.Duration) error {
	return nil
}

// RefreshSessionTTL accepts fixture expiration refresh and leaves configured
// lookup values unchanged.
func (s *compositeGrokVideoCacheStub) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}

// DeleteSessionProviderID accepts fixture deletion and retains the scripted
// provider lookup value.
func (s *compositeGrokVideoCacheStub) DeleteSessionProviderID(context.Context, int64, string) error {
	return nil
}

// SetSessionOwnerGroupID reports successful fixture ownership assignment
// without changing lookup state.
func (s *compositeGrokVideoCacheStub) SetSessionOwnerGroupID(context.Context, int64, string, string, int64, time.Duration) (bool, error) {
	return true, nil
}

// GetSessionOwnerGroupID returns the configured positive owner, or not found
// when ownerID is nonpositive. It ignores request identity arguments so each test
// controls ownership presence through the fixture field.
func (s *compositeGrokVideoCacheStub) GetSessionOwnerGroupID(context.Context, int64, string, string) (int64, error) {
	if s.ownerID > 0 {
		return s.ownerID, nil
	}
	return 0, errors.New("not found")
}

// TestResolveCompositeGrokVideoAPIKeyUsesPersistedOwnerAfterMappingRemoval
// queries a saved task with no current composite mappings. It checks its
// persisted provider/group are restored into a hydrated minimal view for the
// request.
func TestResolveCompositeGrokVideoAPIKeyUsesPersistedOwnerAfterMappingRemoval(t *testing.T) {
	cache := &compositeGrokVideoCacheStub{groupID: 20, providerID: 88, ownerID: 20}
	// Use the persisted task owner even when the key no longer has the group mapping.
	tasks := gatewaymedia.NewVideoTasks(cache, nil, gatewaymedia.VideoOptions{})
	handler := New(Bindings{Dependencies: gatewayhttp.OpenAIDependencies{Gateway: true}, VideoTasks: func() *gatewaymedia.VideoTasks { return tasks }})

	apiKey := &apikey.APIKey{ID: 33, UserID: 44, IsComposite: true}

	selected, providerID, err := handler.resolveCompositeGrokVideoAPIKey(context.Background(), apiKey, "video-123", apiKey.UserID)

	require.NoError(t, err)
	require.Equal(t, int64(88), providerID)
	require.Equal(t, int64(20), *selected.GroupID)
	require.True(t, selected.Group.Hydrated)
}

// RefreshSessionOwnerTTL accepts fixture ownership expiration refresh and
// returns success.
func (s *compositeGrokVideoCacheStub) RefreshSessionOwnerTTL(context.Context, int64, string, string, time.Duration) error {
	return nil
}

// TestResolveCompositeGrokVideoAPIKeyRestoresBoundGroup supplies current
// OpenAI and Grok mappings with a task-bound Grok provider. It checks the
// matching Group pointer is reused and the original authenticated Key's
// GroupID remains nil.
func TestResolveCompositeGrokVideoAPIKeyRestoresBoundGroup(t *testing.T) {
	openAIGroup := &routing.Group{ID: 10, Status: billing.StatusActive}
	grokGroup := &routing.Group{ID: 20, Status: billing.StatusActive}
	cache := &compositeGrokVideoCacheStub{groupID: grokGroup.ID, providerID: 88}
	// Resolve the task against the key's composite groups.
	tasks := gatewaymedia.NewVideoTasks(cache, nil, gatewaymedia.VideoOptions{})
	handler := New(Bindings{Dependencies: gatewayhttp.OpenAIDependencies{Gateway: true}, VideoTasks: func() *gatewaymedia.VideoTasks { return tasks }})

	apiKey := &apikey.APIKey{
		ID: 33, UserID: 44, IsComposite: true,
		CompositeGroups: []apikey.APIKeyCompositeGroup{
			{GroupID: openAIGroup.ID, Prefix: "GPT", Group: openAIGroup},
			{GroupID: grokGroup.ID, Prefix: "Grok", Group: grokGroup},
		},
	}

	selected, providerID, err := handler.resolveCompositeGrokVideoAPIKey(context.Background(), apiKey, "video-123", apiKey.UserID)

	require.NoError(t, err)
	require.Equal(t, int64(88), providerID)
	require.NotNil(t, selected.GroupID)
	require.Equal(t, grokGroup.ID, *selected.GroupID)
	require.Same(t, grokGroup, selected.Group)
	require.Nil(t, apiKey.GroupID, "请求级恢复不能修改认证缓存中的复合 Key")
}

// applyGrokMediaGroupMapping delegates model rewriting with the mapping's
// applied flag and destination. Absent or unusable mapping keeps body and
// Content-Type; selected mappings use the Grok codec for the representation.
func applyGrokMediaGroupMapping(body []byte, contentType string, mapping routing.GroupMappingResult) ([]byte, string, error) {
	return gatewaymedia.RewriteMappedMediaBody(body, contentType, mapping.Mapped, mapping.MappedModel, gatewayprovider.GrokMediaCodec().RewriteGrokMediaRequestModel)
}
