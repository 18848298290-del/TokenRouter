package creative

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/image/webp"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

const (
	PlatformOpenAI = "openai"
	PlatformGemini = "gemini"
	PlatformGrok   = "grok"

	DefaultCreativeResponseMime = "image/png"

	MaxCreativeMaskBytes         = 4 << 20
	MaxCreativeGeminiInlineBytes = 20 << 20
)

// ErrCreativeContentBlocked reports a moderation rejection.
var ErrCreativeContentBlocked = infraerrors.New(infraerrors.CategoryForbidden, "CREATIVE_CONTENT_BLOCKED", "creative content failed moderation")

type (
	UserAccess interface{ CanBindGroup(int64, bool) bool }
	UserReader interface {
		GetByID(context.Context, int64) (UserAccess, error)
	}
)

type GroupView struct {
	ID   int64
	Name string
	// ClaudeCodeOnly excludes the group from Creative Studio; no fallback group is selected.
	ClaudeCodeOnly                            bool
	IsExclusive, AllowImageGeneration, Active bool
	RateMultiplier                            float64
	Operations                                map[string][]string
	ProtocolFallbacks                         map[protocol.ProtocolID][]protocol.ProtocolID
	RoutingPolicy                             routing.GroupRoutingPolicy
	// ModelsList contains the group's enabled custom request names.
	ModelsList []string
}
type GroupReader interface {
	GetByIDLite(context.Context, int64) (*GroupView, error)
	ListActive(context.Context) ([]GroupView, error)
}
type CatalogProvider interface {
	PlatformID() string
	AllowsProtocol(protocol.ProtocolID, map[protocol.ProtocolID][]protocol.ProtocolID) bool
	IsSchedulable() bool
	GetModelMapping() map[string]string
	GetConfiguredRequestModels() []string
	ResolveMappedModel(string) (string, bool)
	IsModelSupported(string) bool
}
type ProviderReader interface {
	ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]CatalogProvider, error)
}
type UserRateReader interface {
	GetByUserAndGroup(context.Context, int64, int64) (*float64, error)
}
type SettingReader interface {
	IsCreativeEnabled(context.Context) bool
	GetCreativeModelSettings(context.Context) []CreativeModelSetting
}
type ModerationInput struct {
	RequestID                                      string
	UserID, BillingUserID                          int64
	GroupID                                        *int64
	GroupName, Endpoint, Platform, Model, Protocol string
	Body                                           []byte
	NoMediaRetention                               bool
}
type (
	ModerationDecision struct{ Allowed bool }
	Moderator          interface {
		Check(context.Context, ModerationInput) (*ModerationDecision, error)
	}
)

type PublicOptions struct {
	Enabled                           bool
	MaxPromptChars                    int
	MaxAssetBytes, MaxTotalInputBytes int64
	DefaultImageSize                  string
}
type Public struct {
	Attributes             *routing.ModelAttributeService
	Now                    func() time.Time
	Repo                   CreativeRunRepository
	UserRepo               UserReader
	GroupRepo              GroupReader
	ProviderRepo           ProviderReader
	UserGroupRateRepo      UserRateReader
	Queue                  CreativeRunQueue
	TransientStore         CreativeTransientStore
	Results                *Results
	Settings               SettingReader
	Options                PublicOptions
	UserNotFound           error
	EnsureKey              func(context.Context, int64, int64) (int64, error)
	GroupMapping           func(context.Context, int64, string) routing.GroupMappingResult
	ImageUnitPrice         func(context.Context, *GroupView, string, string) (float64, bool)
	SubscriptionMultiplier func(context.Context, int64, *GroupView, float64) (float64, bool)
	Moderation             Moderator
	RequestID              func(context.Context) string
	Observe                func(string, ...any)
}

// CreativeModelCapabilities contains the model parameters exposed by Creative Studio.
type CreativeModelCapabilities struct {
	AspectRatios       []string
	Qualities          []string
	OutputFormats      []string
	OutputCompression  *CreativeNumericRange
	BackgroundOptions  []string
	ThinkingLevels     []string
	MaxOutputCount     int
	MaxReferenceImages int
}

type creativeModelRoute struct {
	Attributes          modelcatalog.Attributes
	OperationAttributes map[string]modelcatalog.Attributes
	Platform, Model     string
	Operations          []string
}

// ValidatedCreativeParams contains normalized, validated creation parameters.
type ValidatedCreativeParams struct {
	Platform      string
	Group         *GroupView
	Model         string
	FinalModel    string
	Operation     string
	Prompt        string
	PromptHash    string
	ImageSize     string
	AspectRatio   string
	Quality       string
	Background    string
	ThinkingLevel string
	OutputCount   int
	Sources       []CreativeInputImage
	Mask          *CreativeInputImage
	Fingerprint   string
}

// CreativeFingerprintPayload defines the fixed JSON field order used for request fingerprints.
type CreativeFingerprintPayload struct {
	GroupID       int64    `json:"group_id"`
	Model         string   `json:"model"`
	Operation     string   `json:"operation"`
	PromptSHA256  string   `json:"prompt_sha256"`
	ImageSHA256   []string `json:"image_sha256"`
	MaskSHA256    string   `json:"mask_sha256,omitempty"`
	ImageSize     string   `json:"image_size"`
	AspectRatio   string   `json:"aspect_ratio"`
	Quality       string   `json:"quality,omitempty"`
	Background    string   `json:"background,omitempty"`
	ThinkingLevel string   `json:"thinking_level,omitempty"`
	OutputCount   int      `json:"output_count"`
}

// CreativePricingSnapshot records prices and multipliers at task creation.
type CreativePricingSnapshot struct {
	BaseUnitPrice              float64
	SubscriptionRateMultiplier float64
	BalanceRateMultiplier      float64
	PlanGroupRateEnabled       bool
	EstimatedCost              float64
}

