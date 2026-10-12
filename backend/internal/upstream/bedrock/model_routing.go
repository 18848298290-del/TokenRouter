package bedrock

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

const (
	BedrockRoutingInvalidModel      BedrockRoutingFailure = "invalid_model"
	BedrockRoutingUnsupportedRegion BedrockRoutingFailure = "unsupported_region"
	BedrockRoutingUnverifiedRegion  BedrockRoutingFailure = "unverified_region"

	// Reuse these source-region lists only for models whose verified region sets match.
	BedrockUSSources         = "us-east-1 us-east-2 us-west-1 us-west-2 ca-central-1 ca-west-1"
	BedrockEUSources         = "eu-central-1 eu-central-2 eu-north-1 eu-south-1 eu-south-2 eu-west-1 eu-west-2 eu-west-3"
	BedrockJPSources         = "ap-northeast-1 ap-northeast-3"
	BedrockAUSources         = "ap-southeast-2 ap-southeast-4"
	BedrockAUNZSources       = "ap-southeast-2 ap-southeast-4 ap-southeast-6"
	BedrockGovCloudSources   = "us-gov-east-1 us-gov-west-1"
	BedrockCommercialSources = "us-east-1 us-east-2 us-west-1 us-west-2 ca-central-1 ca-west-1 eu-central-1 eu-central-2 eu-north-1 eu-south-1 eu-south-2 eu-west-1 eu-west-2 eu-west-3 ap-east-2 ap-northeast-1 ap-northeast-2 ap-northeast-3 ap-south-1 ap-south-2 ap-southeast-1 ap-southeast-2 ap-southeast-3 ap-southeast-4 ap-southeast-5 ap-southeast-6 ap-southeast-7 il-central-1 me-central-1 me-south-1 af-south-1 sa-east-1 mx-central-1"
)

