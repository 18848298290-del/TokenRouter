package requeststate

import (
	"errors"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
)

// openai_view.go extracts Responses request fields and applies queued edits to
// retained JSON bytes. The byte view retains the first occurrence of each
// recognized field and can read a useful prefix even when later JSON remains
// incomplete.
//
// Full Decode applies complete-document validation and retains interface-held
// numbers as json.Number. Simple edits are queued against the original bytes
// and applied in order, allowing unrelated raw payload spelling to survive a
// rewrite. An unsupported path disables and clears that queue, directing
// callers to their decoded-object path. The view retains body and patch values
// rather than cloning them; callers serialize queue changes and keep retained
// storage stable while applying or reading it.

// OpenAIRequestView holds lenient scalar evidence and a patch queue over
// retained request bytes. Callers keep body storage and mutable patch values
// stable and synchronize queue access for their request.
type OpenAIRequestView struct {
	// body stays unchanged across ApplyPatches and Decode. Callers treat its
	// backing storage as immutable while scalar strings or the view remain in use.
	body               []byte // original retained request bytes
	Model              string // trimmed first model field
	Stream             bool   // first stream field boolean
	PromptCacheKey     string // trimmed first prompt-cache key
	PreviousResponseID string // trimmed first continuation response ID
	// ServiceTier and HasServiceTier retain spelling and presence independently.
	// A supplied null value can be present with an empty string; callers perform
	// tier validation separately from this lenient field view.
	ServiceTier     string // trimmed first service-tier spelling
	HasServiceTier  bool   // first service-tier field presence
	ReasoningEffort string // trimmed first nested effort
	// patches retains values supplied to MarkPatchSet. Callers leave mutable
	// values unchanged until application and serialize queue updates with reads.
	patches         []openAIRequestPatch // ordered queued request changes
	patchesDisabled bool                 // decoded rewrite required flag
}

// openAIRequestPatch records one queued path deletion or replacement value.
type openAIRequestPatch struct {
	// path selects the changed field, and delete chooses removal rather than
	// encoding value. A retained value supplies replacement data for set patches
	// and stays unused when deletion is selected.
	path   string // trimmed sjson patch path
	delete bool   // delete operation for this path
	// value is shared with the queued caller input. It is ignored for deletions
	// and must stay stable until ApplyPatches encodes it.
	value any // retained replacement value
}

// DecodeOpenAIRequestBody decodes one complete JSON value into an object map
// and retains interface-held numbers as json.Number. Parse errors carry the
// parse-request prefix; JSON null yields a nil map, and incompatible root
// values fail decoding.
func DecodeOpenAIRequestBody(body []byte) (map[string]any, error) {
	var request map[string]any
	if err := wirejson.DecodeUseNumber(body, &request); err != nil {
		return nil, fmt.Errorf("parse request: %w", err)
	}
	return request, nil
}

