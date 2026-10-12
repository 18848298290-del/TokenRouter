package testkit

import (
	"encoding/json"
	"slices"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
)

// OfflineView 提供内嵌目录和补充资料组成的固定测试快照。
var OfflineView = sync.OnceValue(func() modelcatalog.View {
	body, err := modelcatalog.Offline()
	if err != nil {
		panic(err)
	}
	catalog, err := modelcatalog.Parse(body)
	if err != nil {
		panic(err)
	}
	supplements := make(map[string]json.RawMessage)
	if err := json.Unmarshal(modelcatalog.Supplements(), &supplements); err != nil {
		panic(err)
	}
	for model, value := range supplements {
		if err := catalog.ApplyAttributeSupplement(model, value); err != nil {
			panic(err)
		}
	}
	for model, value := range supplements {
		for _, alias := range catalog.FirstPartyAliases(model) {
			if err := catalog.ApplyAttributeSupplement(alias, value); err != nil {
				panic(err)
			}
		}
	}
	return catalog.View()
})

// Catalog 为目录使用方的测试提供可替换版本和明确条目。
type Catalog struct {
	Entries map[string]modelcatalog.Entry
	Version string
}

// New 构造只含测试指定型号的目录，默认展示名称等于 ID。
func New(ids ...string) *Catalog {
	c := &Catalog{Entries: map[string]modelcatalog.Entry{}, Version: "test"}
	for _, id := range ids {
		name := id
		c.Entries[id] = modelcatalog.Entry{Model: id, Attributes: modelcatalog.Attributes{DisplayName: &name}}
	}
	return c
}

// ModelIDs 返回夹具中按 ID 排序的候选。
func (c *Catalog) ModelIDs() []string {
	ids := []string{}
	for id := range c.Entries {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// ModelEntry 返回测试声明的元数据。
func (c *Catalog) ModelEntry(id string) modelcatalog.Entry { return c.Entries[id] }

// ModelVersion 返回测试控制的目录版本。
func (c *Catalog) ModelVersion() string { return c.Version }