var (
	// BedrockModelRegionRules records the AWS model-page review dated 2026-09-09.
	// Entries lacking an exact inference ID and source-region mapping remain unverified.
	// SourceURL identifies each model's evidence; updates must check geographic and global sources.
	BedrockModelRegionRules = map[string]BedrockModelRegionRule{
		"anthropic.claude-opus-4-7": {
			SourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-opus-4-7.html",
			GeoProfiles: []BedrockInferenceProfile{
				BedrockProfile("us.anthropic.claude-opus-4-7", BedrockUSSources),
				BedrockProfile("eu.anthropic.claude-opus-4-7", BedrockEUSources),
				BedrockProfile("jp.anthropic.claude-opus-4-7", BedrockJPSources),
				BedrockProfile("au.anthropic.claude-opus-4-7", BedrockAUSources),
			},
			GlobalProfile:     BedrockProfile("global.anthropic.claude-opus-4-7", BedrockCommercialSources),
			DocumentedRegions: strings.Fields(BedrockCommercialSources),
		},
		"anthropic.claude-opus-4-8": {
			SourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-opus-4-8.html",
			GeoProfiles: []BedrockInferenceProfile{
				BedrockProfile("us.anthropic.claude-opus-4-8", BedrockUSSources),
				BedrockProfile("eu.anthropic.claude-opus-4-8", BedrockEUSources),
				BedrockProfile("jp.anthropic.claude-opus-4-8", BedrockJPSources),
				BedrockProfile("au.anthropic.claude-opus-4-8", BedrockAUSources),
			},
			GlobalProfile:        BedrockProfile("global.anthropic.claude-opus-4-8", BedrockCommercialSources),
			DocumentedRegions:    strings.Fields(BedrockCommercialSources + " " + BedrockGovCloudSources),
			UnverifiedGeoRegions: strings.Fields(BedrockGovCloudSources),
		},
		"anthropic.claude-opus-5": {
			SourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-opus-5.html",
			GeoProfiles: []BedrockInferenceProfile{
				BedrockProfile("us.anthropic.claude-opus-5", BedrockUSSources),
				BedrockProfile("eu.anthropic.claude-opus-5", BedrockEUSources),
				BedrockProfile("au.anthropic.claude-opus-5", BedrockAUSources),
			},
			GlobalProfile:        BedrockProfile("global.anthropic.claude-opus-5", BedrockCommercialSources),
			DocumentedRegions:    strings.Fields(BedrockCommercialSources + " " + BedrockGovCloudSources),
			UnverifiedGeoRegions: strings.Fields(BedrockGovCloudSources),
		},
		"anthropic.claude-sonnet-5": {
			SourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-sonnet-5.html",
			GeoProfiles: []BedrockInferenceProfile{
				BedrockProfile("us.anthropic.claude-sonnet-5", BedrockUSSources),
				BedrockProfile("eu.anthropic.claude-sonnet-5", BedrockEUSources),
				BedrockProfile("au.anthropic.claude-sonnet-5", BedrockAUSources),
			},
			GlobalProfile:        BedrockProfile("global.anthropic.claude-sonnet-5", BedrockCommercialSources),
			DocumentedRegions:    strings.Fields(BedrockCommercialSources + " " + BedrockGovCloudSources),
			UnverifiedGeoRegions: strings.Fields(BedrockGovCloudSources),
		},
		"anthropic.claude-fable-5": {
			SourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-fable-5.html",
			GeoProfiles: []BedrockInferenceProfile{
				BedrockProfile("us.anthropic.claude-fable-5", BedrockUSSources),
			},
			GlobalProfile:     BedrockProfile("global.anthropic.claude-fable-5", BedrockCommercialSources),
			DocumentedRegions: strings.Fields(BedrockCommercialSources),
		},
		"anthropic.claude-fable-5-1": {
			SourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-fable-5-1.html",
			GeoProfiles: []BedrockInferenceProfile{
				BedrockProfile("us.anthropic.claude-fable-5-1", BedrockUSSources),
			},
			GlobalProfile:        BedrockProfile("global.anthropic.claude-fable-5-1", BedrockCommercialSources),
			DocumentedRegions:    strings.Fields(BedrockCommercialSources + " " + BedrockGovCloudSources),
			UnverifiedGeoRegions: strings.Fields(BedrockGovCloudSources),
		},
		"anthropic.claude-opus-4-6-v1": {
			SourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-opus-4-6.html",
			GeoProfiles: []BedrockInferenceProfile{
				BedrockProfile("us.anthropic.claude-opus-4-6-v1", BedrockUSSources),
				BedrockProfile("eu.anthropic.claude-opus-4-6-v1", BedrockEUSources),
				BedrockProfile("au.anthropic.claude-opus-4-6-v1", BedrockAUNZSources),
			},
			GlobalProfile:     BedrockProfile("global.anthropic.claude-opus-4-6-v1", BedrockCommercialSources),
			InRegionSources:   []string{"eu-west-2"},
			DocumentedRegions: strings.Fields(BedrockCommercialSources),
		},
		"anthropic.claude-sonnet-4-6": {
			SourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-sonnet-4-6.html",
			GeoProfiles: []BedrockInferenceProfile{
				BedrockProfile("us.anthropic.claude-sonnet-4-6", BedrockUSSources),
				BedrockProfile("eu.anthropic.claude-sonnet-4-6", BedrockEUSources),
				BedrockProfile("au.anthropic.claude-sonnet-4-6", BedrockAUNZSources),
				BedrockProfile("jp.anthropic.claude-sonnet-4-6", BedrockJPSources),
			},
			GlobalProfile:     BedrockProfile("global.anthropic.claude-sonnet-4-6", BedrockCommercialSources),
			InRegionSources:   []string{"eu-west-2"},
			DocumentedRegions: strings.Fields(BedrockCommercialSources),
		},
		"anthropic.claude-opus-4-5-20251101-v1:0": {
			SourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-opus-4-5.html",
			GeoProfiles: []BedrockInferenceProfile{
				BedrockProfile("us.anthropic.claude-opus-4-5-20251101-v1:0", "us-east-1 us-east-2 us-west-1 us-west-2 ca-central-1"),
				BedrockProfile("eu.anthropic.claude-opus-4-5-20251101-v1:0", BedrockEUSources),
			},
			GlobalProfile:     BedrockProfile("global.anthropic.claude-opus-4-5-20251101-v1:0", BedrockCommercialSources),
			DocumentedRegions: strings.Fields(BedrockCommercialSources),
		},
		"anthropic.claude-sonnet-4-5-20250929-v1:0": {
			SourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-sonnet-4-5.html",
			GeoProfiles: []BedrockInferenceProfile{
				BedrockProfile("us.anthropic.claude-sonnet-4-5-20250929-v1:0", "us-east-1 us-east-2 us-west-1 us-west-2 us-gov-east-1 us-gov-west-1 ca-central-1"),
				BedrockProfile("eu.anthropic.claude-sonnet-4-5-20250929-v1:0", BedrockEUSources),
				BedrockProfile("au.anthropic.claude-sonnet-4-5-20250929-v1:0", BedrockAUNZSources),
				BedrockProfile("jp.anthropic.claude-sonnet-4-5-20250929-v1:0", BedrockJPSources),
			},
			GlobalProfile:     BedrockProfile("global.anthropic.claude-sonnet-4-5-20250929-v1:0", BedrockCommercialSources),
			DocumentedRegions: strings.Fields(BedrockCommercialSources + " " + BedrockGovCloudSources),
		},
		"anthropic.claude-haiku-4-5-20251001-v1:0": {
			SourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-haiku-4-5.html",
			GeoProfiles: []BedrockInferenceProfile{
				BedrockProfile("us.anthropic.claude-haiku-4-5-20251001-v1:0", "us-east-1 us-east-2 us-west-1 us-west-2 ca-central-1"),
				BedrockProfile("eu.anthropic.claude-haiku-4-5-20251001-v1:0", BedrockEUSources),
				BedrockProfile("au.anthropic.claude-haiku-4-5-20251001-v1:0", BedrockAUNZSources),
				BedrockProfile("jp.anthropic.claude-haiku-4-5-20251001-v1:0", BedrockJPSources),
			},
			GlobalProfile:     BedrockProfile("global.anthropic.claude-haiku-4-5-20251001-v1:0", BedrockCommercialSources),
			DocumentedRegions: strings.Fields(BedrockCommercialSources),
		},
		"anthropic.claude-opus-4-1-20250805-v1:0": {
			SourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-opus-4-1.html",
			GeoProfiles: []BedrockInferenceProfile{
				BedrockProfile("us.anthropic.claude-opus-4-1-20250805-v1:0", "us-east-1 us-east-2 us-west-2"),
			},
			DocumentedRegions: strings.Fields("us-east-1 us-east-2 us-west-2"),
		},
		"anthropic.claude-sonnet-4-20250514-v1:0": {
			SourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-sonnet-4.html",
			GeoProfiles: []BedrockInferenceProfile{
				BedrockProfile("us.anthropic.claude-sonnet-4-20250514-v1:0", "us-east-1 us-east-2 us-west-1 us-west-2"),
				BedrockProfile("eu.anthropic.claude-sonnet-4-20250514-v1:0", "eu-central-1 eu-north-1 eu-south-1 eu-south-2 eu-west-1 eu-west-3 il-central-1"),
				BedrockProfile("apac.anthropic.claude-sonnet-4-20250514-v1:0", "ap-northeast-1 ap-northeast-2 ap-northeast-3 ap-south-1 ap-south-2 ap-southeast-1 ap-southeast-2"),
			},
			GlobalProfile:        BedrockProfile("global.anthropic.claude-sonnet-4-20250514-v1:0", "us-east-1 us-east-2 us-west-2 eu-west-1 ap-northeast-1"),
			DocumentedRegions:    strings.Fields("us-east-1 us-east-2 us-west-1 us-west-2 eu-central-1 eu-north-1 eu-south-1 eu-south-2 eu-west-1 eu-west-3 ap-east-2 ap-northeast-1 ap-northeast-2 ap-northeast-3 ap-south-1 ap-south-2 ap-southeast-1 ap-southeast-2 ap-southeast-3 ap-southeast-4 ap-southeast-5 ap-southeast-7 il-central-1"),
			UnverifiedGeoRegions: strings.Fields("ap-east-2 ap-southeast-3 ap-southeast-4 ap-southeast-5 ap-southeast-7"),
		},
		// The legacy overview does not establish exact Opus 4 source regions; keep this entry unverified.
		"anthropic.claude-opus-4-20250514-v1:0": {
			SourceURL: "https://platform.claude.com/docs/en/build-with-claude/claude-on-amazon-bedrock-legacy",
		},
	}

	// DefaultBedrockModelMapping maps Anthropic names to default Bedrock model IDs.
	// ResolveBedrockModelRoute selects the final ID for the source region and inference mode.
	DefaultBedrockModelMapping = map[string]string{
		// Claude Fable
		"claude-fable-5-1": "anthropic.claude-fable-5-1",
		"claude-fable-5":   "anthropic.claude-fable-5",
		// Claude Opus
		// Opus 5, 4.8, and 4.7 use Bedrock IDs without version suffixes.
		"claude-opus-5":            "us.anthropic.claude-opus-5",
		"claude-opus-4-8":          "us.anthropic.claude-opus-4-8",
		"claude-opus-4-7":          "us.anthropic.claude-opus-4-7",
		"claude-opus-4-6-thinking": "us.anthropic.claude-opus-4-6-v1",
		"claude-opus-4-6":          "us.anthropic.claude-opus-4-6-v1",
		"claude-opus-4-5-thinking": "us.anthropic.claude-opus-4-5-20251101-v1:0",
		"claude-opus-4-5-20251101": "us.anthropic.claude-opus-4-5-20251101-v1:0",
		"claude-opus-4-1":          "us.anthropic.claude-opus-4-1-20250805-v1:0",
		"claude-opus-4-20250514":   "us.anthropic.claude-opus-4-20250514-v1:0",
		// Claude Sonnet
		"claude-sonnet-5":            "us.anthropic.claude-sonnet-5",
		"claude-sonnet-4-6-thinking": "us.anthropic.claude-sonnet-4-6",
		"claude-sonnet-4-6":          "us.anthropic.claude-sonnet-4-6",
		"claude-sonnet-4-5":          "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		"claude-sonnet-4-5-thinking": "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		"claude-sonnet-4-5-20250929": "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		"claude-sonnet-4-20250514":   "us.anthropic.claude-sonnet-4-20250514-v1:0",
		// Claude Haiku
		"claude-haiku-4-5":          "us.anthropic.claude-haiku-4-5-20251001-v1:0",
		"claude-haiku-4-5-20251001": "us.anthropic.claude-haiku-4-5-20251001-v1:0",
	}
)

