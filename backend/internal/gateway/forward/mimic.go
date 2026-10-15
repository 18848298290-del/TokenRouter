package forward

import "context"

// mimic.go applies Anthropic OAuth system, metadata, cache, and tool-name
// preparation in order. Anthropic OAuth requests can carry a rewritten system
// prefix and provider-specific metadata in the same body. The following
// normalizer needs to know whether the system rewrite path ran so it can retain
// that path's system cache annotations while still applying metadata and other
// OAuth normalization. The body passes through cache policy after those
// identity choices have been made.
//
// Tool-name rewriting can also require request-local state to reverse names in
// the response. A successful rewrite is bound to that state; when no rewrite
// is reported, the tools-last cache-breakpoint path is applied instead. Callback
// implementations own their byte-slice allocation behavior, so this orchestration
// returns their final body rather than establishing independent storage for every phase.

// MimicPorts supplies settings, body rewrites, and request-local tool restoration binding.
type MimicPorts interface {
	// SystemSettings returns system-injection permission, prompt text, and encoded
	// prompt blocks for this request. Mimic reads these together before choosing
	// whether the later OAuth normalization strips system cache controls.
	SystemSettings(context.Context) (bool, string, string)
	// RewriteMimicSystem applies configured identity text or system blocks for the
	// supplied model. Mimic retains its returned body and records that this path
	// ran, even if the callback returns unchanged bytes.
	RewriteMimicSystem([]byte, string, string, string) []byte
	// MimicMetadata returns the provider-specific metadata identity when it should
	// be injected. An empty string leaves injection disabled in NormalizeOptions;
	// fingerprint lookup and metadata-passthrough policy belong to the adapter.
	MimicMetadata(context.Context, []byte) string
	// NormalizeOAuth applies system-cache and metadata choices from NormalizeOptions
	// to the current body. Callers consume the returned bytes before applying
	// cache and tool policy, with storage ownership determined by the callback.
	NormalizeOAuth([]byte, NormalizeOptions) []byte
	// RewriteCache applies the current request's cache-control policy after OAuth
	// identity normalization. The returned body feeds tool-name handling; the
	// platform adapter owns feature defaults and actual cache annotations.
	RewriteCache(context.Context, []byte) []byte
	// RewriteTools returns a rewritten body and reports whether tool-name
	// restoration state was created. True makes Mimic retain those bytes and call
	// BindTools; false selects the tools-last cache-breakpoint path.
	RewriteTools([]byte) ([]byte, bool)
	// BindTools publishes the preceding successful rewrite's restoration state for
	// response output. Mimic invokes it after retaining the rewritten body, so
	// request and response use the same tool-name mapping.
	BindTools()
	// ToolsLast applies the tools-last cache-breakpoint rule when RewriteTools
	// reports no restoration map. Mimic uses its returned bytes as the final body,
	// leaving their allocation and sharing behavior to the adapter.
	ToolsLast([]byte) []byte
}

// Mimic applies identity, OAuth, cache, and tool policy to an eligible request
// body. Ineligible or empty bodies return the supplied slice with no port calls;
// eligible bodies require usable ports and retain each callback's returned
// bytes for the next step.
//
// System injection records that its rewrite path ran, which disables subsequent
// system-cache stripping even when the returned body is unchanged. Nonempty
// metadata enables identity injection before cache policy. A reported tool
// rewrite binds its response restoration state; otherwise tools-last policy
// supplies the final body. Slice ownership remains with the individual callback
// implementations rather than being guaranteed by this orchestration.
func Mimic(ctx context.Context, p MimicPorts, eligible bool, body []byte, model string) []byte {
	if !eligible || len(body) == 0 {
		return body
	}
	enabled, prompt, blocks := p.SystemSettings(ctx)
	rewritten := false
	// Keep the system rewrite's cache annotations by selecting normalization
	// options from whether that path ran. Byte equality cannot establish that
	// choice, because a callback can return an already suitable body.
	if enabled {
		body = p.RewriteMimicSystem(body, model, prompt, blocks)
		rewritten = true
	}
	options := NormalizeOptions{StripSystemCacheControl: !rewritten}
	if metadata := p.MimicMetadata(ctx, body); metadata != "" {
		options.InjectMetadata = true
		options.MetadataUserID = metadata
	}
	body = p.NormalizeOAuth(body, options)
	body = p.RewriteCache(ctx, body)
	// A tool rewrite creates a response-restoration map that needs binding to
	// this request. Without such a map, apply the separate tools-last breakpoint
	// rule to the normalized body.
	if next, found := p.RewriteTools(body); found {
		body = next
		p.BindTools()
	} else {
		body = p.ToolsLast(body)
	}
	return body
}
