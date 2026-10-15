package pricing

import (
	"strconv"
	"strings"
)

// image_billing_size.go selects image billing tiers while retaining input and
// output size evidence.
// An upstream response may report several image dimensions that differ from the
// requested size. Billing uses the highest recognized output tier for the
// request, while display retains the first nonempty output spelling so recorded
// evidence does not become a synthesized billing label.
//
// ResolveImageBillingSize counts recognized outputs by tier, then selects
// output evidence before a recognized input hint and finally the 2K default.
// Unrecognized output text can remain visible even when input or default
// supplies the billing tier. Dimension classification uses the longest pixel
// edge, retains separate 1K, 2K, and 4K labels, and treats empty or auto input
// as unresolved.

const (
	// These normalized image tier labels are shared by dimension classification,
	// catalog size lookup, and recorded billing size evidence.
	ImageBillingSize1K = "1K" // longest edge at most 1024 pixels
	ImageBillingSize2K = "2K" // longest edge above 1024 through 2048 pixels
	ImageBillingSize4K = "4K" // longest edge above 2048 pixels

	// These evidence labels identify output selection, input selection, the
	// synthesized default, and a reserved legacy-source spelling for size records.
	ImageSizeSourceOutput  = "output"  // recognized output dimension evidence
	ImageSizeSourceInput   = "input"   // recognized supplied input evidence
	ImageSizeSourceDefault = "default" // synthesized 2K billing evidence
	ImageSizeSourceLegacy  = "legacy"  // reserved legacy size-evidence label
)

// ImageBillingSizeResolution contains a normalized billing tier and the size
// evidence used to select it.
type ImageBillingSizeResolution struct {
	// BillingSize is the highest recognized output tier, else a recognized input
	// tier, else 2K. Completion records use this one label for request pricing.
	BillingSize string // selected normalized billing tier
	InputSize   string // trimmed supplied input-size evidence
	// OutputSize retains the first nonempty output spelling, including unresolved
	// text. It can describe a different size from the selected highest billing
	// tier.
	OutputSize string // first nonempty output-size evidence
	Source     string // output, input, or default evidence
	// Breakdown contains positive counts of recognized output tiers in a new map.
	// Input or default selection leaves it nil because output supplied no
	// recognized tiers.
	Breakdown map[string]int // recognized output-tier count map
}

// ClassifyImageBillingTier returns a recognized billing tier and its presence
// for a named tier or positive pixel dimensions. It trims and folds case;
// empty, auto, malformed, nonpositive, and integer-overflow dimensions return
// false so callers can distinguish unresolved evidence from a selected tier.
//
// Pixel dimensions use the longest edge, so a rectangular output can move to
// the next tier even when its shorter edge is within the lower range. Edges up
// to 1024 use 1K, up to 2048 use 2K, and every larger edge uses 4K. Callers that
// need a billing key for unresolved text apply a default separately.
func ClassifyImageBillingTier(size string) (string, bool) {
	trimmed := strings.TrimSpace(size)
	normalized := strings.ToLower(trimmed)
	switch normalized {
	case "", "auto":
		return "", false
	case "1k":
		return ImageBillingSize1K, true
	case "2k":
		return ImageBillingSize2K, true
	case "4k":
		return ImageBillingSize4K, true
	case "2048x2048", "2048x1152":
		return ImageBillingSize2K, true
	case "3840x2160", "2160x3840":
		return ImageBillingSize4K, true
	}

	width, height, ok := ParseImageBillingDimensions(trimmed)
	if !ok {
		return "", false
	}
	// The longest edge selects a billing tier across square and rectangular pixel
	// dimensions.
	maxEdge := max(height, width)
	switch {
	case maxEdge <= 1024:
		return ImageBillingSize1K, true
	case maxEdge <= 2048:
		return ImageBillingSize2K, true
	default:
		return ImageBillingSize4K, true
	}
}

// NormalizeImageBillingTierOrDefault returns a recognized tier or the 2K
// billing default. Empty, auto, and unparseable values all use that default, so
// callers retain raw size evidence separately when recording how the tier was
// chosen.
func NormalizeImageBillingTierOrDefault(size string) string {
	// Unresolved size text still needs one billing key; retain the shared 2K
	// default used by image catalog lookup.
	if tier, ok := ClassifyImageBillingTier(size); ok {
		return tier
	}
	return ImageBillingSize2K
}