// BedrockModelRoute pairs the inference ID with the endpoint and signing source region.
type BedrockModelRoute struct {
	ModelID      string
	SourceRegion string
}

type BedrockRoutingFailure string

// BedrockModelRoutingError exposes a failure category through Error; diagnostics carry configuration details.
type BedrockModelRoutingError struct {
	Reason          BedrockRoutingFailure
	ModelID         string
	SourceRegion    string
	ForceGlobal     bool
	GlobalAvailable bool
}

// BedrockInferenceProfile records the source regions verified for one exact inference ID.
type BedrockInferenceProfile struct {
	Id            string
	SourceRegions []string
}

type BedrockModelRegionRule struct {
	SourceURL            string
	GeoProfiles          []BedrockInferenceProfile
	GlobalProfile        BedrockInferenceProfile
	InRegionSources      []string
	DocumentedRegions    []string
	UnverifiedGeoRegions []string
}

// RouteInput supplies the source region, inference mode, and configured model.
type RouteInput struct {
	Region      string
	ForceGlobal bool
	Model       string
}

func (e *BedrockModelRoutingError) Error() string {
	return "bedrock model routing failed: " + string(e.Reason)
}

// BedrockRoutingDiagnostic formats provider details for administrator diagnostics and internal logs.
func BedrockRoutingDiagnostic(err error) string {
	var failure *BedrockModelRoutingError
	if !errors.As(err, &failure) {
		return err.Error()
	}
	mode := "地域推理"
	if failure.ForceGlobal {
		mode = "全局推理"
	}
	var message string
	switch failure.Reason {
	case BedrockRoutingInvalidModel:
		message = fmt.Sprintf("无法解析 Bedrock 模型 %q", failure.ModelID)
	case BedrockRoutingUnsupportedRegion:
		message = fmt.Sprintf("Bedrock 模型 %q 在来源区域 %q 不支持%s", failure.ModelID, failure.SourceRegion, mode)
	default:
		message = fmt.Sprintf("尚未核实 Bedrock 模型 %q 在来源区域 %q 的%s ID，请核对 AWS 推理配置", failure.ModelID, failure.SourceRegion, mode)
	}
	if !failure.ForceGlobal && failure.GlobalAvailable {
		message += "；该来源区域支持此模型的全局推理，可开启“强制全局”"
	}
	return message
}

