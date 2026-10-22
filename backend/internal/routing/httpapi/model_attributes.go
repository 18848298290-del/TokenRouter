package httpapi

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// model_attributes.go exposes administrative attribute profiles, catalogue
// queries, and route previews over HTTP.
// Model attribute profiles use the HTTP route to choose the record being
// saved. A create request resets the decoded body ID to zero, and an update
// replaces it with the positive route ID before service validation and
// persistence, so a submitted body cannot redirect the write to another
// profile.
//
// Default attribute queries read published catalog data and status together
// through an optional index or a snapshot callback. Search combines
// model/display text, provider identity, and true capability declarations
// before pagination. Group previews use saved or supplied draft rules against
// requestable routes; manual catalog refresh is a separate callback, keeping
// ordinary reads on the current published catalog.

// ModelAttributeHandler serves profile administration, route previews, and
// default attribute queries.
type ModelAttributeHandler struct {
	service   *routing.ModelAttributeService // attribute profile and catalog service
	catalogue *routing.RequestableCatalogue  // configured preview route reader
}

// NewModelAttributeHandler binds the attribute profile service to its HTTP
// endpoints. Callers supply a nonnil service and configure the repository and
// catalog callbacks invoked by the routes they serve.
func NewModelAttributeHandler(service *routing.ModelAttributeService, catalogue *routing.RequestableCatalogue) *ModelAttributeHandler {
	return &ModelAttributeHandler{service: service, catalogue: catalogue}
}

// RegisterModelAttributeRoutes binds profile CRUD, catalog queries, manual
// refresh, and route preview under model-attributes. The caller supplies
// administrator middleware on admin because registration adds these handlers
// directly to that router group.
func RegisterModelAttributeRoutes(admin *gin.RouterGroup, h *ModelAttributeHandler) {
	routes := admin.Group("/model-attributes")
	adminapi.Register(routes, ManagementOperations["model_attributes.configs.list"], h.List)
	adminapi.Register(routes, ManagementOperations["model_attributes.configs.get"], h.Get)
	adminapi.Register(routes, ManagementOperations["model_attributes.configs.create"], h.Save)
	adminapi.Register(routes, ManagementOperations["model_attributes.configs.update"], h.Save)
	adminapi.Register(routes, ManagementOperations["model_attributes.configs.delete"], h.Delete)
	adminapi.Register(routes, ManagementOperations["model_attributes.defaults.list"], h.Defaults)
	adminapi.Register(routes, ManagementOperations["model_attributes.defaults.model.list"], h.Model)
	adminapi.Register(routes, ManagementOperations["model_attributes.defaults.models.list"], h.Models)
	adminapi.Register(routes, ManagementOperations["model_attributes.defaults.update.create"], h.Update)
	adminapi.Register(routes, ManagementOperations["model_attributes.preview.create"], h.Preview)
}

// Preview returns effective attributes for the requested group's matching
// model routes. It requires strict JSON, a positive group ID, and nonblank
// model text, and validates supplied draft attributes and route conditions
// before delegating. Omitted drafts use the saved group profile; an empty
// client protocol includes matching routes across entries. A successful
// preview with no matching route returns an empty routes list.
func (h *ModelAttributeHandler) Preview(c *gin.Context) {
	var input ModelAttributePreviewRequest
	if err := httpx.BindJSONStrict(c, &input); err != nil || input.GroupID <= 0 || strings.TrimSpace(input.Model) == "" {
		httpx.ErrorFrom(c, apperror.BadRequest("INVALID_ATTRIBUTE_PREVIEW", "group ID and model are required"))
		return
	}
	if input.Config != nil {
		for _, rule := range input.Config.Rules {
			if err := rule.Attributes.Validate(); err != nil {
				httpx.ErrorFrom(c, apperror.BadRequest("INVALID_MODEL_ATTRIBUTES", err.Error()))
				return
			}
			if err := rule.Match.Validate(); err != nil {
				httpx.ErrorFrom(c, apperror.BadRequest("INVALID_ATTRIBUTE_MATCH", err.Error()))
				return
			}
		}
	}
	targets, err := h.catalogue.AttributeTargets(c.Request.Context(), input.GroupID, input.Model, input.ClientProtocol)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	result, err := h.service.Preview(c.Request.Context(), input.GroupID, targets, input.Config)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	httpx.Success(c, gin.H{"routes": result})
}

