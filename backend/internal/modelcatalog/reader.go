package modelcatalog

// Reader exposes catalog candidates and metadata. Implementations retain the last valid snapshot when updates
// fail.
type Reader interface {
	ModelIDs() []string
	ModelEntry(string) Entry
	ModelVersion() string
}
