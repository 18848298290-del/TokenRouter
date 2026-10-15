package contract

import "time"

// media.go defines retained moderation image records and their snapshot
// outcomes.
// Administrator image review retains snapshot outcomes even when retrieval
// fails. Records keep the submitted reference and original item position so a
// failed image can still be associated with its ordinary audit or Cyber
// warning.

// ContentModerationMedia represents one retained image, its review position,
// and the outcome of snapshot retrieval.
type ContentModerationMedia struct {
	ID int64 `json:"id"` // stored media row identifier
	// LogID and CyberWarningID identify the owning record. The ordinary and Cyber
	// insertion paths set their respective pointer before writing media in the
	// owner operation; those pointers share the owner record ID storage.
	LogID          *int64 `json:"-"`            // ordinary audit owner identifier
	CyberWarningID *int64 `json:"-"`            // Cyber warning owner identifier
	SourceIndex    int    `json:"source_index"` // original input item position
	Source         string `json:"source"`       // user or tool origin
	MIMEType       string `json:"mime_type"`    // snapshot image media type
	SHA256         string `json:"sha256"`       // hexadecimal snapshot content digest
	ByteSize       int64  `json:"byte_size"`    // bytes
	// submitted image reference text
	OriginalRef    string `json:"original_ref"`
	SnapshotStatus string `json:"snapshot_status"` // pending, ready, or error
	// snapshot retrieval error message
	SnapshotError string `json:"snapshot_error"`
	// Content is excluded from JSON and loaded separately for administrator
	// downloads. SnapshotMedia initially shares existing byte storage when copying
	// entries, so callers keep supplied content stable until that operation
	// returns.
	Content   []byte    `json:"-"`          // binary image snapshot bytes
	CreatedAt time.Time `json:"created_at"` // database row creation time
}