// attributeID parses a positive configuration ID from the route and reports
// whether parsing succeeded. An invalid or nonpositive value writes
// INVALID_ATTRIBUTE_CONFIG_ID to the response, so callers return when the
// boolean is false.
func attributeID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.ErrorFrom(c, apperror.BadRequest("INVALID_ATTRIBUTE_CONFIG_ID", "invalid configuration ID"))
		return 0, false
	}
	return id, true
}

// List returns stored attribute profiles filtered by exact status and
// case-insensitive name/description search. It applies standard pagination
// after filtering and retains the repository's result order; the returned
// total counts all matching profiles before pagination.
func (h *ModelAttributeHandler) List(c *gin.Context) {
	rows, err := h.service.Repo.List(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	search, status := strings.ToLower(strings.TrimSpace(c.Query("search"))), c.Query("status")
	filtered := []routing.ModelAttributeConfig{}
	for _, row := range rows {
		if (status == "" || row.Status == status) && (search == "" || strings.Contains(strings.ToLower(row.Name+" "+row.Description), search)) {
			filtered = append(filtered, row)
		}
	}
	page, size := httpx.ParsePagination(c)
	start := attributePageStart(page, size, len(filtered))
	end := min(start+size, len(filtered))
	httpx.Paginated(c, filtered[start:end], int64(len(filtered)), page, size)
}

// Get returns the stored profile named by the route's positive configuration
// ID. It stops after attributeID writes a validation error and otherwise
// forwards repository errors through the standard HTTP response.
func (h *ModelAttributeHandler) Get(c *gin.Context) {
	id, ok := attributeID(c)
	if !ok {
		return
	}
	value, err := h.service.Repo.Get(c.Request.Context(), id)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	httpx.Success(c, value)
}

// Save strictly decodes an attribute profile and persists it through the
// service. Creation resets the submitted ID to zero; update replaces that
// value with the route's validated positive ID, making the route determine
// which stored record is changed. The service validates rules, associates
// groups, and prepares the saved value before the handler returns it. A
// binding, route-ID, or service failure returns its corresponding HTTP error.
func (h *ModelAttributeHandler) Save(c *gin.Context) {
	var value routing.ModelAttributeConfig
	if err := httpx.BindJSONStrict(c, &value); err != nil {
		httpx.ErrorFrom(c, apperror.BadRequest("INVALID_ATTRIBUTE_CONFIG", err.Error()))
		return
	}
	// Use zero for creation and the validated route ID for updates so a body ID
	// cannot select another persisted profile.
	value.ID = 0
	if c.Param("id") != "" {
		id, ok := attributeID(c)
		if !ok {
			return
		}
		value.ID = id
	}
	if err := h.service.Save(c.Request.Context(), &value); err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	httpx.Success(c, value)
}

// Delete removes the profile selected by a positive route ID through the
// attribute service. It stops after route validation or service errors, and
// reports deleted=true after successful removal and service-owned cache
// invalidation.
func (h *ModelAttributeHandler) Delete(c *gin.Context) {
	id, ok := attributeID(c)
	if !ok {
		return
	}
	if err := h.service.Delete(c.Request.Context(), id); err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	httpx.Success(c, gin.H{"deleted": true})
}

// Defaults returns published catalog attributes filtered by search, provider,
// and capability. It uses the prepared index when available and otherwise
// filters one snapshot, accepting capability fields declared true. The
// response includes the matching total, the complete provider choices, and
// status from that publication.
func (h *ModelAttributeHandler) Defaults(c *gin.Context) {
	if h.service.Catalog.Index != nil {
		index, status := h.service.Catalog.Index()
		page, size := httpx.ParsePagination(c)
		total := index.Len()
		result := index.Search(modelcatalog.AttributeQuery{Search: c.Query("search"), Provider: c.Query("provider"), Capability: c.Query("capability"), Offset: attributePageStart(page, size, total), Limit: size})
		httpx.Success(c, gin.H{"items": result.Items, "total": result.Total, "providers": result.Providers, "version": status.Version, "last_updated": status.LastUpdated, "last_error": status.LastError})
		return
	}
	// When an index is unavailable, read one snapshot so filtered rows and
	// returned catalog status describe the same publication.
	snapshot := h.service.Catalog.Snapshot()
	rows := []modelcatalog.Entry{}
	providers := map[string]bool{}
	search, provider, capability := strings.ToLower(strings.TrimSpace(c.Query("search"))), c.Query("provider"), c.Query("capability")
	for _, row := range snapshot.Items {
		providers[row.Provider] = true
		name := row.Model
		if row.Attributes.DisplayName != nil {
			name += " " + *row.Attributes.DisplayName
		}
		if search != "" && !strings.Contains(strings.ToLower(name), search) || provider != "" && row.Provider != provider {
			continue
		}
		if capability != "" {
			// Read capability names through the serialized attribute fields; missing,
			// false, or unextractable values do not qualify for this true-capability
			// filter.
			body, _ := json.Marshal(row.Attributes)
			var attrs map[string]any
			_ = json.Unmarshal(body, &attrs)
			if attrs[capability] != true {
				continue
			}
		}
		rows = append(rows, row)
	}
	providerNames := []string{}
	for name := range providers {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)
	page, size := httpx.ParsePagination(c)
	start := attributePageStart(page, size, len(rows))
	end := min(start+size, len(rows))
	httpx.Success(c, gin.H{"items": rows[start:end], "total": len(rows), "providers": providerNames, "version": snapshot.Version, "last_updated": snapshot.LastUpdated, "last_error": snapshot.LastError})
}

// Model returns default attributes for a trimmed model query through the
// catalog lookup callback. Empty model text returns MODEL_REQUIRED; unknown
// attributes keep the lookup callback's result and are returned through the
// success envelope.
func (h *ModelAttributeHandler) Model(c *gin.Context) {
	model := strings.TrimSpace(c.Query("model"))
	if model == "" {
		httpx.ErrorFrom(c, apperror.BadRequest("MODEL_REQUIRED", "model is required"))
		return
	}
	httpx.Success(c, h.service.Catalog.Lookup(model))
}

// Models returns catalog model identities using the prepared index when
// available. The snapshot path collects IDs in snapshot order; both paths
// read already published data and leave catalog refresh to the Update
// endpoint.
func (h *ModelAttributeHandler) Models(c *gin.Context) {
	if h.service.Catalog.Index != nil {
		index, _ := h.service.Catalog.Index()
		httpx.Success(c, index.ModelIDs())
		return
	}
	// The snapshot callback supplies names for adapters without an Index
	// callback, allowing their model selectors to use the published directory.
	names := []string{}
	for _, row := range h.service.Catalog.Snapshot().Items {
		names = append(names, row.Model)
	}
	httpx.Success(c, names)
}

// Update invokes the configured catalog refresh callback for an administrator
// request. It returns updated=true on success and a generic HTTP 502
// catalog-update error when the callback reports failure.
func (h *ModelAttributeHandler) Update(c *gin.Context) {
	if err := h.service.Catalog.Update(); err != nil {
		httpx.Error(c, 502, "failed to update model catalog")
		return
	}
	httpx.Success(c, gin.H{"updated": true})
}

// attributePageStart returns a page offset bounded by the available row
// count. Callers supply page >= 1, size > 0, and total >= 0, as the HTTP
// pagination paths do; beyond-end pages return total before offset
// multiplication.
func attributePageStart(page, size, total int) int {
	// Check available pages before multiplying to bound the slice offset.
	if page-1 > total/size {
		return total
	}
	return (page - 1) * size
}
