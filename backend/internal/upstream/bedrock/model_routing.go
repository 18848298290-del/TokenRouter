package bedrock

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// model_routing.go resolves Bedrock model aliases and inference profiles
// against source-region rules.
// Bedrock source regions identify both the HTTP endpoint and signing region,
// while inference-profile prefixes identify where model execution can route.
// The static per-model rules record exact profile IDs and source-region
// membership, so geography is selected from those entries instead of inferred
// from an AWS region prefix.
//
// Default Anthropic aliases resolve through the same source-region rules as
// configured vendor IDs. An explicit bare ID can retain verified single-region
// execution; otherwise geographic or forced-global profiles are selected for
// that model. Unknown explicit vendor IDs and ARNs pass to upstream
// validation. Missing recorded evidence remains distinct from recorded lack of
// support, allowing administrator diagnostics to explain the failed route
// without exposing those details through the error label.

const (
	// BedrockRoutingFailure values distinguish invalid model identity, recorded
	// unsupported routing, and combinations lacking sufficient source-region
	// evidence for a selected profile.
	BedrockRoutingInvalidModel      BedrockRoutingFailure = "invalid_model"      // unresolved model identity
	BedrockRoutingUnsupportedRegion BedrockRoutingFailure = "unsupported_region" // recorded unsupported source routing
	BedrockRoutingUnverifiedRegion  BedrockRoutingFailure = "unverified_region"  // unverified source routing evidence

	// Source-region string groups supply shared membership lists for the static
	// model entries. Reuse a list when those entries record the same exact source
	// set.
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

// BedrockModelRoute pairs the selected inference ID with its source endpoint
// region.
type BedrockModelRoute struct {
	ModelID      string // selected vendor inference identifier
	SourceRegion string // endpoint and signing source region
}

// BedrockRoutingFailure identifies the reason static Bedrock model routing
// cannot select a target.
type BedrockRoutingFailure string

// BedrockModelRoutingError retains administrator route details beside its
// public failure category.
type BedrockModelRoutingError struct {
	Reason       BedrockRoutingFailure // static route failure category
	ModelID      string                // model identity under resolution
	SourceRegion string                // source region under resolution
	ForceGlobal  bool                  // requested global inference selection
	// GlobalAvailable records the selected source region membership in the global
	// profile. Administrator diagnostics offer a global-selection hint only when
	// global inference was not already requested.
	GlobalAvailable bool // recorded global source eligibility
}

// BedrockInferenceProfile pairs an exact inference identifier with its
// recorded source regions.
type BedrockInferenceProfile struct {
	// Id and SourceRegions jointly determine Supports. An empty Id makes the
	// profile unavailable; otherwise exact source membership selects the recorded
	// inference identity.
	Id            string   // exact vendor inference identifier
	SourceRegions []string // recorded eligible source regions
}

// BedrockModelRegionRule contains model-specific source evidence and its
// available inference choices.
type BedrockModelRegionRule struct {
	SourceURL string // model rule evidence location
	// GeoProfiles and GlobalProfile provide exact selectable IDs and source sets.
	// InRegionSources permits explicit bare IDs; DocumentedRegions and
	// UnverifiedGeoRegions distinguish unsupported choices from missing evidence.
	GeoProfiles          []BedrockInferenceProfile // ordered geographic profile choices
	GlobalProfile        BedrockInferenceProfile   // recorded global inference profile
	InRegionSources      []string                  // explicit bare-model source regions
	DocumentedRegions    []string                  // recorded model source-region coverage
	UnverifiedGeoRegions []string                  // documented regions lacking geographic IDs
}

// RouteInput supplies the already mapped model, source region, and inference
// mode.
type RouteInput struct {
	Region      string // configured signing source region
	ForceGlobal bool   // forced global profile selection
	Model       string // already mapped provider model
}

// Error returns the Bedrock routing category as a label for caller-facing
// failure handling.
func (e *BedrockModelRoutingError) Error() string {
	return "bedrock model routing failed: " + string(e.Reason)
}

// BedrockRoutingDiagnostic formats model, source region, and inference mode
// for administrator diagnostics. Callers provide a nonnil error; other error
// types use their own text, while eligible geographic failures can include a
// global-mode hint.
func BedrockRoutingDiagnostic(err error) string {
	var failure *BedrockModelRoutingError
	if !errors.As(err, &failure) {
		return err.Error()
	}
	mode := "geographic inference"
	if failure.ForceGlobal {
		mode = "global inference"
	}
	var message string
	switch failure.Reason {
	case BedrockRoutingInvalidModel:
		message = fmt.Sprintf("could not resolve Bedrock model %q", failure.ModelID)
	case BedrockRoutingUnsupportedRegion:
		message = fmt.Sprintf("Bedrock model %q in source region %q does not support %s", failure.ModelID, failure.SourceRegion, mode)
	default:
		message = fmt.Sprintf("Bedrock model %q in source region %q has no verified %s ID; check the AWS inference configuration", failure.ModelID, failure.SourceRegion, mode)
	}
	if !failure.ForceGlobal && failure.GlobalAvailable {
		message += "; this source region supports global inference for this model; enable Force Global to use it"
	}
	return message
}

// ResolveBedrockModelRoute selects an inference ID using provider.Model after
// caller model mapping and the resolved source region. requestedModel supplies
// diagnostic identity for an absent provider; a nonnil provider uses its
// configured Model value.
//
// Default aliases enter the same model-specific rule lookup as vendor IDs.
// Explicit bare IDs retain a recorded single-region route when permitted,
// while forced-global mode selects only its eligible global profile and
// ordinary mode searches geographic profiles in order. Unknown explicit vendor
// IDs and ARNs pass through. Failure classification separates invalid
// identity, absent evidence, and recorded unsupported routing; SourceRegion
// remains available even when selection fails.
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
			// Keep an explicit vendor ID or ARN for upstream validation when the static
			// model table has no entry; default aliases still require a recorded route.
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
	// Permit a recorded single-region source only for an explicit bare model ID;
	// a default alias still selects an inference profile for the same base model.
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
	// Use the evidence sets to distinguish a missing exact profile mapping from a
	// recorded unsupported choice, so administrator diagnostics do not equate
	// unknown routing with vendor rejection.
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

// BedrockBaseModelID removes the first recognized case-sensitive inference
// prefix and keeps date/version suffixes. Unmatched model text passes through
// as supplied; callers trim and resolve aliases before invoking this helper.
func BedrockBaseModelID(modelID string) string {
	for _, prefix := range BedrockCrossRegionPrefixes {
		if after, ok := strings.CutPrefix(modelID, prefix); ok {
			return after
		}
	}
	return modelID
}

// Supports reports whether a nonempty profile ID records region in
// SourceRegions. Comparison is exact and case-sensitive; an empty ID is
// unavailable even when its source slice contains that region.
func (p BedrockInferenceProfile) Supports(region string) bool {
	return p.Id != "" && slices.Contains(p.SourceRegions, region)
}

// BedrockProfile constructs a static profile from the supplied exact ID and
// whitespace-separated source regions. Source strings become a fresh slice,
// while the ID spelling remains as supplied.
func BedrockProfile(id, sourceRegions string) BedrockInferenceProfile {
	return BedrockInferenceProfile{Id: id, SourceRegions: strings.Fields(sourceRegions)}
}
