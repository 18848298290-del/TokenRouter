package backup

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// BackupPart describes a gzip archive segment with a one-based index and optional SHA-256 digest.
type BackupPart struct {
	Index      int    `json:"index"`
	StorageKey string `json:"storage_key,omitempty"`
	S3Key      string `json:"s3_key,omitempty"`
	SizeBytes  int64  `json:"size_bytes"`
	SHA256     string `json:"sha256,omitempty"`
}

// OrderedBackupParts returns a sorted copy and rejects gaps, empty storage keys, and nonpositive sizes.
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

// BackupPartStorageKey returns StorageKey, falling back to the legacy S3Key when empty.
func BackupPartStorageKey(part BackupPart) string {
	if strings.TrimSpace(part.StorageKey) != "" {
		return strings.TrimSpace(part.StorageKey)
	}
	return strings.TrimSpace(part.S3Key)
}
