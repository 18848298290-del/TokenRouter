package settings

// OmittedKeys identifies absent non-pointer fields; nil drops no keys from the update.
type OmittedKeys map[string]struct{}

// DropFrom removes omitted keys from the prepared values map.
func (o OmittedKeys) DropFrom(values map[string]string) {
	for key := range o {
		delete(values, key)
	}
}
