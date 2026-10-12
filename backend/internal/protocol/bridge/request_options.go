package bridge

// RequestOptions contains caller-selected target behavior, independent of the model name.
type RequestOptions struct {
	DropSampling      bool
	DropTemperature   bool
	DropTopP          bool
	SupportsMaxEffort bool
}
