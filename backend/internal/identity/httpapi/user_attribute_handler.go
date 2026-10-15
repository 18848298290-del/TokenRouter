package httpapi

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// user_attribute_handler.go handles administrator attribute definitions and
// per-account attribute value requests.
// Administrator attribute definitions describe input rules separately from the
// per-account string values they validate. HTTP updates preserve optional
// definition edits, translate ordered IDs to display positions, and format
// stored timestamps as RFC3339 text for the panel response.
//
// Batch reads normalize positive unique account IDs before encoding a snapshot
// key, so input order and duplicate IDs select the same cached result. The
// process-wide snapshot cache keeps those ID-keyed values for thirty seconds,
// and these mutation handlers do not invalidate it. Successful writes can
// therefore precede a later batch response using an unexpired earlier
// snapshot; ordinary account value reads still call the attribute service.

// UserAttributesBatchCache retains process-wide account attribute snapshots
// for thirty seconds, keyed by normalized user IDs during administrator batch
// reads.
var UserAttributesBatchCache = response.NewSnapshotCache(30 * time.Second)

// UserAttributeHandler serves administrator attribute definitions and current
// per-account string values.
type UserAttributeHandler struct {
	attrService *identity.UserAttributeService // attribute definition and value service
}

// CreateAttributeDefinitionRequest carries the input rules for a new
// administrator-defined attribute.
type CreateAttributeDefinitionRequest struct {
	Key         string                           `json:"key" binding:"required,min=1,max=100"`  // unique definition key text
	Name        string                           `json:"name" binding:"required,min=1,max=255"` // definition display name text
	Description string                           `json:"description"`                           // definition descriptive help text
	Type        string                           `json:"type" binding:"required"`               // attribute input kind label
	Options     []identity.UserAttributeOption   `json:"options"`                               // selectable attribute input choices
	Required    bool                             `json:"required"`                              // editing required-input flag
	Validation  identity.UserAttributeValidation `json:"validation"`                            // attribute value validation limits
	Placeholder string                           `json:"placeholder"`                           // definition input placeholder text
	Enabled     bool                             `json:"enabled"`                               // definition display enabled flag
}

// UpdateAttributeDefinitionRequest carries optional edits to an existing
// attribute definition.
type UpdateAttributeDefinitionRequest struct {
	// Name, Description, Type, Options, Required, Validation, Placeholder, and
	// Enabled use pointers to retain supplied zero and empty edits. Nil values
	// leave the existing definition fields to service update handling.
	Name        *string                           `json:"name"`        // definition display name text
	Description *string                           `json:"description"` // definition descriptive help text
	Type        *string                           `json:"type"`        // attribute input kind label
	Options     *[]identity.UserAttributeOption   `json:"options"`     // selectable attribute input choices
	Required    *bool                             `json:"required"`    // editing required-input flag
	Validation  *identity.UserAttributeValidation `json:"validation"`  // attribute value validation limits
	Placeholder *string                           `json:"placeholder"` // definition input placeholder text
	Enabled     *bool                             `json:"enabled"`     // definition display enabled flag
}

// ReorderRequest lists definition IDs whose array positions become display
// order values.
type ReorderRequest struct {
	IDs []int64 `json:"ids" binding:"required"` // definition IDs in requested order
}

// UpdateUserAttributesRequest supplies attribute IDs and replacement strings
// for one account.
type UpdateUserAttributesRequest struct {
	Values map[int64]string `json:"values" binding:"required"` // definition ID replacement strings
}

// BatchGetUserAttributesRequest names the account IDs requested for a cached
// attribute snapshot.
type BatchGetUserAttributesRequest struct {
	UserIDs []int64 `json:"user_ids" binding:"required"` // requested account row identifiers
}

// BatchUserAttributesResponse maps account IDs to their stored definition-ID
// value maps.
type BatchUserAttributesResponse struct {
	Attributes map[int64]map[int64]string `json:"attributes"` // account ID definition value maps
}

