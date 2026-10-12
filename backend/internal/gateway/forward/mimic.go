package forward

import "context"

// MimicPorts supplies settings and platform-specific client-identity rewrites.
type MimicPorts interface {
	SystemSettings(context.Context) (bool, string, string)
	RewriteMimicSystem([]byte, string, string, string) []byte
	MimicMetadata(context.Context, []byte) string
	NormalizeOAuth([]byte, NormalizeOptions) []byte
	RewriteCache(context.Context, []byte) []byte
	RewriteTools([]byte) ([]byte, bool)
	BindTools()
	ToolsLast([]byte) []byte
}

// Mimic applies system, metadata, cache, and tool rewrites in order for eligible requests.
func Mimic(ctx context.Context, p MimicPorts, eligible bool, body []byte, model string) []byte {
	if !eligible || len(body) == 0 {
		return body
	}
	enabled, prompt, blocks := p.SystemSettings(ctx)
	rewritten := false
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
	if next, found := p.RewriteTools(body); found {
		body = next
		p.BindTools()
	} else {
		body = p.ToolsLast(body)
	}
	return body
}
