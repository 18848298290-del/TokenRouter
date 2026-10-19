package httpapi

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// models_gemini.go serves Gemini model lists and individual resources with
// group eligibility and API-key alias metadata.
// Gemini model resources describe configured candidates that support the
// Gemini generation protocol. Group protocol permission and custom display
// lists constrain these names before concrete Key aliases are added, so
// catalogue metadata cannot independently make a model available. Composite
// bindings attach their saved prefix to the public resource name.
//
// Single-resource lookup validates the URL segment and selects one exact
// configured composite prefix before checking only the requested name and
// possible exact alias target. List and resource output restore public names
// while taking capability metadata from an eligible target. JSON alias
// augmentation copies the target object's fields before replacing its name
// and displayName, keeping unrelated envelope and model metadata available to
// clients.

// @project-doc docs/domains/api_key_model_redirects.md#model_list_projection
// GeminiV1BetaListModels writes locally requestable Gemini model resources
// for the authenticated Key. It uses Google-shaped errors for missing
// authentication or denied ordinary-group protocol permission. Catalogue read
// failures return unavailable; uncovered composite subscriptions yield an empty list.
func (h *ModelsHandler) GeminiV1BetaListModels(c *gin.Context) {
	done, accepted := h.BeginRequest(c, "google")
	if !accepted {
		return
	}
	defer done()
	key, ok := h.backend.Access(c)
	if !ok || key == nil {
		WriteGoogleError(c, http.StatusUnauthorized, "Invalid API key")
		return
	}
	models, allowed := h.requestableGeminiModels(c, key)
	if !allowed {
		return
	}
	c.JSON(http.StatusOK, GeminiModelsList{Models: models})
}

// GeminiV1BetaGetModel writes one eligible Gemini model resource under the
// requested public name. It rejects blank, wildcard or unsafe URL segments
// before model resolution. Composite lookup matches the saved prefix exactly
// and then applies user, subscription and Gemini protocol permission for that
// one group.
//
// Only the requested name and its configured exact alias target reach
// ResolveSelected. Results are filtered by Gemini capability and the group's
// custom list. When the public name is unavailable as a concrete model but
// its alias target qualifies, metadata comes from that target while name and
// displayName identify the requested alias or prefixed resource. Unresolved
// resources return Google not found.
func (h *ModelsHandler) GeminiV1BetaGetModel(c *gin.Context) {
	done, accepted := h.BeginRequest(c, "google")
	if !accepted {
		return
	}
	defer done()
	key, ok := h.backend.Access(c)
	if !ok || key == nil {
		WriteGoogleError(c, http.StatusUnauthorized, "Invalid API key")
		return
	}
	name := strings.TrimPrefix(strings.TrimSpace(c.Param("model")), "/")
	segment := name
	if key.IsComposite {
		_, rest, found := strings.Cut(name, "/")
		if found {
			segment = rest
		}
	}
	if segment == "" || strings.ContainsAny(segment, "*?") || !h.backend.SafeModelSegment(segment) {
		WriteGoogleError(c, http.StatusBadRequest, "Invalid model in URL")
		return
	}
	group := key.Group
	if key.IsComposite {
		prefix, rest, found := strings.Cut(name, "/")
		group = nil
		if found {
			preferred, ready := h.CompositePreferredSubscription(c, key)
			if ready {
				for _, binding := range key.CompositeGroups {
					if binding.Prefix == prefix && CompositeGroupAvailableToUser(key, preferred, binding.Group) {
						group = binding.Group
						segment = rest
						break
					}
				}
			}
		}
	} else if group == nil || !group.AllowsClientProtocol(protocol.ProtocolGeminiGenerateContent) {
		WriteGoogleError(c, http.StatusForbidden, "This group does not allow Gemini GenerateContent requests")
		return
	}
	if group != nil && group.AllowsClientProtocol(protocol.ProtocolGeminiGenerateContent) && h.backend.Available() {
		forced, _ := h.backend.ForcedPlatform(c)
		forced = strings.TrimSpace(forced)
		// Limit resolution to this resource and its exact alias target. Full catalogue
		// enumeration would add unrelated candidate work to every single-model lookup.
		selected := []string{segment}
		target, alias := key.ModelMapping[segment]
		if alias {
			selected = append(selected, target)
		}
		resolved := h.backend.ResolveSelected(c.Request.Context(), &group.ID, forced, selected)
		if resolved.Err != nil {
			WriteGoogleError(c, http.StatusServiceUnavailable, resolved.Err.Error())
			return
		}
		visible := make(map[string]bool, len(resolved.Models))
		for _, model := range resolved.Models {
			if !slices.Contains(model.Protocols, protocol.ProtocolGeminiGenerateContent) {
				continue
			}
			if customListEnabled(group) && len(FilterModelsByCustomList([]string{model.ID}, nil, group.ModelsListConfig.Models)) == 0 {
				continue
			}
			visible[model.ID] = true
		}
		metadataID := segment
		// An unavailable public alias can borrow its eligible target's capability
		// metadata, then restore the public name without changing the target resolution.
		if !visible[segment] && alias && visible[target] {
			metadataID = target
		}
		if visible[metadataID] {
			model := h.catalog.GeminiModel(metadataID, forced == capability.PlatformAntigravity)
			model.Name = "models/" + name
			if name != metadataID {
				model.DisplayName = name
			}
			c.JSON(http.StatusOK, model)
			return
		}
	}
	WriteGoogleError(c, http.StatusNotFound, "Model is not available in this group")
}

