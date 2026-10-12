package contract

import "time"

// ContentModerationMedia stores an image snapshot and its retrieval status for administrator review.
type ContentModerationMedia struct {
	ID             int64     `json:"id"`
	LogID          *int64    `json:"-"`
	CyberWarningID *int64    `json:"-"`
	SourceIndex    int       `json:"source_index"`
	Source         string    `json:"source"`
	MIMEType       string    `json:"mime_type"`
	SHA256         string    `json:"sha256"`
	ByteSize       int64     `json:"byte_size"`
	OriginalRef    string    `json:"original_ref"`
	SnapshotStatus string    `json:"snapshot_status"`
	SnapshotError  string    `json:"snapshot_error"`
	Content        []byte    `json:"-"`
	CreatedAt      time.Time `json:"created_at"`
}