func (s *Public) warn(event string, values ...any) {
	if s.Observe != nil {
		s.Observe(event, values...)
	}
}

func (s *Public) Enabled(ctx context.Context) bool {
	return s != nil && s.Repo != nil && s.GroupRepo != nil && s.Options.Enabled && s.Settings != nil && s.Settings.IsCreativeEnabled(ctx)
}

func (s *Public) MaxPromptChars() int {
	if s != nil && s.Options.MaxPromptChars > 0 {
		return s.Options.MaxPromptChars
	}
	return 8000
}

// MaxAssetBytes returns the per-file input limit shared by handlers and model validation.
func (s *Public) MaxAssetBytes() int64 {
	if s != nil && s.Options.MaxAssetBytes > 0 {
		return s.Options.MaxAssetBytes
	}
	return 33554432
}

// MaxTotalInputBytes returns the total input byte limit for one task.
func (s *Public) MaxTotalInputBytes() int64 {
	if s != nil && s.Options.MaxTotalInputBytes > 0 {
		return s.Options.MaxTotalInputBytes
	}
	return 67108864
}

func (s *Public) DefaultImageSize() string {
	if s != nil && strings.TrimSpace(s.Options.DefaultImageSize) != "" {
		return strings.TrimSpace(s.Options.DefaultImageSize)
	}
	return "1K"
}

func mappedCatalogModel(a CatalogProvider, m string) string {
	if a == nil {
		return ""
	}
	value, _ := a.ResolveMappedModel(m)
	if strings.TrimSpace(value) == "" {
		return m
	}
	return strings.TrimSpace(value)
}

// GetCapabilities returns input limits shared by the frontend and multipart parser.
func (s *Public) GetCapabilities(ctx context.Context) *CreativeCapabilitiesResponse {
	return &CreativeCapabilitiesResponse{
		MaxPromptChars:     s.MaxPromptChars(),
		MaxAssetBytes:      s.MaxAssetBytes(),
		MaxTotalInputBytes: s.MaxTotalInputBytes(),
		MaxMaskBytes:       MaxCreativeMaskBytes,
		AllowedMIMETypes:   []string{"image/png", "image/jpeg", "image/webp"},
	}
}

// ListModels returns the group and image model combinations available to the user.
func (s *Public) ListModels(ctx context.Context, userID int64) (*CreativeModelsResponse, error) {
	if !s.Enabled(ctx) {
		// An empty list lets the frontend display the disabled state.
		return &CreativeModelsResponse{Data: make([]CreativeModelPublic, 0)}, nil
	}
	user, err := s.UserRepo.GetByID(ctx, userID)
	if err != nil || user == nil {
		return nil, s.UserNotFound
	}
	settings := s.CreativeModelSettings(ctx)
	modelSettings := CreativeModelSettingsIndex(settings)
	if len(modelSettings) == 0 {
		return &CreativeModelsResponse{Data: make([]CreativeModelPublic, 0)}, nil
	}
	groups, err := s.GroupRepo.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	out := &CreativeModelsResponse{Data: make([]CreativeModelPublic, 0)}
	for i := range groups {
		group := &groups[i]
		if !user.CanBindGroup(group.ID, group.IsExclusive) {
			continue
		}
		if !group.AllowImageGeneration || !group.Active || group.ClaudeCodeOnly {
			continue
		}
		platformOperations := groupOperations(group)
		if len(platformOperations) == 0 {
			continue
		}
		query, err := s.prepareCreativeModels(ctx, group)
		if err != nil {
			return nil, err
		}
		routes, err := query.resolveModels(ctx, creativeSettingsModels(settings, group.ID))
		if err != nil {
			return nil, err
		}
		models := creativeModelsFromRoutes(routes)
		if err != nil {
			return nil, err
		}
		modelNames := make([]string, 0, len(models))
		for model := range models {
			modelNames = append(modelNames, model)
		}
		sort.Strings(modelNames)
		for _, model := range modelNames {
			operations, configured := CreativeOperationsForModel(modelSettings, group.ID, model, routes[model].Operations)
			if !configured || len(operations) == 0 {
				continue
			}
			// Resolve sizes from the selected route's model capabilities, independently of prices.
			finalModel := models[model]
			if finalModel == "" {
				finalModel = model
			}
			imageSizes := creativeRouteImageSizes(routes[model])
			if len(imageSizes) == 0 {
				continue
			}
			capabilities := CreativeCapabilitiesForModel(routes[model].Platform, finalModel, routes[model].Attributes)
			pricingModel := s.BillingModel(ctx, group, model, finalModel)
			if _, ok := s.ImageUnitPrice(ctx, group, pricingModel, imageSizes[0]); !ok {
				continue
			}
			out.Data = append(out.Data, CreativeModelPublic{
				GroupID:            group.ID,
				GroupName:          group.Name,
				Model:              model,
				Operations:         operations,
				ImageSizes:         imageSizes,
				AspectRatios:       capabilities.AspectRatios,
				Qualities:          capabilities.Qualities,
				OutputFormats:      capabilities.OutputFormats,
				OutputCompression:  capabilities.OutputCompression,
				BackgroundOptions:  capabilities.BackgroundOptions,
				ThinkingLevels:     capabilities.ThinkingLevels,
				MaxOutputCount:     capabilities.MaxOutputCount,
				MaxReferenceImages: capabilities.MaxReferenceImages,
				Price512:           s.CreativePrice(ctx, group, pricingModel, "512"),
				Price1K:            s.CreativePrice(ctx, group, pricingModel, "1K"),
				Price2K:            s.CreativePrice(ctx, group, pricingModel, "2K"),
				Price4K:            s.CreativePrice(ctx, group, pricingModel, "4K"),
			})
		}
	}
	return out, nil
}

