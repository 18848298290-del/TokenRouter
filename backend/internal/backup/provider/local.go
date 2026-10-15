package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// local.go stores and retrieves backup archives beneath a configured
// filesystem directory.
// Local archive keys are resolved beneath one configured directory before file
// access begins. Lexical checks reject escaping paths, and symlink resolution
// permits targets inside that directory while rejecting resolved targets outside
// it. New upload paths resolve their nearest existing parent, then reconstruct
// the missing suffix before access through an opened directory root.
//
// Upload and download use validated os.Root handles with Go's platform-specific
// confinement guarantees. JavaScript builds can race during symlink validation,
// so replacement-race containment is not guaranteed there. Deletion resolves the
// parent and removes the named entry, allowing an accepted final symlink to be
// removed while retaining its target. Upload failures attempt partial-file removal;
// copying observes context between chunks, while filesystem readers and writers
// retain their own behavior for operations already blocked in progress.

// LocalBackupStore stores archive files beneath its configured filesystem root.
type LocalBackupStore struct {
	// basePath is supplied by construction and defines the root used for path
	// validation and os.Root access. Per-record keys select entries beneath this directory.
	basePath string
}

// NewLocalBackupStore binds archive operations to the supplied filesystem directory.
// Construction creates no directories; Upload and HeadBucket create the root
// when needed, while existing-file operations use its current resolved location.
func NewLocalBackupStore(basePath string) *LocalBackupStore {
	return &LocalBackupStore{basePath: basePath}
}

// Upload creates or truncates a validated local archive entry and copies body
// into it, returning its byte count on success. The caller owns body; copying
// checks context before each read but cannot interrupt an already blocked read
// or write, and failures return zero after attempting partial-file removal.
//
// Path and symlink checks precede access through the same opened root. On Linux,
// macOS, and Windows, that handle confines directory-component replacement during
// creation; JavaScript retains Go's symlink-validation race. The file and root
// close on return. Partial-file removal is attempted while the output handle is
// still open, and its failure is suppressed in favor of the copy error.
func (s *LocalBackupStore) Upload(ctx context.Context, key string, body io.Reader, _ string) (int64, error) {
	if _, err := s.safePath(key); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(s.basePath, 0o755); err != nil {
		return 0, err
	}
	root, path, err := s.rooted(key)
	if err != nil {
		return 0, err
	}
	defer func() { _ = root.Close() }()
	if err := root.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, fmt.Errorf("create backup directory: %w", err)
	}
	out, err := root.Create(path)
	if err != nil {
		return 0, fmt.Errorf("create backup file: %w", err)
	}
	// Output closure runs after the copy result has been selected. Keep that
	// result when the deferred close reports an error; success therefore reports copying itself.
	defer func() { _ = out.Close() }()

	written, err := copyWithContext(ctx, out, body)
	if err != nil {
		// Copy failure leaves an incomplete archive. Attempt removal through the same
		// root before deferred file closure, retaining the copy error even if removal fails.
		_ = root.Remove(path)
		return 0, err
	}
	return written, nil
}

// UploadFile uses the local streaming upload path for a caller-owned reader.
// It shares Upload's confinement, context polling, and partial-file cleanup behavior;
// contentType is accepted for store compatibility and does not alter local bytes.
func (s *LocalBackupStore) UploadFile(ctx context.Context, key string, body io.Reader, contentType string) (int64, error) {
	return s.Upload(ctx, key, body, contentType)
}

// Download opens a validated archive entry and returns a file the caller closes.
// It ignores the supplied context; after the root handle closes, the returned
// file remains independently open for archive readers and their cancellation cleanup.
func (s *LocalBackupStore) Download(_ context.Context, key string) (io.ReadCloser, error) {
	root, path, err := s.rooted(key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open backup file: %w", err)
	}
	return file, nil
}

