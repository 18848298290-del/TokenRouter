//go:build embed

package web

// init provides an empty initialization hook in the embed test build. It
// performs no fixture setup or shared-state mutation, leaving the package's
// TestMain responsible for Gin test mode.
func init() {
}