// ListCreativeModelCandidates returns models administrators can add to the allowlist.
// It applies group, provider, and platform rules without filtering by user permissions.
func (s *Public) ListCreativeModelCandidates(ctx context.Context) ([]CreativeModelCandidate, error) {
	if s == nil || s.GroupRepo == nil {
		return nil, errors.New("creative group repository is not configured")
	}
	groups, err := s.GroupRepo.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]CreativeModelCandidate, 0)
	for i := range groups {
		group := &groups[i]
		if !group.Active || !group.AllowImageGeneration || group.ClaudeCodeOnly {
			continue
		}
		operations := groupOperations(group)
		if len(operations) == 0 {
			continue
		}
		routes, err := s.creativeModelRoutes(ctx, group)
		models := creativeModelsFromRoutes(routes)
		if err != nil {
			return nil, err
		}
		modelNames := make([]string, 0, len(models))
		for model, finalModel := range models {
			if finalModel == "" {
				finalModel = model
			}
			if len(CreativeImageSizesForModel(routes[model].Platform, finalModel)) == 0 {
				continue
			}
			modelNames = append(modelNames, model)
		}
		sort.Strings(modelNames)
		for _, model := range modelNames {
			out = append(out, CreativeModelCandidate{
				GroupID:    group.ID,
				GroupName:  group.Name,
				Platform:   routes[model].Platform,
				Model:      model,
				Operations: append([]string(nil), routes[model].Operations...),
			})
		}
	}
	return out, nil
}

func (s *Public) CreativeModelSettings(ctx context.Context) []CreativeModelSetting {
	if s == nil || s.Settings == nil {
		return []CreativeModelSetting{}
	}
	return s.Settings.GetCreativeModelSettings(ctx)
}

// CreativePrice returns the per-image display price with the group's marketplace multiplier.
func (s *Public) CreativePrice(ctx context.Context, group *GroupView, model, imageSize string) float64 {
	unit, ok := s.ImageUnitPrice(ctx, group, model, imageSize)
	if !ok {
		return 0
	}
	return unit * group.RateMultiplier
}

// CreativeOperationsForPlatform returns the operations implemented for the platform.
func CreativeOperationsForPlatform(platform string) []string {
	switch strings.TrimSpace(platform) {
	case PlatformOpenAI:
		return []string{CreativeOperationGenerate, CreativeOperationEdit, CreativeOperationInpaint}
	case PlatformGemini, PlatformGrok:
		return []string{CreativeOperationGenerate, CreativeOperationEdit}
	default:
		return nil
	}
}

// CreativeDefaultImageSizesForPlatform returns size tiers to use when model capabilities are unknown.
// OpenAI and Gemini default to 1K/2K/4K; Grok defaults to 1K/2K.
// Callers narrow these tiers using known model capabilities.
func CreativeDefaultImageSizesForPlatform(platform string) []string {
	switch strings.TrimSpace(platform) {
	case PlatformOpenAI:
		return []string{"1K", "2K", "4K"}
	case PlatformGrok:
		return []string{"1K", "2K"}
	case PlatformGemini:
		return []string{"1K", "2K", "4K"}
	default:
		return nil
	}
}

// CreativeCapabilitiesForModel converts shared media attributes into Creative Studio parameters.
func CreativeCapabilitiesForModel(platform, model string, attributes ...modelcatalog.Attributes) CreativeModelCapabilities {
	result := CreativeModelCapabilities{AspectRatios: []string{}, Qualities: []string{}, OutputFormats: []string{}, BackgroundOptions: []string{}, ThinkingLevels: []string{}, MaxOutputCount: 1, MaxReferenceImages: 1}
	var media *modelcatalog.MediaCapabilities
	if len(attributes) > 0 {
		media = attributes[0].Media
		result.ThinkingLevels = attributes[0].Efforts()
	} else {
		media = modelcatalog.MediaForModel(platform, model)
	}
	if media == nil {
		return result
	}
	result.AspectRatios = append(result.AspectRatios, media.AspectRatios...)
	result.Qualities = append(result.Qualities, media.Qualities...)
	result.OutputFormats = append(result.OutputFormats, media.OutputFormats...)
	result.BackgroundOptions = append(result.BackgroundOptions, media.BackgroundOptions...)
	if media.MaxOutputCount != nil {
		result.MaxOutputCount = *media.MaxOutputCount
	}
	if media.MaxReferenceImages != nil {
		result.MaxReferenceImages = *media.MaxReferenceImages
	}
	return result
}

func creativeRouteImageSizes(route creativeModelRoute) []string {
	if route.Attributes.Media != nil && route.Attributes.Media.ImageSizes != nil {
		return append([]string(nil), route.Attributes.Media.ImageSizes...)
	}
	return CreativeImageSizesForModel(route.Platform, route.Model)
}

func CreativeNormalizedModelID(model string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(model)), "models/")
}

func CreativeGeminiMaxReferenceImages(model string) int {
	if media := modelcatalog.MediaForModel(PlatformGemini, model); media != nil && media.MaxReferenceImages != nil {
		return *media.MaxReferenceImages
	}
	return 1
}

// CreativeImageSizesForModel returns model size tiers, using platform defaults for unknown models.
// Size availability is independent of configured prices.
func CreativeImageSizesForModel(platform, model string) []string {
	if media := modelcatalog.MediaForModel(platform, model); media != nil {
		return append([]string(nil), media.ImageSizes...)
	}
	return CreativeDefaultImageSizesForPlatform(platform)
}

