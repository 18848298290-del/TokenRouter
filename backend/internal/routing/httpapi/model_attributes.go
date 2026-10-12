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
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// ModelAttributeHandler 提供属性档案和默认目录查询，不绑定生成请求入口。
type ModelAttributeHandler struct {
	service *routing.ModelAttributeService
}

func NewModelAttributeHandler(service *routing.ModelAttributeService) *ModelAttributeHandler {
	return &ModelAttributeHandler{service: service}
}

func RegisterModelAttributeRoutes(admin *gin.RouterGroup, h *ModelAttributeHandler) {
	routes := admin.Group("/model-attributes")
	routes.GET("/configs", h.List)
	routes.GET("/configs/:id", h.Get)
	routes.POST("/configs", h.Save)
	routes.PUT("/configs/:id", h.Save)
	routes.DELETE("/configs/:id", h.Delete)
	routes.GET("/defaults", h.Defaults)
	routes.GET("/defaults/model", h.Model)
	routes.GET("/defaults/models", h.Models)
	routes.POST("/defaults/update", h.Update)
	routes.POST("/preview", h.Preview)
}

func (h *ModelAttributeHandler) Preview(c *gin.Context) {
	var input struct {
		GroupID        int64                         `json:"group_id"`
		Model          string                        `json:"model"`
		ClientProtocol string                        `json:"client_protocol"`
		Config         *routing.ModelAttributeConfig `json:"config,omitempty"`
	}
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
	result, err := h.service.Preview(c.Request.Context(), input.GroupID, input.Model, input.ClientProtocol, input.Config)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	httpx.Success(c, gin.H{"routes": result})
}

func attributeID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.ErrorFrom(c, apperror.BadRequest("INVALID_ATTRIBUTE_CONFIG_ID", "invalid configuration ID"))
		return 0, false
	}
	return id, true
}

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

func (h *ModelAttributeHandler) Save(c *gin.Context) {
	var value routing.ModelAttributeConfig
	if err := httpx.BindJSONStrict(c, &value); err != nil {
		httpx.ErrorFrom(c, apperror.BadRequest("INVALID_ATTRIBUTE_CONFIG", err.Error()))
		return
	}
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

func (h *ModelAttributeHandler) Defaults(c *gin.Context) {
	if h.service.Catalog.Index != nil {
		index, status := h.service.Catalog.Index()
		page, size := httpx.ParsePagination(c)
		total := index.Len()
		result := index.Search(modelcatalog.AttributeQuery{Search: c.Query("search"), Provider: c.Query("provider"), Capability: c.Query("capability"), Offset: attributePageStart(page, size, total), Limit: size})
		httpx.Success(c, gin.H{"items": result.Items, "total": result.Total, "providers": result.Providers, "version": status.Version, "last_updated": status.LastUpdated, "last_error": status.LastError})
		return
	}
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

func (h *ModelAttributeHandler) Model(c *gin.Context) {
	model := strings.TrimSpace(c.Query("model"))
	if model == "" {
		httpx.ErrorFrom(c, apperror.BadRequest("MODEL_REQUIRED", "model is required"))
		return
	}
	httpx.Success(c, h.service.Catalog.Lookup(model))
}

func (h *ModelAttributeHandler) Models(c *gin.Context) {
	if h.service.Catalog.Index != nil {
		index, _ := h.service.Catalog.Index()
		httpx.Success(c, index.ModelIDs())
		return
	}
	names := []string{}
	for _, row := range h.service.Catalog.Snapshot().Items {
		names = append(names, row.Model)
	}
	httpx.Success(c, names)
}

func (h *ModelAttributeHandler) Update(c *gin.Context) {
	if err := h.service.Catalog.Update(); err != nil {
		httpx.Error(c, 502, "failed to update model catalog")
		return
	}
	httpx.Success(c, gin.H{"updated": true})
}

// attributePageStart 避免异常大页码乘法溢出导致切片越界。
func attributePageStart(page, size, total int) int {
	if page-1 > total/size {
		return total
	}
	return (page - 1) * size
}