// AttributeDefinitionResponse exposes input rules and ordered display metadata
// for administrator clients.
type AttributeDefinitionResponse struct {
	ID          int64  `json:"id"`          // stored attribute row identifier
	Key         string `json:"key"`         // unique definition key text
	Name        string `json:"name"`        // definition display name text
	Description string `json:"description"` // definition descriptive help text
	Type        string `json:"type"`        // attribute input kind label
	// Options shares the supplied definition's entries. Copy the slice before
	// changing selectable values or labels in an independent response.
	Options  []identity.UserAttributeOption `json:"options"`  // selectable attribute input choices
	Required bool                           `json:"required"` // editing required-input flag
	// Validation copies the rule struct and retains its optional pointers. Copy
	// those values before changing bounds or pattern text in an independent response.
	Validation   identity.UserAttributeValidation `json:"validation"`    // attribute value validation limits
	Placeholder  string                           `json:"placeholder"`   // definition input placeholder text
	DisplayOrder int                              `json:"display_order"` // definition display order position
	Enabled      bool                             `json:"enabled"`       // definition display enabled flag
	CreatedAt    string                           `json:"created_at"`    // RFC3339
	UpdatedAt    string                           `json:"updated_at"`    // RFC3339
}

// AttributeValueResponse exposes one stored account attribute value with
// formatted row timestamps.
type AttributeValueResponse struct {
	ID          int64  `json:"id"`           // stored attribute row identifier
	UserID      int64  `json:"user_id"`      // owning account row identifier
	AttributeID int64  `json:"attribute_id"` // owning definition row identifier
	Value       string `json:"value"`        // stored account attribute string
	CreatedAt   string `json:"created_at"`   // RFC3339
	UpdatedAt   string `json:"updated_at"`   // RFC3339
}

// NewUserAttributeHandler retains the supplied attribute service for
// administrator definition and value requests.
func NewUserAttributeHandler(attrService *identity.UserAttributeService) *UserAttributeHandler {
	return &UserAttributeHandler{attrService: attrService}
}

// DefToResponse maps a nonnil definition to its administrator JSON shape and
// formats timestamps as RFC3339. Options and any validation pointers retain
// sharing with the supplied domain value.
func DefToResponse(def *identity.UserAttributeDefinition) *AttributeDefinitionResponse {
	return &AttributeDefinitionResponse{
		ID:           def.ID,
		Key:          def.Key,
		Name:         def.Name,
		Description:  def.Description,
		Type:         string(def.Type),
		Options:      def.Options,
		Required:     def.Required,
		Validation:   def.Validation,
		Placeholder:  def.Placeholder,
		DisplayOrder: def.DisplayOrder,
		Enabled:      def.Enabled,
		CreatedAt:    def.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:    def.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// ValueToResponse maps a nonnil stored attribute value to administrator JSON
// fields. Created and updated timestamps become RFC3339 text, while
// identifiers and the saved value string copy directly.
func ValueToResponse(val *identity.UserAttributeValue) *AttributeValueResponse {
	return &AttributeValueResponse{
		ID:          val.ID,
		UserID:      val.UserID,
		AttributeID: val.AttributeID,
		Value:       val.Value,
		CreatedAt:   val.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:   val.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// ListDefinitions writes administrator definition data, optionally selecting
// enabled entries. Only exact enabled=true activates that filter; service
// errors stop conversion, and empty results use an allocated response slice.
func (h *UserAttributeHandler) ListDefinitions(c *gin.Context) {
	enabledOnly := c.Query("enabled") == "true"

	defs, err := h.attrService.ListDefinitions(c.Request.Context(), enabledOnly)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]*AttributeDefinitionResponse, 0, len(defs))
	for i := range defs {
		out = append(out, DefToResponse(&defs[i]))
	}

	response.Success(c, out)
}

// CreateDefinition validates request JSON before passing prepared definition
// rules to the attribute service. The service validates supported input kinds
// and rules; successful storage returns the administrator response shape.
func (h *UserAttributeHandler) CreateDefinition(c *gin.Context) {
	var req CreateAttributeDefinitionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	def, err := h.attrService.CreateDefinition(c.Request.Context(), identity.CreateAttributeDefinitionInput{
		Key:         req.Key,
		Name:        req.Name,
		Description: req.Description,
		Type:        identity.UserAttributeType(req.Type),
		Options:     req.Options,
		Required:    req.Required,
		Validation:  req.Validation,
		Placeholder: req.Placeholder,
		Enabled:     req.Enabled,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, DefToResponse(def))
}

// UpdateDefinition parses the route ID and passes optional rule edits to the
// service. A nonnil type string is converted to the domain kind; omitted
// pointer fields remain nil in its update input.
func (h *UserAttributeHandler) UpdateDefinition(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid attribute ID")
		return
	}

	var req UpdateAttributeDefinitionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	input := identity.UpdateAttributeDefinitionInput{
		Name:        req.Name,
		Description: req.Description,
		Options:     req.Options,
		Required:    req.Required,
		Validation:  req.Validation,
		Placeholder: req.Placeholder,
		Enabled:     req.Enabled,
	}
	if req.Type != nil {
		t := identity.UserAttributeType(*req.Type)
		input.Type = &t
	}

	def, err := h.attrService.UpdateDefinition(c.Request.Context(), id, input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, DefToResponse(def))
}

// DeleteDefinition parses the route ID and requests definition removal through
// the attribute service. Invalid IDs stop before service work, while
// successful deletion writes the panel completion message.
func (h *UserAttributeHandler) DeleteDefinition(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid attribute ID")
		return
	}

	if err := h.attrService.DeleteDefinition(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "Attribute definition deleted successfully"})
}

