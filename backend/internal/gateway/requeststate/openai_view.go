package requeststate

import (
	"errors"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
)

type OpenAIRequestView struct {
	body               []byte
	Model              string
	Stream             bool
	PromptCacheKey     string
	PreviousResponseID string
	ServiceTier        string
	HasServiceTier     bool
	ReasoningEffort    string
	patches            []openAIRequestPatch
	patchesDisabled    bool
}

type openAIRequestPatch struct {
	path   string
	delete bool
	value  any
}

// DecodeOpenAIRequestBody decodes numbers without float conversion and prefixes parse errors.
func DecodeOpenAIRequestBody(body []byte) (map[string]any, error) {
	var request map[string]any
	if err := wirejson.DecodeUseNumber(body, &request); err != nil {
		return nil, fmt.Errorf("parse request: %w", err)
	}
	return request, nil
}

func NewOpenAIRequestView(body []byte) OpenAIRequestView {
	if len(body) == 0 {
		return OpenAIRequestView{}
	}
	const (
		modelField uint8 = 1 << iota
		streamField
		promptCacheKeyField
		previousResponseIDField
		serviceTierField
		reasoningField
		allRequestViewFields = modelField | streamField | promptCacheKeyField |
			previousResponseIDField | serviceTierField | reasoningField
	)

	view := OpenAIRequestView{body: body}
	var seen uint8
	// The view retains the caller's body bytes; callers must keep them unchanged while using the view.
	wirejson.ParseView(body).ForEach(func(key, value gjson.Result) bool {
		switch key.Str {
		case "model":
			if seen&modelField == 0 {
				view.Model = strings.TrimSpace(value.String())
				seen |= modelField
			}
		case "stream":
			if seen&streamField == 0 {
				view.Stream = value.Bool()
				seen |= streamField
			}
		case "prompt_cache_key":
			if seen&promptCacheKeyField == 0 {
				view.PromptCacheKey = strings.TrimSpace(value.String())
				seen |= promptCacheKeyField
			}
		case "previous_response_id":
			if seen&previousResponseIDField == 0 {
				view.PreviousResponseID = strings.TrimSpace(value.String())
				seen |= previousResponseIDField
			}
		case "service_tier":
			if seen&serviceTierField == 0 {
				view.ServiceTier = strings.TrimSpace(value.String())
				view.HasServiceTier = value.Exists()
				seen |= serviceTierField
			}
		case "reasoning":
			if seen&reasoningField == 0 {
				view.ReasoningEffort = strings.TrimSpace(value.Get("effort").String())
				seen |= reasoningField
			}
		}
		return seen != allRequestViewFields
	})
	return view
}

// Decode parses the original body into a full object; queued patches are applied separately.
func (v OpenAIRequestView) Decode() (map[string]any, error) {
	return DecodeOpenAIRequestBody(v.Bytes())
}

// OpenAIRequestMetaFromBody extracts model, stream, and cache key through a field view.
func OpenAIRequestMetaFromBody(body []byte) (model string, stream bool, promptCacheKey string) {
	view := NewOpenAIRequestView(body)
	return view.Model, view.Stream, view.PromptCacheKey
}

func (v *OpenAIRequestView) MarkPatchSet(path string, value any) {
	if v == nil || v.patchesDisabled {
		return
	}
	path = strings.TrimSpace(path)
	if !isSimpleOpenAIRequestPatchPath(path) {
		v.DisablePatches()
		return
	}
	v.patches = append(v.patches, openAIRequestPatch{path: path, value: value})
}

func (v *OpenAIRequestView) MarkPatchDelete(path string) {
	if v == nil || v.patchesDisabled {
		return
	}
	path = strings.TrimSpace(path)
	if !isSimpleOpenAIRequestPatchPath(path) {
		v.DisablePatches()
		return
	}
	v.patches = append(v.patches, openAIRequestPatch{path: path, delete: true})
}

func isSimpleOpenAIRequestPatchPath(path string) bool {
	if path == "" || strings.ContainsRune(path, '\\') {
		return false
	}
	for part := range strings.SplitSeq(path, ".") {
		if strings.TrimSpace(part) == "" {
			return false
		}
	}
	return true
}

func (v *OpenAIRequestView) DisablePatches() {
	if v == nil {
		return
	}
	v.patchesDisabled = true
	v.patches = nil
}

func (v OpenAIRequestView) HasPatches() bool {
	return !v.patchesDisabled && len(v.patches) > 0
}

func (v OpenAIRequestView) ApplyPatches() ([]byte, error) {
	if v.patchesDisabled || len(v.patches) == 0 {
		return nil, errors.New("openai request patches disabled")
	}
	body := v.body
	for _, patch := range v.patches {
		var err error
		if patch.delete {
			body, err = sjson.DeleteBytes(body, patch.path)
		} else {
			body, err = sjson.SetBytes(body, patch.path, patch.value)
		}
		if err != nil {
			return nil, err
		}
	}
	return body, nil
}

// Bytes returns the original body slice; callers must treat it as read-only.
func (v OpenAIRequestView) Bytes() []byte { return v.body }

// PatchesDisabled reports whether the caller must use the full-object rewrite path.
func (v OpenAIRequestView) PatchesDisabled() bool { return v.patchesDisabled }