// requestableGeminiModels builds the authenticated Key's ordered Gemini
// resource list and reports whether presentation may continue. Denied
// ordinary-group protocol permission or failed catalogue loading writes a Google
// error and returns false.
// Missing catalogue support returns an allocated empty list and true;
// uncovered composite subscription also leaves that list empty.
//
// Each eligible group contributes concrete Gemini-capable IDs intersected
// with its custom list, then available exact Key aliases. A prefix is
// attached after target eligibility, and duplicate public IDs retain their
// first occurrence. Alias resources borrow target metadata and display their
// public identity, so capability fields remain tied to an actually eligible
// model.
func (h *ModelsHandler) requestableGeminiModels(c *gin.Context, key *apikey.APIKey) ([]GeminiModel, bool) {
	models := make([]GeminiModel, 0)
	if !key.IsComposite && (key.Group == nil || !key.Group.AllowsClientProtocol(protocol.ProtocolGeminiGenerateContent)) {
		WriteGoogleError(c, http.StatusForbidden, "This group does not allow Gemini GenerateContent requests")
		return nil, false
	}
	if !h.backend.Available() {
		return models, true
	}
	forced, _ := h.backend.ForcedPlatform(c)
	forced = strings.TrimSpace(forced)
	seen := make(map[string]bool)
	appendGroup := func(group *routing.Group, prefix string) error {
		if group == nil || !group.AllowsClientProtocol(protocol.ProtocolGeminiGenerateContent) {
			return nil
		}
		id := group.ID
		resolved := h.backend.Resolve(c.Request.Context(), &id, forced)
		if resolved.Err != nil {
			return resolved.Err
		}
		available := make([]string, 0, len(resolved.Models))
		for _, model := range resolved.Models {
			if slices.Contains(model.Protocols, protocol.ProtocolGeminiGenerateContent) && !strings.ContainsAny(model.ID, "*?") {
				available = append(available, model.ID)
			}
		}
		if customListEnabled(group) {
			available = FilterModelsByCustomList(available, nil, group.ModelsListConfig.Models)
		}
		aliases := apikey.AvailableAPIKeyModelAliases(available, key.ModelMapping)
		// Append aliases after group filtering, then prefix the public identity. This
		// keeps copied capability metadata tied to an eligible target in that binding.
		for _, modelID := range append(available, aliases...) {
			publicID := modelID
			if prefix != "" {
				publicID = prefix + "/" + publicID
			}
			if seen[publicID] {
				continue
			}
			seen[publicID] = true
			metadataID := modelID
			if slices.Contains(aliases, modelID) {
				metadataID = key.ModelMapping[modelID]
			}
			model := h.catalog.GeminiModel(metadataID, forced == capability.PlatformAntigravity)
			model.Name = "models/" + publicID
			if publicID != metadataID {
				model.DisplayName = publicID
			}
			models = append(models, model)
		}
		return nil
	}
	if key.IsComposite {
		subscription, ready := h.CompositePreferredSubscription(c, key)
		if !ready {
			return models, true
		}
		for _, binding := range key.CompositeGroups {
			if CompositeGroupAvailableToUser(key, subscription, binding.Group) {
				if err := appendGroup(binding.Group, binding.Prefix); err != nil {
					WriteGoogleError(c, http.StatusServiceUnavailable, err.Error())
					return nil, false
				}
			}
		}
	} else {
		if err := appendGroup(key.Group, ""); err != nil {
			WriteGoogleError(c, http.StatusServiceUnavailable, err.Error())
			return nil, false
		}
	}
	return models, true
}