// ReorderDefinitions maps supplied definition IDs to their zero-based array
// positions before service persistence. Repeated IDs select their final
// position in the map, and malformed JSON stops before the write.
func (h *UserAttributeHandler) ReorderDefinitions(c *gin.Context) {
	var req ReorderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	// Display order uses the request position for each ID. A repeated definition
	// ID overwrites its earlier map entry, so the last occurrence determines the
	// stored position.
	orders := make(map[int64]int, len(req.IDs))
	for i, id := range req.IDs {
		orders[id] = i
	}

	if err := h.attrService.ReorderDefinitions(c.Request.Context(), orders); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "Reorder successful"})
}

// GetUserAttributes returns stored values for the parsed administrator account
// route ID. Service errors stop the response, while successful results convert
// each row with RFC3339 timestamps and an allocated outer slice.
func (h *UserAttributeHandler) GetUserAttributes(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid user ID")
		return
	}

	values, err := h.attrService.GetUserAttributes(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]*AttributeValueResponse, 0, len(values))
	for i := range values {
		out = append(out, ValueToResponse(&values[i]))
	}

	response.Success(c, out)
}

// UpdateUserAttributes submits definition-ID replacement strings for the
// parsed account ID, then reads stored values for its response. A later read
// error can follow a successful update; request map iteration supplies no
// input ordering.
func (h *UserAttributeHandler) UpdateUserAttributes(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid user ID")
		return
	}

	var req UpdateUserAttributesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	inputs := make([]identity.UpdateUserAttributeInput, 0, len(req.Values))
	for attrID, value := range req.Values {
		inputs = append(inputs, identity.UpdateUserAttributeInput{
			AttributeID: attrID,
			Value:       value,
		})
	}

	if err := h.attrService.UpdateUserAttributes(c.Request.Context(), userID, inputs); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	values, err := h.attrService.GetUserAttributes(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]*AttributeValueResponse, 0, len(values))
	for i := range values {
		out = append(out, ValueToResponse(&values[i]))
	}

	response.Success(c, out)
}

// GetBatchUserAttributes returns values for normalized positive unique account
// IDs using a thirty-second process snapshot. Invalid-only selections return
// an empty map; cache hits write the stored payload, while misses call the
// service and save a snapshot.
//
// The cache key encodes sorted IDs, so reordered or duplicate input shares a
// result. Attribute mutations in this handler leave existing snapshots until
// expiry. Concurrent misses can load separately because this path uses
// separate Get and Set calls rather than the cache load-coordination API.
func (h *UserAttributeHandler) GetBatchUserAttributes(c *gin.Context) {
	var req BatchGetUserAttributesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	userIDs := response.NormalizeInt64IDList(req.UserIDs)
	if len(userIDs) == 0 {
		response.Success(c, BatchUserAttributesResponse{Attributes: map[int64]map[int64]string{}})
		return
	}

	// Encoding normalized sorted IDs makes equivalent account selections share
	// one key. This key contains only the supported integer slice, so the ignored
	// marshal error has no alternate payload path.
	keyRaw, _ := json.Marshal(struct {
		UserIDs []int64 `json:"user_ids"` // requested account row identifiers
	}{
		UserIDs: userIDs,
	})
	cacheKey := string(keyRaw)
	if cached, ok := UserAttributesBatchCache.Get(cacheKey); ok {
		c.Header("X-Snapshot-Cache", "hit")
		response.Success(c, cached.Payload)
		return
	}

	attrs, err := h.attrService.GetBatchUserAttributes(c.Request.Context(), userIDs)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	payload := BatchUserAttributesResponse{Attributes: attrs}
	// Save the service result after a successful batch read. This snapshot
	// remains keyed to the account selection until expiry even when a later
	// mutation changes attribute values.
	UserAttributesBatchCache.Set(cacheKey, payload)
	c.Header("X-Snapshot-Cache", "miss")
	response.Success(c, payload)
}
