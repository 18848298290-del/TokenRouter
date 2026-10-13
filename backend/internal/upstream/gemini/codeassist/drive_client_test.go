package codeassist

import "testing"

// TestDriveStorageInfo constructs a quota observation with one-hundred GiB
// capacity and fifty GiB usage. It checks the fields retain their byte counts;
// this fixture exercises representation rather than an API exchange.
func TestDriveStorageInfo(t *testing.T) {
	info := &DriveStorageInfo{
		Limit: 100 * 1024 * 1024 * 1024, // 100 GiB
		Usage: 50 * 1024 * 1024 * 1024,  // 50 GiB
	}

	if info.Limit != 100*1024*1024*1024 {
		t.Errorf("Expected limit 100GB, got %d", info.Limit)
	}
	if info.Usage != 50*1024*1024*1024 {
		t.Errorf("Expected usage 50GB, got %d", info.Usage)
	}
}