// CreativeFilterImageSizesForModel applies known model size limits and adds Gemini's supported 512 tier.
// Unknown models keep the supplied tiers; known 1K-only models are restricted to 1K.
func CreativeFilterImageSizesForModel(platform, model string, sizes []string) []string {
	platform = strings.TrimSpace(platform)
	if platform == PlatformGrok && upstream.IsGrokImageGenerationModel(model) {
		return CreativeFilterImageSizes(sizes, "1K", "2K")
	}
	if platform == PlatformOpenAI && upstream.IsGPTImageGenerationModel(model) && !IsCreativeGPTImage2Model(model) {
		return CreativeFilterImageSizes(sizes, "1K", "2K")
	}
	if platform != PlatformGemini {
		return append([]string(nil), sizes...)
	}
	if IsCreativeGemini512Model(model) {
		filtered := append([]string(nil), sizes...)
		if ContainsCreativeImageSize(filtered, "1K") && !ContainsCreativeImageSize(filtered, "512") {
			filtered = append([]string{"512"}, filtered...)
		}
		return filtered
	}
	if !IsCreativeGemini1KOnlyModel(model) {
		return append([]string(nil), sizes...)
	}
	filtered := make([]string, 0, 1)
	for _, size := range sizes {
		if strings.EqualFold(strings.TrimSpace(size), "1K") {
			filtered = append(filtered, "1K")
		}
	}
	return filtered
}

// CreativeFilterImageSizes keeps allowed tiers in the supplied order.
func CreativeFilterImageSizes(sizes []string, allowed ...string) []string {
	filtered := make([]string, 0, len(sizes))
	for _, size := range sizes {
		if ContainsCreativeImageSize(allowed, size) {
			filtered = append(filtered, size)
		}
	}
	return filtered
}

// IsCreativeGemini512Model reports whether the model belongs to the Gemini family supporting 512 output.
func IsCreativeGemini512Model(model string) bool {
	model = CreativeNormalizedModelID(model)
	return strings.HasPrefix(model, "gemini-3.1-flash-image") && !strings.HasPrefix(model, "gemini-3.1-flash-lite-image")
}

// IsCreativeGemini1KOnlyModel reports whether the model belongs to a known 1K-only family.
func IsCreativeGemini1KOnlyModel(model string) bool {
	model = CreativeNormalizedModelID(model)
	switch {
	case strings.HasPrefix(model, "gemini-2.5-flash-image"):
		return true
	case strings.HasPrefix(model, "gemini-3.1-flash-lite-image"):
		return true
	case model == "gemini-2.0-flash-exp-image-generation":
		return true
	default:
		return false
	}
}

func IsCreativeGPTImage2Model(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gpt-image-2")
}