// NewOpenAIRequestView extracts first-occurrence request scalars from retained
// body bytes. Empty input yields an empty view, and gjson prefix extraction
// does not establish complete JSON validity.
//
// Seen bits prevent later duplicate root fields from replacing their earlier
// values, including an earlier null. Strings are trimmed and stream uses gjson
// boolean conversion; these reads supply execution evidence before full Decode.
// The view can retain strings backed by body storage, so callers leave that
// storage unchanged throughout its lifetime and rebuild a view after adopting
// patched bytes.
func NewOpenAIRequestView(body []byte) OpenAIRequestView {
	if len(body) == 0 {
		return OpenAIRequestView{}
	}
	// Each recognized root field gets one seen bit; recording its first occurrence
	// prevents later duplicate fields from changing scalar evidence used by preparation.
	const (
		modelField              uint8 = 1 << iota // model root field observed
		streamField                               // stream root field observed
		promptCacheKeyField                       // prompt-cache key root field observed
		previousResponseIDField                   // continuation ID root field observed
		serviceTierField                          // service-tier root field observed
		reasoningField                            // nested effort root field observed
		// allRequestViewFields is complete when every extracted root field has been seen.
		allRequestViewFields = modelField | streamField | promptCacheKeyField |
			previousResponseIDField | serviceTierField | reasoningField
	)

	view := OpenAIRequestView{body: body}
	var seen uint8
	// A prefix view supplies early request evidence while keeping first duplicate
	// values. Complete-document validation remains with Decode before object rewriting.
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

// Decode decodes the original retained body as one complete object through
// DecodeOpenAIRequestBody. Queued patches are excluded from this parse; callers
// apply and adopt them separately before rebuilding a view for changed bytes.
func (v OpenAIRequestView) Decode() (map[string]any, error) {
	return DecodeOpenAIRequestBody(v.Bytes())
}

// OpenAIRequestMetaFromBody returns lenient model, stream, and prompt-cache
// evidence from a byte view. Missing input supplies zero values, and these
// scalars do not establish complete JSON validity for request execution.
func OpenAIRequestMetaFromBody(body []byte) (model string, stream bool, promptCacheKey string) {
	view := NewOpenAIRequestView(body)
	return view.Model, view.Stream, view.PromptCacheKey
}

// MarkPatchSet queues a replacement for a trimmed simple path on a nonnil
// enabled view. Unsupported paths disable and clear all queued edits;
// replacement values are retained, so callers keep mutable values unchanged
// until application.
func (v *OpenAIRequestView) MarkPatchSet(path string, value any) {
	if v == nil || v.patchesDisabled {
		return
	}
	path = strings.TrimSpace(path)
	if !isSimpleOpenAIRequestPatchPath(path) {
		// This queue cannot safely encode an unsupported path alongside earlier edits.
		// Discard it and let the caller rebuild through the decoded-object path.
		v.DisablePatches()
		return
	}
	v.patches = append(v.patches, openAIRequestPatch{path: path, value: value})
}

// MarkPatchDelete queues a deletion for a trimmed simple path on a nonnil
// enabled view. Unsupported paths disable and clear the whole queue, and
// callers synchronize queue changes with subsequent reads or application.
func (v *OpenAIRequestView) MarkPatchDelete(path string) {
	if v == nil || v.patchesDisabled {
		return
	}
	path = strings.TrimSpace(path)
	if !isSimpleOpenAIRequestPatchPath(path) {
		// This queue cannot safely encode an unsupported path alongside earlier edits.
		// Discard it and let the caller rebuild through the decoded-object path.
		v.DisablePatches()
		return
	}
	v.patches = append(v.patches, openAIRequestPatch{path: path, delete: true})
}

// isSimpleOpenAIRequestPatchPath accepts nonempty dot-separated paths with
// nonblank components and no backslash. It defines this queue's supported shape
// rather than complete sjson path validation; callers switch to decoded
// rewriting when it returns false.
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

// DisablePatches marks a nonnil view for decoded rewriting and discards its
// queued patches. It retains original body and extracted scalars, and later
// MarkPatch calls leave the disabled queue untouched.
func (v *OpenAIRequestView) DisablePatches() {
	if v == nil {
		return
	}
	v.patchesDisabled = true
	v.patches = nil
}

// HasPatches reports whether an enabled view has queued request edits.
func (v OpenAIRequestView) HasPatches() bool {
	return !v.patchesDisabled && len(v.patches) > 0
}

// ApplyPatches applies queued edits in order and returns replacement bytes,
// leaving the view's body, scalars and queue unchanged. Disabled or empty
// queues return an error, and a failed sjson operation returns no replacement.
//
// The returned body starts from retained original storage; sequential
// set/delete calls resolve repeated paths in queue order. Callers adopt
// successful bytes and rebuild the view before reading updated scalars, and
// keep retained patch values stable during encoding. Applying the same view
// again replays the queue against its original body, so this operation is a
// computation rather than an in-place publication of new request state.
func (v OpenAIRequestView) ApplyPatches() ([]byte, error) {
	if v.patchesDisabled || len(v.patches) == 0 {
		return nil, errors.New("openai request patches disabled")
	}
	body := v.body
	// Apply each patch to the preceding bytes so ordered edits to one path take
	// effect together, while unrelated payload fields bypass full-object encoding.
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

// Bytes returns the original retained body slice used by this request view.
// Callers treat its backing storage as read-only and adopt ApplyPatches results
// separately. A no-op deletion can return the same storage, and patching does
// not update this view.
func (v OpenAIRequestView) Bytes() []byte { return v.body }

// PatchesDisabled reports whether a path or caller decision disabled byte
// patching. A true value tells preparation to use its decoded-object rewrite
// path; original bytes remain available for that decode.
func (v OpenAIRequestView) PatchesDisabled() bool { return v.patchesDisabled }
