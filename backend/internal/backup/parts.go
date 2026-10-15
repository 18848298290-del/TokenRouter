package backup

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// parts.go validates and orders archive segment metadata for multipart
// downloads and restoration.
// Multipart archive metadata must describe every segment in order before restore
// can assemble a complete gzip stream. Sorting a copy keeps the stored record's
// slice order intact, while contiguous indices, nonempty effective keys, and positive
// sizes reject incomplete plans before any object download begins.

// BackupPart describes a gzip archive segment with a one-based index and optional SHA-256 digest.
type BackupPart struct {
	Index int `json:"index"` // one-based contiguous segment number
	// StorageKey and S3Key are current and legacy lookup alternatives for one archive
	// segment. BackupPartStorageKey prefers trimmed StorageKey, using trimmed S3Key
	// when the current field is empty.
	StorageKey string `json:"storage_key,omitempty"` // current archive object key
	S3Key      string `json:"s3_key,omitempty"`      // legacy S3 object key
	// SizeBytes is the compressed segment length. Ordering rejects nonpositive sizes,
	// and multipart restore compares the downloaded byte count with this recorded value.
	SizeBytes int64 `json:"size_bytes"`
	// SHA256 is an optional hexadecimal digest. Multipart restore compares it case
	// insensitively with downloaded bytes when present, while empty digests skip checksum verification.
	SHA256 string `json:"sha256,omitempty"`
}

// OrderedBackupParts returns a copy sorted by one-based segment index and rejects
// empty plans, index gaps or duplicates, missing effective keys, and nonpositive
// sizes. It leaves the input slice order unchanged so callers can validate a
// restoration plan without rearranging metadata retained by the backup service.
func OrderedBackupParts(parts []BackupPart) ([]BackupPart, error) {
	if len(parts) == 0 {
		return nil, errors.New("backup parts are empty")
	}
	ordered := append([]BackupPart(nil), parts...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Index < ordered[j].Index })
	for i, part := range ordered {
		if part.Index != i+1 || BackupPartStorageKey(part) == "" || part.SizeBytes <= 0 {
			return nil, fmt.Errorf("invalid backup part metadata at index %d", i+1)
		}
	}
	return ordered, nil
}

// BackupPartStorageKey returns the trimmed current segment key, using the trimmed
// legacy S3 key when the current value is empty. Callers use this precedence
// consistently for restore validation and object downloads from older records.
func BackupPartStorageKey(part BackupPart) string {
	if strings.TrimSpace(part.StorageKey) != "" {
		return strings.TrimSpace(part.StorageKey)
	}
	// Older multipart metadata can contain only S3Key. Reuse that recorded key so
	// archives written with the legacy field remain addressable during object restoration and cleanup.
	return strings.TrimSpace(part.S3Key)
}