// ResolveBedrockModelRoute selects known inference IDs from verified source-region rules.
// @project-doc docs/interfaces/anthropic_upstream.md#bedrock_region_routing
func ResolveBedrockModelRoute(provider *RouteInput, requestedModel string) (BedrockModelRoute, error) {
	route := BedrockModelRoute{SourceRegion: BedrockRuntimeRegion(provider)}
	if provider == nil {
		return route, &BedrockModelRoutingError{Reason: BedrockRoutingInvalidModel, ModelID: requestedModel, SourceRegion: route.SourceRegion}
	}
	modelID := strings.TrimSpace(provider.Model)
	defaultID, isDefaultAlias := DefaultBedrockModelMapping[modelID]
	if isDefaultAlias {
		modelID = defaultID
	}
	baseID := BedrockBaseModelID(modelID)
	rule, knownModel := BedrockModelRegionRules[baseID]
	failure := &BedrockModelRoutingError{
		ModelID: modelID, SourceRegion: route.SourceRegion, ForceGlobal: ShouldForceBedrockGlobal(provider),
	}
	if !knownModel {
		if !isDefaultAlias && IsLikelyBedrockModelID(modelID) {
			// Preserve explicit unknown vendor IDs and ARNs for upstream validation.
			route.ModelID = modelID
			return route, nil
		}
		failure.Reason = BedrockRoutingInvalidModel
		if isDefaultAlias {
			failure.Reason = BedrockRoutingUnverifiedRegion
		}
		return route, failure
	}
	failure.GlobalAvailable = rule.GlobalProfile.Supports(route.SourceRegion)
	// Explicit base IDs retain verified single-region routing; default aliases use inference profiles.
	if !failure.ForceGlobal && !isDefaultAlias && modelID == baseID && slices.Contains(rule.InRegionSources, route.SourceRegion) {
		route.ModelID = modelID
		return route, nil
	}
	if failure.ForceGlobal {
		if failure.GlobalAvailable {
			route.ModelID = rule.GlobalProfile.Id
			return route, nil
		}
	} else {
		for _, profile := range rule.GeoProfiles {
			if profile.Supports(route.SourceRegion) {
				route.ModelID = profile.Id
				return route, nil
			}
		}
	}
	// Missing evidence is unverified, distinct from documented lack of support.
	failure.Reason = BedrockRoutingUnverifiedRegion
	if slices.Contains(rule.DocumentedRegions, route.SourceRegion) &&
		(failure.ForceGlobal || !slices.Contains(rule.UnverifiedGeoRegions, route.SourceRegion)) {
		failure.Reason = BedrockRoutingUnsupportedRegion
	}
	if failure.ForceGlobal && rule.GlobalProfile.Id == "" && len(rule.DocumentedRegions) > 0 {
		failure.Reason = BedrockRoutingUnsupportedRegion
	}
	return route, failure
}

// BedrockBaseModelID strips a recognized inference prefix, retaining date and version suffixes.
func BedrockBaseModelID(modelID string) string {
	for _, prefix := range BedrockCrossRegionPrefixes {
		if after, ok := strings.CutPrefix(modelID, prefix); ok {
			return after
		}
	}
	return modelID
}

func (p BedrockInferenceProfile) Supports(region string) bool {
	return p.Id != "" && slices.Contains(p.SourceRegions, region)
}

// BedrockProfile splits the source-region list when constructing the static rules.
func BedrockProfile(id, sourceRegions string) BedrockInferenceProfile {
	return BedrockInferenceProfile{Id: id, SourceRegions: strings.Fields(sourceRegions)}
}
