package session

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// Responses-to-Chat history can recover reasoning text by its Responses item ID.
// Plaintext request history, completed stream items, and buffered output seed this
// cache, so later encrypted-only history can reuse text when it remains stored.
// Each storage operation receives an independent context with a two-second
// deadline. The production Redis client uses configured socket read/write
// timeouts, so these context deadlines impose no two-second method-return bound.

// responsesReasoningCacheTTL retains stored reasoning text for seven days to
// support later item-ID recovery while the corresponding Redis entry remains available.
const responsesReasoningCacheTTL = 7 * 24 * time.Hour

// ReasoningHistory reads and refreshes optional item-ID reasoning text for conversion.
type ReasoningHistory struct {
	// Cache supplies synchronous text operations; nil storage makes Lookup empty
	// and makes Store skip writes while history parsing can continue.
	Cache ReasoningContentCache
	// Warn receives the item ID and failed write error synchronously; nil skips
	// warnings after failed caching.
	Warn func(string, error)
}

// Lookup returns the cached text for itemID, or empty when the cache is absent or
// any read fails. The cache receives a fresh background context with a
// two-second deadline, independent of the requesting client's cancellation.
// Completion after that deadline depends on the cache adapter.
func (s *ReasoningHistory) Lookup(itemID string) string {
	if s == nil || s.Cache == nil {
		return ""
	}
	cache := s.Cache
	// Give conversion history a context independent of the request, allowing
	// the cache adapter to recover text after client cancellation.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	content, err := cache.GetReasoningContent(ctx, itemID)
	if err != nil {
		// A miss and an unavailable cache both leave conversion without stored
		// reasoning text; the consumer can continue using its other history evidence.
		return ""
	}
	return content
}

// FromInput refreshes reasoning text extracted from an array of plaintext history
// items. Nil storage, non-array bytes, or malformed JSON are ignored; valid items
// are stored serially with a separately created context deadline for each call.
func (s *ReasoningHistory) FromInput(inputRaw json.RawMessage) {
	if s == nil || s.Cache == nil {
		return
	}
	inputRaw = bytes.TrimSpace(inputRaw)
	if len(inputRaw) == 0 || inputRaw[0] != '[' {
		return
	}
	var items []json.RawMessage
	// Invalid history supplies no trusted item IDs for recovery, so skip its
	// cache refresh while the caller continues its request conversion path.
	if err := json.Unmarshal(inputRaw, &items); err != nil {
		return
	}
	for _, raw := range items {
		id, text, ok := bridge.ExtractResponsesReasoningItem(raw)
		if ok && id != "" && text != "" {
			s.Store(id, text)
		}
	}
}

// FromEvents stores reasoning from completed output-item stream events in order.
// Other event types and absent items are ignored; cacheItem validates reasoning
// identity, and unavailable storage makes the resulting Store calls harmless.
func (s *ReasoningHistory) FromEvents(events []protocolopenai.ResponsesStreamEvent) {
	for _, event := range events {
		if event.Type == "response.output_item.done" && event.Item != nil {
			s.cacheItem(event.Item)
		}
	}
}

// FromOutput inspects buffered response items and stores their reasoning summaries.
// Items are visited in supplied order, with cacheItem filtering invalid identity;
// absent storage or write failure allows response handling to continue.
func (s *ReasoningHistory) FromOutput(output []protocolopenai.ResponsesOutput) {
	for i := range output {
		s.cacheItem(&output[i])
	}
}

// cacheItem joins nonblank reasoning-summary text under the item's ID.
// Nil items, other item types, and empty IDs are skipped; each summary is trimmed,
// then joined with newlines before Store is invoked.
func (s *ReasoningHistory) cacheItem(item *protocolopenai.ResponsesOutput) {
	if item == nil || item.Type != "reasoning" || item.ID == "" {
		return
	}
	parts := make([]string, 0, len(item.Summary))
	for _, summary := range item.Summary {
		if text := strings.TrimSpace(summary.Text); text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) > 0 {
		s.Store(item.ID, strings.Join(parts, "\n"))
	}
}

// Store writes itemID and content with the seven-day reasoning lifetime.
// Nil history or storage skips the write. Each write receives a fresh background
// context with a two-second deadline, allowing upstream draining to invoke it
// after client cancellation. Errors reach the optional Warn callback synchronously.
func (s *ReasoningHistory) Store(itemID, content string) {
	if s == nil || s.Cache == nil {
		return
	}
	cache := s.Cache
	// Give response draining a fresh context independent of request cancellation;
	// each item receives its own deadline, so a list creates multiple operation contexts.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Cache failure is advisory to output; report the item through Warn when
	// configured and let the caller continue consuming or converting the response.
	if err := cache.SetReasoningContent(ctx, itemID, content, responsesReasoningCacheTTL); err != nil {
		if s.Warn != nil {
			s.Warn(itemID, err)
		}
	}
}
