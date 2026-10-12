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

const responsesReasoningCacheTTL = 7 * 24 * time.Hour

// ReasoningHistory caches reasoning text for seven days with a two-second budget per operation.
type ReasoningHistory struct {
	Cache ReasoningContentCache
	Warn  func(string, error)
}

// Lookup retrieves reasoning text by item ID and returns empty on a miss or unavailable cache.
func (s *ReasoningHistory) Lookup(itemID string) string {
	if s == nil || s.Cache == nil {
		return ""
	}
	cache := s.Cache
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	content, err := cache.GetReasoningContent(ctx, itemID)
	if err != nil {
		return ""
	}
	return content
}

// FromInput refreshes cached reasoning from plaintext items in request history,
// allowing later encrypted-only items to recover after cache eviction or instance changes.
func (s *ReasoningHistory) FromInput(inputRaw json.RawMessage) {
	if s == nil || s.Cache == nil {
		return
	}
	inputRaw = bytes.TrimSpace(inputRaw)
	if len(inputRaw) == 0 || inputRaw[0] != '[' {
		return
	}
	var items []json.RawMessage
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

// FromEvents caches completed reasoning items from Responses stream events.
func (s *ReasoningHistory) FromEvents(events []protocolopenai.ResponsesStreamEvent) {
	for _, event := range events {
		if event.Type == "response.output_item.done" && event.Item != nil {
			s.cacheItem(event.Item)
		}
	}
}

// FromOutput caches reasoning items from a non-streaming Responses output.
func (s *ReasoningHistory) FromOutput(output []protocolopenai.ResponsesOutput) {
	for i := range output {
		s.cacheItem(&output[i])
	}
}

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

// Store writes with an independent context so client cancellation does not interrupt caching during upstream draining.
// It reports write failures through Warn and lets response output continue.
func (s *ReasoningHistory) Store(itemID, content string) {
	if s == nil || s.Cache == nil {
		return
	}
	cache := s.Cache
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := cache.SetReasoningContent(ctx, itemID, content, responsesReasoningCacheTTL); err != nil {
		if s.Warn != nil {
			s.Warn(itemID, err)
		}
	}
}