// ResolveImageBillingSize returns one billing tier with input, output, and
// source evidence. It trims input and compacts output spellings while retaining
// duplicate reports. Recognized outputs contribute per-tier counts, and the
// highest output tier takes precedence over the input hint.
//
// OutputSize retains the first nonempty report, even when that report is
// unrecognized or differs from the selected highest tier. With no recognized
// output, a recognized input supplies the tier; otherwise 2K is used. Breakdown
// is a newly allocated positive-count map only when output supplied recognized
// tiers.
func ResolveImageBillingSize(inputSize string, outputSizes []string) ImageBillingSizeResolution {
	inputSize = strings.TrimSpace(inputSize)
	outputSizes = CompactTrimmedStrings(outputSizes)

	breakdown := map[string]int{}
	outputSize := FirstDisplayImageOutputSize(outputSizes)
	outputTier := ""
	for _, output := range outputSizes {
		tier, ok := ClassifyImageBillingTier(output)
		if !ok {
			continue
		}
		breakdown[tier]++
		if ImageTierRank(tier) > ImageTierRank(outputTier) {
			outputTier = tier
		}
	}
	if outputTier != "" {
		return ImageBillingSizeResolution{
			BillingSize: outputTier,
			InputSize:   inputSize,
			OutputSize:  outputSize,
			Source:      ImageSizeSourceOutput,
			Breakdown:   NormalizeImageSizeBreakdown(breakdown),
		}
	}

	// No recognized output supplies a tier, so use the caller's recognized size
	// hint while retaining raw output display evidence.
	if tier, ok := ClassifyImageBillingTier(inputSize); ok {
		return ImageBillingSizeResolution{
			BillingSize: tier,
			InputSize:   inputSize,
			OutputSize:  outputSize,
			Source:      ImageSizeSourceInput,
		}
	}

	// Neither output nor input supplies a recognized tier; select 2K and mark that
	// the billing size came from the default.
	return ImageBillingSizeResolution{
		BillingSize: ImageBillingSize2K,
		InputSize:   inputSize,
		OutputSize:  outputSize,
		Source:      ImageSizeSourceDefault,
	}
}

// ParseImageBillingDimensions returns positive machine-sized width and height
// from WxH text. Case and surrounding component whitespace are accepted.
// Malformed separators, integer parsing failures including overflow, and
// nonpositive dimensions return false.
func ParseImageBillingDimensions(size string) (int, int, bool) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(size)), "x")
	if len(parts) != 2 {
		return 0, 0, false
	}
	// Reject either integer conversion failure as unresolved size evidence,
	// allowing ResolveImageBillingSize to try the next source while discarding a
	// partial width or height.
	width, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, false
	}
	height, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, false
	}
	if width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

// CompactTrimmedStrings returns a newly allocated slice of nonempty trimmed
// values. Input order and duplicate values are retained because repeated output
// reports contribute repeated tier counts. Nil or empty input returns nil.
func CompactTrimmedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// FirstDisplayImageOutputSize returns the first nonempty trimmed output size
// string. It retains that report's spelling and accepts unrecognized text,
// leaving billing classification to a separate operation. No such report
// returns an empty string.
func FirstDisplayImageOutputSize(outputSizes []string) string {
	for _, output := range outputSizes {
		if trimmed := strings.TrimSpace(output); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// ImageTierRank returns the ordered 1K, 2K, or 4K tier rank. Case and
// surrounding whitespace are ignored. Unknown values return zero, allowing
// recognized output tiers to outrank unresolved size evidence.
func ImageTierRank(tier string) int {
	switch strings.ToUpper(strings.TrimSpace(tier)) {
	case ImageBillingSize1K:
		return 1
	case ImageBillingSize2K:
		return 2
	case ImageBillingSize4K:
		return 3
	default:
		return 0
	}
}

// NormalizeImageSizeBreakdown copies positive counts under exact 1K, 2K, and 4K
// keys. Unknown keys and nonpositive counts are omitted. Nil, empty, or
// entirely omitted input returns nil; retained counts occupy a new map.
func NormalizeImageSizeBreakdown(in map[string]int) map[string]int {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int, len(in))
	for _, tier := range []string{ImageBillingSize1K, ImageBillingSize2K, ImageBillingSize4K} {
		if count := in[tier]; count > 0 {
			out[tier] = count
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