// Delete removes an accepted archive entry and treats missing roots, parents,
// or files as completed deletion. It ignores context and checks confinement
// before removal; a final symlink with an accepted in-root target is removed as
// an entry, leaving that target intact.
//
// The initial rooted lookup validates the target, then parent resolution retains
// the entry's own basename for os.Root.Remove. Resolving the complete target
// again for removal would delete the target file of a final symlink rather than
// the archive entry named by the record's key.
func (s *LocalBackupStore) Delete(_ context.Context, key string) error {
	root, _, err := s.rooted(key)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Retention or earlier cleanup may already have removed the directory or
			// archive entry; report completion so repeated cleanup can discard its stale record.
			return nil
		}
		return err
	}
	defer func() { _ = root.Close() }()
	// Resolve the parent instead of the final symlink target, retaining the named
	// archive entry for removal through os.Root with that platform's path-confinement guarantees.
	full, err := s.safePath(key)
	if err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(full))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// A vanished parent means its named archive entry is already unavailable.
			// Treat that concurrent or earlier cleanup as complete without broadening removal to another path.
			return nil
		}
		return err
	}
	relative, err := filepath.Rel(root.Name(), filepath.Join(parent, filepath.Base(full)))
	if err != nil {
		return err
	}
	// A concurrent cleanup may remove the final entry after parent validation;
	// ignore that absence while keeping other removal errors visible to retention and deletion callers.
	if err = root.Remove(relative); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete backup file: %w", err)
	}
	return nil
}

// PresignURL validates the local key and returns the application-download error.
// Filesystem archives have no object-storage presigner, so callers use the HTTP
// download endpoint; context and requested URL lifetime are unused on this path.
func (s *LocalBackupStore) PresignURL(_ context.Context, key string, _ time.Duration) (string, error) {
	_, err := s.safePath(key)
	if err != nil {
		return "", err
	}
	return "", apperror.BadRequest("BACKUP_LOCAL_DOWNLOAD_REQUIRES_API", "local backup must be downloaded through the application")
}

// HeadBucket creates the configured root and probes its writability using a
// temporary file. It ignores context, reports creation failures, and attempts to
// close and remove the probe while suppressing cleanup errors after creation succeeds.
func (s *LocalBackupStore) HeadBucket(_ context.Context) error {
	if err := os.MkdirAll(s.basePath, 0o755); err != nil {
		return fmt.Errorf("create local backup directory: %w", err)
	}
	testFile, err := os.CreateTemp(s.basePath, ".write-test-*")
	if err != nil {
		return fmt.Errorf("local backup directory is not writable: %w", err)
	}
	name := testFile.Name()
	// Probe creation establishes the writable-directory result. Cleanup is best effort
	// so a close or remove error does not replace that result with a storage-connection failure.
	_ = testFile.Close()
	_ = os.Remove(name)
	return nil
}

// safePath returns an absolute lexical path beneath the configured base directory.
// Empty, absolute, and escaping keys return INVALID_BACKUP_PATH; symlinks are
// evaluated later by rooted, so callers do not use this lexical result alone for file access.
func (s *LocalBackupStore) safePath(key string) (string, error) {
	cleanKey := filepath.Clean(strings.TrimSpace(key))
	if cleanKey == "." || strings.HasPrefix(cleanKey, ".."+string(filepath.Separator)) || filepath.IsAbs(cleanKey) {
		return "", apperror.BadRequest("INVALID_BACKUP_PATH", "invalid backup path")
	}
	baseAbs, err := filepath.Abs(s.basePath)
	if err != nil {
		return "", err
	}
	fullPath := filepath.Join(baseAbs, cleanKey)
	rel, err := filepath.Rel(baseAbs, fullPath)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", apperror.BadRequest("INVALID_BACKUP_PATH", "invalid backup path")
	}
	return fullPath, nil
}

