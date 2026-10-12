package modelcatalog

import (
	"maps"
	"slices"
	"sort"
	"strings"
)

// Index fixes a supplemented catalog version. Queries copy only the selected entries.
type Index struct{ data *attributeIndex }

type attributeIndex struct {
	entries                  []Entry
	names, providers, search []string
}

// AttributeQuery uses a zero-based Offset. A zero Limit returns all remaining matches.
type AttributeQuery struct {
	Search, Provider, Capability string
	Offset, Limit                int
}

type AttributePage struct {
	Items     []Entry
	Providers []string
	Total     int
}

func NewIndex(catalog *Catalog) Index {
	data := &attributeIndex{names: []string{}, providers: []string{}}
	if catalog == nil {
		return Index{data: data}
	}
	data.entries = catalog.Rows()
	providers := map[string]bool{}
	for _, entry := range data.entries {
		data.names = append(data.names, entry.Model)
		text := entry.Model
		if entry.Attributes.DisplayName != nil {
			text += " " + *entry.Attributes.DisplayName
		}
		data.search = append(data.search, strings.ToLower(text))
		providers[entry.Provider] = true
	}
	for provider := range providers {
		data.providers = append(data.providers, provider)
	}
	sort.Strings(data.providers)
	return Index{data: data}
}

func (index Index) ModelIDs() []string {
	if index.data == nil {
		return []string{}
	}
	return slices.Clone(index.data.names)
}

func (index Index) Search(query AttributeQuery) AttributePage {
	result := AttributePage{Items: []Entry{}, Providers: []string{}}
	if index.data == nil {
		return result
	}
	result.Providers = slices.Clone(index.data.providers)
	search := strings.ToLower(strings.TrimSpace(query.Search))
	for i, entry := range index.data.entries {
		if query.Provider != "" && entry.Provider != query.Provider || search != "" && !strings.Contains(index.data.search[i], search) || query.Capability != "" && !entry.Attributes.Supports(query.Capability) {
			continue
		}
		matched := result.Total
		result.Total++
		if matched < max(0, query.Offset) || query.Limit > 0 && len(result.Items) >= query.Limit {
			continue
		}
		entry.Cost = cloneCost(entry.Cost)
		entry.Fast = clonePointer(entry.Fast)
		if entry.Fast != nil {
			*entry.Fast = cloneCost(*entry.Fast)
		}
		entry.Tiers = slices.Clone(entry.Tiers)
		for i := range entry.Tiers {
			entry.Tiers[i].Cost = cloneCost(entry.Tiers[i].Cost)
		}
		entry.Attributes = entry.Attributes.Clone()
		entry.AttributeSources = maps.Clone(entry.AttributeSources)
		result.Items = append(result.Items, entry)
	}
	return result
}

func (a Attributes) Supports(capability string) bool {
	var value *bool
	switch capability {
	case "reasoning":
		value = a.Reasoning
	case "temperature":
		value = a.Temperature
	case "top_p":
		value = a.TopP
	case "attachment":
		value = a.Attachment
	case "tool_call":
		value = a.ToolCall
	case "structured_output":
		value = a.StructuredOutput
	}
	return value != nil && *value
}

func (index Index) Len() int {
	if index.data == nil {
		return 0
	}
	return len(index.data.entries)
}

func cloneCost(cost Cost) Cost {
	cost.Input = clonePointer(cost.Input)
	cost.Output = clonePointer(cost.Output)
	cost.CacheRead = clonePointer(cost.CacheRead)
	cost.CacheWrite = clonePointer(cost.CacheWrite)
	return cost
}