// AppendAPIKeyAliasesToGeminiModelsJSON appends available exact Key aliases
// to a Gemini JSON model list. Empty input or mapping, unsupported JSON
// shape, or encoding failure returns the supplied body. Existing model order
// and envelope fields remain represented when encoding succeeds.
//
// The first valid resource object for each target supplies alias metadata.
// Its field map is copied, then name and displayName are replaced with the
// alias; remaining RawMessage values are reused until serialization. Alias
// enumeration depends on the initial target set, so newly appended aliases do
// not make another mapping eligible. Wildcard sources and targets absent from
// that initial set produce no added resource.
func (h *ModelsHandler) AppendAPIKeyAliasesToGeminiModelsJSON(body []byte, mapping map[string]string) []byte {
	if len(body) == 0 || len(mapping) == 0 {
		return body
	}
	var payload map[string]json.RawMessage
	// Unsupported or malformed list shapes retain the upstream bytes, allowing
	// callers to present an unaugmented response rather than a reconstructed error.
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	var models []map[string]json.RawMessage
	if err := json.Unmarshal(payload["models"], &models); err != nil {
		return body
	}

	modelIDs := make([]string, 0, len(models))
	templates := make(map[string]map[string]json.RawMessage, len(models))
	// Keep undecodable or blank names in the existing array but exclude them from
	// the alias index, since an added resource needs an identifiable target.
	for _, model := range models {
		var name string
		if err := json.Unmarshal(model["name"], &name); err != nil {
			continue
		}
		modelID := strings.TrimPrefix(strings.TrimSpace(name), "models/")
		if modelID == "" {
			continue
		}
		modelIDs = append(modelIDs, modelID)
		if _, exists := templates[modelID]; !exists {
			templates[modelID] = model
		}
	}

	for _, alias := range apikey.AvailableAPIKeyModelAliases(modelIDs, mapping) {
		template, exists := templates[mapping[alias]]
		if !exists {
			continue
		}
		// Copy the target field map before changing alias identity so the existing
		// resource keeps its name and every additional upstream metadata field survives.
		cloned := make(map[string]json.RawMessage, len(template))
		maps.Copy(cloned, template)
		cloned["name"], _ = json.Marshal("models/" + alias)
		cloned["displayName"], _ = json.Marshal(alias)
		models = append(models, cloned)
	}

	// Publish a complete encoded envelope after both serialization steps succeed.
	// Any encoding failure retains the supplied body instead of a partial augmentation.
	encodedModels, err := json.Marshal(models)
	if err != nil {
		return body
	}
	payload["models"] = encodedModels
	updated, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return updated
}