// copyWithContext copies source chunks and returns the bytes written before any
// read, write, short-write, or observed cancellation error. Context is polled before
// each read; active I/O is delegated unchanged, and EOF completes with the count
// after writing any bytes delivered with that final read.
func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	// Bound the temporary copy buffer independently of total archive size.
	buf := make([]byte, 32*1024)
	var written int64
	for {
		select {
		case <-ctx.Done():
			return written, ctx.Err()
		default:
		}
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[0:nr])
			if nw > 0 {
				written += int64(nw)
			}
			if ew != nil {
				return written, ew
			}
			if nr != nw {
				return written, io.ErrShortWrite
			}
		}
		if er != nil {
			if er == io.EOF {
				break
			}
			return written, er
		}
	}
	return written, nil
}

// rooted returns an opened directory root and relative target after lexical and
// symlink confinement checks. The caller closes the root and accesses the returned
// path through that handle, including for targets whose final components do not exist yet.
//
// Resolution walks back to an existing parent and records missing suffix components,
// then appends them to that parent's resolved target. It rejects a target outside
// the real base directory before returning the handle. Subsequent os.Root operations
// confine component traversal across entry replacement on Linux, macOS, and Windows;
// JavaScript retains Go's symlink-validation race. Callers use the returned root
// rather than resolving this relative result through an unrelated filesystem path.
func (s *LocalBackupStore) rooted(key string) (*os.Root, string, error) {
	full, err := s.safePath(key)
	if err != nil {
		return nil, "", err
	}
	base, err := filepath.EvalSymlinks(s.basePath)
	if err != nil {
		return nil, "", err
	}
	base, err = filepath.Abs(base)
	if err != nil {
		return nil, "", err
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, "", err
	}
	// New upload targets may have several missing components. Resolve the nearest
	// existing parent first, then restore those components in their original path order under the root.
	probe := full
	var suffix []string
	for {
		target, e := filepath.EvalSymlinks(probe)
		if e == nil {
			for _, s := range slices.Backward(suffix) {
				target = filepath.Join(target, s)
			}
			rel, e := filepath.Rel(base, target)
			if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				// A resolved target outside the opened base is rejected before any file
				// operation. Release this unused root handle while returning the path-validation error.
				_ = root.Close()
				return nil, "", apperror.BadRequest("INVALID_BACKUP_PATH", "invalid backup path")
			}
			return root, rel, nil
		}
		if !errors.Is(e, os.ErrNotExist) || probe == filepath.Dir(probe) {
			// Only missing components justify walking toward a parent. Other resolution
			// failures release the unused root instead of guessing a target that could escape validation.
			_ = root.Close()
			return nil, "", e
		}
		suffix = append(suffix, filepath.Base(probe))
		probe = filepath.Dir(probe)
	}
}

// DefaultBackupLocalPath returns the backups subdirectory beneath the selected
// application data directory. Selection follows resolveBackupDataDir, so the
// returned path can come from DATA_DIR, a writable container directory, or the current directory.
func DefaultBackupLocalPath() string {
	return filepath.Join(resolveBackupDataDir(), "backups")
}

// resolveBackupDataDir selects DATA_DIR as supplied when it contains nonwhitespace
// text, otherwise probes /app/data and finally returns the current-directory marker.
// The environment override is not validated here; fallback probing attempts a
// write and suppresses cleanup errors once that probe can be created.
func resolveBackupDataDir() string {
	if dir := os.Getenv("DATA_DIR"); strings.TrimSpace(dir) != "" {
		return dir
	}

	dockerDataDir := "/app/data"
	// Use the current directory when the container directory is missing, unusable,
	// or fails its write probe, allowing local placement selection to return after those failures.
	if info, err := os.Stat(dockerDataDir); err == nil && info.IsDir() {
		testFile := filepath.Join(dockerDataDir, ".write_test")
		if f, err := os.Create(testFile); err == nil {
			// Successful probe creation selects the container data directory. Attempt
			// cleanup before returning, while leaving creation failure to choose the current-directory default.
			_ = f.Close()
			_ = os.Remove(testFile)
			return dockerDataDir
		}
	}
	return "."
}
