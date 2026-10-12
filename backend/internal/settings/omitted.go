package settings

// Flat request structs initialize absent non-pointer fields to zero, which can
// look like an intentional settings update after normalization. The HTTP mapper
// records their omitted storage keys so the prepared batch can retain those
// saved values during a partial request.

// OmittedKeys names absent non-pointer request fields mapped to their stored setting keys.
type OmittedKeys map[string]struct{}

// DropFrom removes every recorded omitted key from the supplied prepared map.
// A nil set removes nothing; deletion mutates the caller's map in place before
// its remaining values are handed to the update session.
func (o OmittedKeys) DropFrom(values map[string]string) {
	for key := range o {
		delete(values, key)
	}
}