func ContainsCreativeImageSize(sizes []string, target string) bool {
	for _, size := range sizes {
		if strings.EqualFold(strings.TrimSpace(size), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func CreativeCanonicalOption(value string, options []string) (string, bool) {
	value = strings.TrimSpace(value)
	for _, option := range options {
		if strings.EqualFold(strings.TrimSpace(option), value) {
			return option, true
		}
	}
	return "", false
}

func CreativeContainsOption(options []string, value string) bool {
	_, ok := CreativeCanonicalOption(value, options)
	return ok
}

// CreativeDefaultOption selects the preferred option, then the first available option, or an empty string.
func CreativeDefaultOption(options []string, preferred string) string {
	if CreativeContainsOption(options, preferred) {
		value, _ := CreativeCanonicalOption(preferred, options)
		return value
	}
	if len(options) > 0 {
		return options[0]
	}
	return ""
}

// CreativeModelsForGroup resolves image models through group/provider mappings and staged allowlists.
// @project-doc docs/domains/creative_studio.md#creative_model_policy
func (s *Public) CreativeModelsForGroup(ctx context.Context, group *GroupView) (map[string]string, error) {
	routes, err := s.creativeModelRoutes(ctx, group)
	return creativeModelsFromRoutes(routes), err
}

func creativeModelsFromRoutes(routes map[string]creativeModelRoute) map[string]string {
	models := make(map[string]string, len(routes))
	for name, route := range routes {
		models[name] = route.Model
	}
	return models
}

// groupOperations returns candidate platform operations in CreativeOperationOrder.
func groupOperations(group *GroupView) []string {
	var operations []string
	for _, operation := range CreativeOperationOrder {
		for _, allowed := range group.Operations {
			if CreativeContainsOption(allowed, operation) {
				operations = append(operations, operation)
				break
			}
		}
	}
	return operations
}

// creativeModelRoutes resolves image models from saved settings and platform execution rules.
func (s *Public) creativeModelRoutes(ctx context.Context, group *GroupView) (map[string]creativeModelRoute, error) {
	query, err := s.prepareCreativeModels(ctx, group)
	if err != nil || query == nil {
		return map[string]creativeModelRoute{}, err
	}
	configured := creativeSettingsModels(s.CreativeModelSettings(ctx), group.ID)
	return query.resolveModels(ctx, query.candidates(configured))
}

// IsCreativeGeminiImageModel recognizes image model names used in explicit allowlists.
// The nano-banana-* proxy aliases are accepted as Gemini image request models.
func IsCreativeGeminiImageModel(model string) bool {
	model = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(model)), "models/")
	return (strings.HasPrefix(model, "gemini-") && strings.Contains(model, "image")) ||
		strings.HasPrefix(model, "nano-banana-")
}

// CreateRun validates and moderates a request, handles replay, then provisions funding, transient input,
// and queue entry.
// @project-doc docs/domains/creative_studio.md#creative_task_lifecycle
func (s *Public) CreateRun(ctx context.Context, scope CreativeRunScope, params CreateCreativeRunParamsPublic, idempotencyKey string) (*CreativeRunPublic, error) {
	normalizedScope, err := NormalizeCreativeRunScope(scope)
	if err != nil {
		return nil, err
	}
	scope = normalizedScope
	userID := scope.UserID
	if !s.Enabled(ctx) {
		return nil, ErrCreativeDisabled
	}
	validated, err := s.ValidateCreateParams(ctx, userID, &params)
	if err != nil {
		return nil, err
	}
	if err := s.ModerateCreativeRequest(ctx, userID, validated); err != nil {
		return nil, err
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey != "" {
		existing, err := s.Repo.GetCreativeRunByIdempotencyKey(ctx, scope, idempotencyKey)
		if err == nil {
			if existing.RequestFingerprint != validated.Fingerprint {
				return nil, ErrCreativeRunIdempotencyConflict
			}
			out, err := s.Results.GetRunPublic(ctx, existing.RunID)
			if err != nil {
				return nil, err
			}
			out.IdempotentReplay = true
			return out, nil
		}
		if !errors.Is(err, ErrCreativeRunNotFound) {
			return nil, err
		}
	}
	pricing, err := s.ResolveCreativePricing(ctx, userID, validated)
	if err != nil {
		return nil, err
	}
	managedKey, err := s.EnsureKey(ctx, userID, validated.Group.ID)
	if err != nil {
		return nil, err
	}
	runID, err := NewCreativeRunID()
	if err != nil {
		return nil, err
	}
	holdAmount := pricing.EstimatedCost
	run, err := s.Repo.CreateCreativeRun(ctx, CreateCreativeRunParams{
		RunID:                      runID,
		UserID:                     userID,
		WorkspaceID:                scope.WorkspaceID,
		GroupID:                    validated.Group.ID,
		APIKeyID:                   managedKey,
		Model:                      validated.Model,
		RequestedModel:             params.Model,
		Operation:                  validated.Operation,
		Platform:                   validated.Platform,
		RequestedOutputCount:       validated.OutputCount,
		ImageSize:                  validated.ImageSize,
		AspectRatio:                validated.AspectRatio,
		ResponseMIMEType:           DefaultCreativeResponseMime,
		PromptHash:                 validated.PromptHash,
		RequestFingerprint:         validated.Fingerprint,
		IdempotencyKey:             CreativeStringPtr(idempotencyKey),
		EstimatedCost:              pricing.EstimatedCost,
		HoldAmount:                 holdAmount,
		BaseUnitPrice:              pricing.BaseUnitPrice,
		SubscriptionRateMultiplier: pricing.SubscriptionRateMultiplier,
		BalanceRateMultiplier:      pricing.BalanceRateMultiplier,
		PlanGroupRateEnabled:       pricing.PlanGroupRateEnabled,
	})
	if err != nil {
		return nil, err
	}
	telemetry.RecordRelatedRequest(ctx, telemetry.RequestRecord{RequestID: telemetry.StableRequestID(CreativeSettlementRequestID(run.RunID)), State: "running", Model: run.Model, StartedAt: run.CreatedAt, Aliases: []telemetry.RequestAlias{{Kind: "task", Value: run.RunID}, {Kind: "billing", Value: CreativeSettlementRequestID(run.RunID)}}})
	if err := s.Results.EnsureCreativeOutbox(ctx, run.RunID, CreativeRunOutboxProvision); err != nil {
		return nil, err
	}
	// Creation failures schedule release and cleanup through the outbox.
	if err := s.Results.Funding.Reserve(ctx, run); err != nil {
		s.FailRunAfterCreateError(ctx, run, "BILLING_HOLD_FAILED", err)
		return nil, err
	}
	if marker, ok := s.Repo.(CreativeRunAllowanceMarker); ok {
		if err := marker.SetCreativeRunAllowanceReserved(ctx, run.RunID, run.AllowanceReserved); err != nil {
			// Reserve is idempotent; retain the provisioning outbox so recovery can persist the
			// reservation marker.
			return nil, err
		}
	}
	if err := s.Repo.SetCreativeRunProvisioningPhase(ctx, run.RunID, CreativeProvisioningPhaseHoldReserved); err != nil {
		s.FailRunAfterCreateError(ctx, run, "PROVISIONING_PHASE_FAILED", err)
		return nil, err
	}
	s.Results.InvalidateCreativeAuthCache(ctx, userID)
	if err := s.SaveRunTransient(ctx, run, validated); err != nil {
		s.FailRunAfterCreateError(ctx, run, "TRANSIENT_SAVE_FAILED", err)
		return nil, ErrCreativeTransientFailed
	}
	if err := s.Repo.SetCreativeRunProvisioningPhase(ctx, run.RunID, CreativeProvisioningPhaseTransientSaved); err != nil {
		s.FailRunAfterCreateError(ctx, run, "PROVISIONING_PHASE_FAILED", err)
		return nil, err
	}
	if s.Queue == nil {
		err := errors.New("creative queue is not configured")
		s.FailRunAfterCreateError(ctx, run, "QUEUE_FAILED", err)
		return nil, err
	}
	if err := s.Queue.Enqueue(ctx, run.RunID); err != nil && !errors.Is(err, ErrCreativeAlreadyQueued) {
		s.FailRunAfterCreateError(ctx, run, "QUEUE_FAILED", err)
		return nil, err
	}
	if err := s.Repo.SetCreativeRunProvisioningPhase(ctx, run.RunID, CreativeProvisioningPhaseEnqueued); err != nil {
		s.FailRunAfterCreateError(ctx, run, "PROVISIONING_PHASE_FAILED", err)
		return nil, err
	}
	out, err := s.Results.GetRunPublic(ctx, run.RunID)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// FailRunAfterCreateError requests release_pending with failed as the target and schedules recovery.
func (s *Public) FailRunAfterCreateError(ctx context.Context, run *CreativeRun, code string, cause error) {
	if run == nil {
		return
	}
	message := SanitizeCreativeMessage(cause.Error())
	if err := s.Repo.TransitionCreativeRunStatus(ctx, run.RunID, CreativeRunStatusReleasePending, CreativeRunTransitionOptions{
		ErrorCode:           &code,
		ErrorMessage:        &message,
		ReleaseTargetStatus: CreativeRunStatusFailed,
	}); err != nil {
		s.warn("creative.create_failure_mark_failed",
			"run_id", run.RunID,
			"error", err,
		)
	}
	_ = s.Results.EnsureCreativeOutbox(ctx, run.RunID, CreativeRunOutboxRelease)
}

// SaveRunTransient writes the payload and input bytes to temporary Redis storage.
func (s *Public) SaveRunTransient(ctx context.Context, run *CreativeRun, validated *ValidatedCreativeParams) error {
	if s.TransientStore == nil {
		return errors.New("creative transient store is not configured")
	}
	payload := &CreativeRunPayload{
		RunID:              run.RunID,
		UserID:             run.UserID,
		GroupID:            run.GroupID,
		APIKeyID:           run.APIKeyID,
		Model:              run.Model,
		Operation:          run.Operation,
		Prompt:             validated.Prompt,
		ImageSize:          run.ImageSize,
		AspectRatio:        run.AspectRatio,
		Background:         validated.Background,
		ThinkingLevel:      validated.ThinkingLevel,
		Quality:            validated.Quality,
		SourceCount:        len(validated.Sources),
		HasMask:            validated.Mask != nil,
		RequestFingerprint: run.RequestFingerprint,
	}
	if err := s.TransientStore.SavePayload(ctx, run.RunID, payload); err != nil {
		return err
	}
	for i := range validated.Sources {
		if err := s.TransientStore.SaveInput(ctx, run.RunID, i, validated.Sources[i].Bytes); err != nil {
			return err
		}
	}
	if validated.Mask != nil {
		if err := s.TransientStore.SaveMask(ctx, run.RunID, validated.Mask.Bytes); err != nil {
			return err
		}
	}
	return nil
}

// ValidateCreateParams checks access, model capabilities, and inputs, then returns normalized parameters
// and a fingerprint.
func (s *Public) ValidateCreateParams(ctx context.Context, userID int64, params *CreateCreativeRunParamsPublic) (*ValidatedCreativeParams, error) {
	if params == nil {
		return nil, ErrCreativeInvalidParams
	}
	user, err := s.UserRepo.GetByID(ctx, userID)
	if err != nil || user == nil {
		return nil, s.UserNotFound
	}
	group, err := s.GroupRepo.GetByIDLite(ctx, params.GroupID)
	if err != nil || group == nil || !group.Active {
		return nil, ErrCreativeGroupForbidden
	}
	if !user.CanBindGroup(group.ID, group.IsExclusive) {
		return nil, ErrCreativeGroupForbidden
	}
	if group.ClaudeCodeOnly {
		return nil, ErrCreativeGroupForbidden
	}
	if !group.AllowImageGeneration {
		return nil, ErrCreativeGroupImageDisabled
	}
	operations := groupOperations(group)
	if len(operations) == 0 {
		return nil, ErrCreativeGroupImageDisabled
	}
	model := strings.TrimSpace(params.Model)
	modelSettings := CreativeModelSettingsIndex(s.CreativeModelSettings(ctx))
	configuredOperations, configured := CreativeOperationsForModel(modelSettings, group.ID, model, operations)
	if !configured {
		return nil, ErrCreativeInvalidModel
	}
	operations = configuredOperations
	if len(operations) == 0 {
		return nil, ErrCreativeOperationUnsupported
	}
	query, err := s.prepareCreativeModels(ctx, group)
	if err != nil {
		return nil, err
	}
	route, available, err := query.resolve(ctx, model, false)
	if err != nil {
		return nil, err
	}
	if !available {
		return nil, ErrCreativeInvalidModel
	}
	finalModel := route.Model
	if finalModel == "" {
		finalModel = model
	}
	operations = intersectCreativeOperations(operations, route.Operations)
	operation := strings.TrimSpace(params.Operation)
	operationAllowed := slices.Contains(operations, operation)
	if !operationAllowed {
		return nil, ErrCreativeOperationUnsupported
	}
	if attrs, found := route.OperationAttributes[operation]; found {
		route.Attributes = attrs
	}
	capabilities := CreativeCapabilitiesForModel(route.Platform, finalModel, route.Attributes)
	if capabilities.MaxReferenceImages > 0 && len(params.SourceImages) > capabilities.MaxReferenceImages {
		return nil, ErrCreativeInvalidParams
	}

	prompt := strings.TrimSpace(params.Prompt)
	if prompt == "" {
		return nil, ErrCreativeInvalidParams
	}
	if utf8.RuneCountInString(prompt) > s.MaxPromptChars() {
		return nil, ErrCreativePromptTooLong
	}

	outputCount := params.OutputCount
	if outputCount == 0 {
		outputCount = 1
	}
	if outputCount < 1 || outputCount > capabilities.MaxOutputCount {
		return nil, ErrCreativeInvalidParams
	}

	imageSize := strings.TrimSpace(params.ImageSize)
	if imageSize == "" {
		imageSize = s.DefaultImageSize()
	}
	imageSize, supported := CreativeCanonicalOption(imageSize, creativeRouteImageSizes(route))
	if !supported {
		return nil, ErrCreativeInvalidParams
	}
	aspectRatio := strings.TrimSpace(params.AspectRatio)
	if aspectRatio == "" {
		aspectRatio = CreativeDefaultOption(capabilities.AspectRatios, "auto")
	} else {
		aspectRatio, supported = CreativeCanonicalOption(aspectRatio, capabilities.AspectRatios)
		if !supported {
			return nil, ErrCreativeInvalidParams
		}
	}
	quality := strings.ToLower(strings.TrimSpace(params.Quality))
	if len(capabilities.Qualities) > 0 {
		if quality == "" {
			quality = CreativeDefaultOption(capabilities.Qualities, "medium")
		} else if !CreativeContainsOption(capabilities.Qualities, quality) {
			return nil, ErrCreativeInvalidParams
		}
	} else if quality != "" {
		return nil, ErrCreativeInvalidParams
	}
	background := strings.ToLower(strings.TrimSpace(params.Background))
	if len(capabilities.BackgroundOptions) > 0 {
		if background == "" {
			background = CreativeDefaultOption(capabilities.BackgroundOptions, "auto")
		} else if !CreativeContainsOption(capabilities.BackgroundOptions, background) {
			return nil, ErrCreativeInvalidParams
		}
	} else if background != "" {
		return nil, ErrCreativeInvalidParams
	}
	thinkingLevel := strings.ToLower(strings.TrimSpace(params.ThinkingLevel))
	if len(capabilities.ThinkingLevels) > 0 {
		if thinkingLevel == "" {
			thinkingLevel = CreativeDefaultOption(capabilities.ThinkingLevels, "minimal")
		} else if !CreativeContainsOption(capabilities.ThinkingLevels, thinkingLevel) {
			return nil, ErrCreativeInvalidParams
		}
	} else if thinkingLevel != "" {
		return nil, ErrCreativeInvalidParams
	}

	sources := make([]CreativeInputImage, 0, len(params.SourceImages))
	totalBytes := 0
	for i := range params.SourceImages {
		source, err := NormalizeCreativeImageInput(params.SourceImages[i], s.MaxAssetBytes())
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
		totalBytes += len(source.Bytes)
	}
	var mask *CreativeInputImage
	if params.Mask != nil && len(params.Mask.Bytes) > 0 {
		if len(params.Mask.Bytes) > MaxCreativeMaskBytes {
			return nil, ErrCreativeAssetTooLarge
		}
		normalized, err := NormalizeCreativeImageInput(*params.Mask, s.MaxAssetBytes())
		if err != nil {
			return nil, err
		}
		if normalized.Mime != "image/png" {
			return nil, ErrCreativeMaskRequired
		}
		mask = &normalized
		totalBytes += len(normalized.Bytes)
	}
	if totalBytes > 0 && int64(totalBytes) > s.MaxTotalInputBytes() {
		return nil, ErrCreativeInputTooLarge
	}
	if route.Platform == PlatformGemini {
		encodedBytes := base64.StdEncoding.EncodedLen(len([]byte(prompt)))
		for _, source := range sources {
			encodedBytes += base64.StdEncoding.EncodedLen(len(source.Bytes))
		}
		if mask != nil {
			encodedBytes += base64.StdEncoding.EncodedLen(len(mask.Bytes))
		}
		// Estimate the full Gemini JSON request size, reserving extra bytes for field overhead.
		if int64(encodedBytes)+int64(len(prompt))+4096 > MaxCreativeGeminiInlineBytes {
			return nil, ErrCreativeInputTooLarge
		}
	}

	switch operation {
	case CreativeOperationEdit:
		if len(sources) == 0 {
			return nil, ErrCreativeInvalidParams
		}
	case CreativeOperationInpaint:
		if len(sources) == 0 || mask == nil {
			return nil, ErrCreativeMaskRequired
		}
		maskWidth, maskHeight, err := CreativeImageDimensions(mask.Bytes, mask.Mime)
		if err != nil {
			return nil, ErrCreativeMaskRequired
		}
		sourceWidth, sourceHeight, err := CreativeImageDimensions(sources[0].Bytes, sources[0].Mime)
		if err != nil {
			return nil, ErrCreativeInvalidMime
		}
		if maskWidth != sourceWidth || maskHeight != sourceHeight {
			return nil, ErrCreativeMaskSizeMismatch
		}
	}
	if mask != nil && operation != CreativeOperationInpaint {
		return nil, ErrCreativeInvalidParams
	}

	promptHash := Sha256Hex([]byte(prompt))
	fingerprint := BuildCreativeRequestFingerprint(CreativeFingerprintPayload{
		GroupID:       group.ID,
		Model:         model,
		Operation:     operation,
		PromptSHA256:  promptHash,
		ImageSHA256:   CreativeImageHashes(sources),
		MaskSHA256:    CreativeImageHash(mask),
		ImageSize:     imageSize,
		AspectRatio:   aspectRatio,
		Quality:       quality,
		Background:    background,
		ThinkingLevel: thinkingLevel,
		OutputCount:   outputCount,
	})
	return &ValidatedCreativeParams{
		Group:         group,
		Platform:      route.Platform,
		Model:         model,
		FinalModel:    finalModel,
		Operation:     operation,
		Prompt:        prompt,
		PromptHash:    promptHash,
		ImageSize:     imageSize,
		AspectRatio:   aspectRatio,
		Quality:       quality,
		Background:    background,
		ThinkingLevel: thinkingLevel,
		OutputCount:   outputCount,
		Sources:       sources,
		Mask:          mask,
		Fingerprint:   fingerprint,
	}, nil
}

// NormalizeCreativeImageInput checks nonempty input, the byte limit, and the declared and detected MIME types.
func NormalizeCreativeImageInput(input CreativeInputImage, maxBytes int64) (CreativeInputImage, error) {
	if len(input.Bytes) == 0 {
		return input, ErrCreativeInvalidMime
	}
	if int64(len(input.Bytes)) > maxBytes {
		return input, ErrCreativeAssetTooLarge
	}
	declaredMIME := strings.ToLower(strings.TrimSpace(input.Mime))
	mime := declaredMIME
	switch mime {
	case "image/png", "image/jpeg", "image/jpg", "image/webp":
	default:
		// Sniff bytes when the declared MIME type is missing or unsupported.
		mime = SniffCreativeImageMime(input.Bytes)
		if mime == "" {
			return input, ErrCreativeInvalidMime
		}
	}
	if mime == "image/jpg" {
		mime = "image/jpeg"
	}
	// Check file signatures even for accepted MIME headers so mislabeled content is rejected.
	detectedMIME := SniffCreativeImageMime(input.Bytes)
	if detectedMIME == "" || detectedMIME != mime {
		return input, ErrCreativeInvalidMime
	}
	return CreativeInputImage{Bytes: input.Bytes, Mime: mime}, nil
}
func SniffCreativeImageMime(data []byte) string { return upstream.SniffImageMIME(data) }

// CreativeImageDimensions reads image dimensions, using x/image/webp for WebP.
func CreativeImageDimensions(data []byte, mime string) (int, int, error) {
	if mime == "image/webp" {
		cfg, err := webp.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return 0, 0, err
		}
		return cfg.Width, cfg.Height, nil
	}
	switch mime {
	case "image/png", "image/jpeg":
		// Blank imports register the JPEG and PNG decoders with image.DecodeConfig.
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return 0, 0, err
		}
		return cfg.Width, cfg.Height, nil
	default:
		return 0, 0, fmt.Errorf("unsupported image mime %s", mime)
	}
}

// BuildCreativeRequestFingerprint returns the SHA-256 hash of the payload's fixed-order JSON.
func BuildCreativeRequestFingerprint(payload CreativeFingerprintPayload) string {
	body, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return Sha256Hex(body)
}

func CreativeImageHashes(sources []CreativeInputImage) []string {
	hashes := make([]string, 0, len(sources))
	for i := range sources {
		hashes = append(hashes, Sha256Hex(sources[i].Bytes))
	}
	return hashes
}

func CreativeImageHash(image *CreativeInputImage) string {
	if image == nil {
		return ""
	}
	return Sha256Hex(image.Bytes)
}

func Sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ModerateCreativeRequest submits the prompt and images using the OpenAI Images moderation format.
// NoMediaRetention prevents moderation from retaining media snapshots or body excerpts.
func (s *Public) ModerateCreativeRequest(ctx context.Context, userID int64, validated *ValidatedCreativeParams) error {
	if s.Moderation == nil {
		return nil
	}
	images := make([]map[string]string, 0, len(validated.Sources)+1)
	for i := range validated.Sources {
		images = append(images, map[string]string{
			"image_url": "data:" + validated.Sources[i].Mime + ";base64," + base64.StdEncoding.EncodeToString(validated.Sources[i].Bytes),
		})
	}
	if validated.Mask != nil {
		images = append(images, map[string]string{
			"image_url": "data:" + validated.Mask.Mime + ";base64," + base64.StdEncoding.EncodeToString(validated.Mask.Bytes),
		})
	}
	payload := map[string]any{
		"model":  validated.Model,
		"prompt": validated.Prompt,
		"images": images,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	requestID := telemetry.NewRequestID()
	if s.RequestID != nil {
		if clientRequestID := strings.TrimSpace(s.RequestID(ctx)); clientRequestID != "" {
			requestID = clientRequestID
		}
	}
	decision, err := s.Moderation.Check(ctx, ModerationInput{
		RequestID:        requestID,
		UserID:           userID,
		BillingUserID:    userID,
		GroupID:          &validated.Group.ID,
		GroupName:        validated.Group.Name,
		Endpoint:         "/v1/creative/runs",
		Platform:         validated.Platform,
		Model:            validated.Model,
		Protocol:         "openai_images",
		Body:             body,
		NoMediaRetention: true,
	})
	if err != nil {
		// Moderation service errors fail open; a successful denial below still blocks the request.
		s.warn("creative.moderation_check_failed",
			"user_id", userID,
			"error", err,
		)
		return nil
	}
	if decision != nil && !decision.Allowed {
		return ErrCreativeContentBlocked
	}
	return nil
}

// ResolveCreativePricing calculates per-image pricing and subscription/balance multipliers for the funding
// snapshot.
func (s *Public) ResolveCreativePricing(ctx context.Context, userID int64, validated *ValidatedCreativeParams) (*CreativePricingSnapshot, error) {
	group := validated.Group
	groupDefault := group.RateMultiplier
	if groupDefault < 0 {
		groupDefault = 0
	}
	subscriptionRate := groupDefault
	balanceRate := groupDefault
	if s.UserGroupRateRepo != nil {
		if userRate, err := s.UserGroupRateRepo.GetByUserAndGroup(ctx, userID, group.ID); err == nil && userRate != nil {
			balanceRate = *userRate
		}
	}
	effective := balanceRate
	planGroupRateEnabled := true
	if s.SubscriptionMultiplier != nil {
		if value, ok := s.SubscriptionMultiplier(ctx, userID, group, groupDefault); ok {
			effective = value
		}
	}
	baseUnitPrice := 0.0
	estimatedCost := 0.0
	pricingModel := s.BillingModel(ctx, group, validated.Model, validated.FinalModel)
	if resolvedUnitPrice, ok := s.ImageUnitPrice(ctx, group, pricingModel, validated.ImageSize); ok {
		baseUnitPrice = resolvedUnitPrice
		estimatedCost = resolvedUnitPrice * float64(validated.OutputCount) * effective
	} else {
		return nil, billing.ErrImageTaskPricingMissing
	}
	return &CreativePricingSnapshot{
		BaseUnitPrice:              baseUnitPrice,
		SubscriptionRateMultiplier: subscriptionRate,
		BalanceRateMultiplier:      balanceRate,
		PlanGroupRateEnabled:       planGroupRateEnabled,
		EstimatedCost:              estimatedCost,
	}, nil
}

// BillingModel uses the shared pricing configuration's model selection when linked, otherwise the upstream
// model.
func (s *Public) BillingModel(ctx context.Context, group *GroupView, requested, upstream string) string {
	if upstream == "" {
		upstream = requested
	}
	if s.GroupMapping == nil || group == nil {
		return upstream
	}
	mapping := s.GroupMapping(ctx, group.ID, requested)
	if mapping.PricingConfigID == 0 {
		return upstream
	}
	mapped := mapping.MappedModel
	if mapped == "" {
		mapped = requested
	}
	return routing.BillingModelForPrice(mapping, requested, mapped, upstream)
}

func intersectCreativeOperations(left, right []string) []string {
	var result []string
	for _, value := range left {
		if CreativeContainsOption(right, value) {
			result = append(result, value)
		}
	}
	return result
}
