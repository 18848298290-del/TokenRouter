package creative

import (
	"slices"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// operation_protocol.go maps creative operations to protocol gates and derives
// each platform's allowed operations.
// Creative operations use the image protocol gates of their selected platform.
// Gemini generation and editing share GenerateContent, while OpenAI and Grok
// separate generation from edits; explicit group permissions filter the
// implemented operations through those gates.

// OperationProtocol selects the client protocol used for creative group
// permission checks. Gemini always uses GenerateContent; every non-Gemini
// operation other than generate maps to ImagesEdits, so callers validate
// operation names separately.
func OperationProtocol(platform, operation string) protocol.ProtocolID {
	if platform == PlatformGemini {
		return protocol.ProtocolGeminiGenerateContent
	}
	if operation == CreativeOperationGenerate {
		return protocol.ProtocolImagesGenerations
	}
	return protocol.ProtocolImagesEdits
}

// OperationsForGroup returns a fresh platform map of implemented creative
// operations. When explicit is true, callers supply a nonnil allows callback
// and operations failing the corresponding protocol gate are removed.
func OperationsForGroup(explicit bool, allows func(protocol.ProtocolID) bool) map[string][]string {
	out := make(map[string][]string)
	for _, platform := range []string{PlatformOpenAI, PlatformGemini, PlatformGrok} {
		operations := CreativeOperationsForPlatform(platform)
		if explicit {
			operations = slices.DeleteFunc(operations, func(operation string) bool { return !allows(OperationProtocol(platform, operation)) })
		}
		out[platform] = operations
	}
	return out
}
