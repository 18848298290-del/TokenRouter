package google

// Drive storage capacity is used by GeminiAuthorization to estimate a Google
// One tier, while usage reports consumed storage separately. The Drive client
// converts decimal response strings to byte counts and leaves missing or
// unparseable observations at zero, so zero alone cannot establish unused capacity.

// DriveStorageInfo holds reported Drive capacity and consumed-storage byte counts.
type DriveStorageInfo struct {
	// Limit and Usage are parsed independently from the Drive storage response.
	// The client leaves either field at zero when its decimal string is missing
	// or invalid; tier estimation reads Limit rather than subtracting Usage.
	Limit int64 `json:"limit"` // bytes
	Usage int64 `json:"usage"` // bytes
}
