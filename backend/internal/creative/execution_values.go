package creative

import "github.com/TokenFlux/TokenRouter/internal/upstream"

const CreativeMaxOutputBytes = 32 << 20

func CreativePlatformImageModel(platform, model string) bool {
	switch platform {
	case PlatformOpenAI:
		return upstream.IsGPTImageGenerationModel(model)
	case PlatformGrok:
		return upstream.IsGrokImageGenerationModel(model)
	case PlatformGemini:
		return IsCreativeGeminiImageModel(model)
	default:
		return false
	}
}

// NormalizeCreativeOutputs rejects oversized images, drops empty and duplicate bytes,
// and returns the first usable image with index zero.
func NormalizeCreativeOutputs(outputs []CreativeOutput) ([]CreativeOutput, error) {
	if len(outputs) == 0 {
		return nil, CreativeNonRetryableError("platform returned no image output")
	}
	seen := make(map[string]struct{}, len(outputs))
	out := make([]CreativeOutput, 0, len(outputs))
	for _, output := range outputs {
		if len(output.Bytes) == 0 {
			continue
		}
		if len(output.Bytes) > CreativeMaxOutputBytes {
			return nil, CreativeNonRetryableError("creative output %d exceeds size limit %d bytes", output.Index, CreativeMaxOutputBytes)
		}
		sum := Sha256Hex(output.Bytes)
		if _, ok := seen[sum]; ok {
			continue
		}
		seen[sum] = struct{}{}
		out = append(out, output)
	}
	if len(out) == 0 {
		return nil, CreativeNonRetryableError("platform returned no usable image output")
	}
	if len(out) > 1 {
		out = out[:1]
	}
	for index := range out {
		out[index].Index = index
	}
	return out, nil
}
