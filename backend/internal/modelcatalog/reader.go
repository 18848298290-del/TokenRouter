package modelcatalog

// Catalog consumers read model identities, metadata, and a publication version
// through the same small interface. Production implementations retain their
// last valid publication after a failed update so consumers can continue
// resolving metadata during refresh failures.

// Reader exposes model identities, metadata, and the retained catalog publication version.
type Reader interface {
	// ModelIDs returns the model identities available from the current publication.
	ModelIDs() []string
	// ModelEntry returns catalog metadata for the requested complete model identity.
	ModelEntry(string) Entry
	// ModelVersion returns the content version identifying the current catalog publication.
	ModelVersion() string
}
