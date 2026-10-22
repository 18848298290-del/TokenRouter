package provider

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/backup"
)

// pg.go runs PostgreSQL export and restore processes and reports their stream
// and process failures.
// Restore input can fail after its earlier SQL has reached the database client.
// Closing stdin then presents EOF, so the restore path requests process cancellation
// before closing that pipe and joins copy, close, and exit failures. The psql
// command enables single-transaction execution and ON_ERROR_STOP while sending
// the derived-table prefix and dump SQL through the same input pipe.
//
// Process exit remains separate from stream completion. Dump callers close the
// returned stdout reader to wait for pg_dump and observe its exit error; restore
// waits inside the call after supplying input. Connection options are copied at
// construction, and nonempty password or SSL settings supplement the child's
// inherited environment. Restore error reporting does not inspect the database
// to determine whether cancellation preceded its commit decision.

// DatabaseOptions is the database connection configuration supplied at construction.
type DatabaseOptions struct {
	Host string // PostgreSQL host or socket directory
	Port int    // PostgreSQL server connection port
	// User, Password, DBName, and SSLMode supply both pg_dump and psql connections.
	// User and DBName are command arguments; Password and SSLMode are environment overrides.
	// Nonempty Password and SSLMode add PostgreSQL environment entries. Empty values
	// leave inherited entries intact rather than clearing the child's existing credential or TLS policy.
	// User is the database login name; Password is its supplied credential value;
	// DBName identifies the target database; SSLMode supplies the PostgreSQL transport policy.
	User, Password, DBName, SSLMode string
}

// PgDumper implements database export and restore through pg_dump and psql.
type PgDumper struct {
	// cfg points to the value copied by NewPgDumper. Calls share these fixed
	// connection options, so subsequent mutation of the caller's options cannot change them.
	cfg *DatabaseOptions
}

// cmdReadCloser wraps a command stdout pipe and waits for the process on Close.
type cmdReadCloser struct {
	// ReadCloser supplies cmd's stdout. once closes that stream and waits for cmd
	// once, retaining closeErr so repeated Close calls report the same process exit result.
	io.ReadCloser           // command stdout stream reader
	cmd           *exec.Cmd // running database export process
	once          sync.Once // one close and wait guard
	closeErr      error     // cached command exit failure
}

// NewPgDumper constructs a database exporter with copied PostgreSQL connection
// options. Commands start only when Dump or Restore is called, and those calls
// use the caller's contexts for subprocess cancellation.
func NewPgDumper(cfg DatabaseOptions) backup.DBDumper {
	return &PgDumper{cfg: &cfg}
}

// Dump starts pg_dump and returns its stdout as a streaming archive reader.
// ExcludeTableData omits selected table rows while keeping schema definitions;
// callers close the returned reader to wait for the process and report exit
// failure, including when they stop consuming its output early.
func (d *PgDumper) Dump(ctx context.Context, opts backup.BackupDumpOptions) (io.ReadCloser, error) {
	args := []string{
		"-h", d.cfg.Host,
		"-p", fmt.Sprintf("%d", d.cfg.Port),
		"-U", d.cfg.User,
		"-d", d.cfg.DBName,
		"--no-owner",
		"--no-acl",
		"--clean",
		"--if-exists",
	}
	for _, tablePattern := range opts.ExcludeTableData {
		// Keep schema objects available for restoration even when content policy
		// excludes their rows, so optional historical groups can be omitted without dropping their tables.
		args = append(args, "--exclude-table-data="+tablePattern)
	}

	cmd := exec.CommandContext(ctx, "pg_dump", args...)
	if d.cfg.Password != "" {
		cmd.Env = append(cmd.Environ(), "PGPASSWORD="+d.cfg.Password)
	}
	if d.cfg.SSLMode != "" {
		cmd.Env = append(cmd.Environ(), "PGSSLMODE="+d.cfg.SSLMode)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start pg_dump: %w", err)
	}

	return &cmdReadCloser{ReadCloser: stdout, cmd: cmd}, nil
}

// Restore prefixes archive SQL with removal of usage_user_activity and supplies
// it to psql with single-transaction and stop-on-error options. Empty input is
// rejected before starting the client; callers supply SQL suitable for that
// transaction because embedded transaction control can override psql's grouping.
//
// A copy failure cancels the child context before stdin closes, then process
// waiting joins copy, close, and exit errors. Cancelling requests child termination;
// it does not synchronously establish that termination precedes EOF handling.
// Returned errors therefore report the observed input and process failures, while
// callers separately reconcile database state if a restore outcome is uncertain.
// @project-doc docs/operations/deployment_and_migrations.md#maintenance_execution
func (d *PgDumper) Restore(ctx context.Context, data io.Reader) error {
	// Peek without consuming the first byte, rejecting empty or unreadable archives
	// before a psql process can execute the derived-table removal prefix against the database.
	archive := bufio.NewReader(data)
	if _, err := archive.Peek(1); err != nil {
		return fmt.Errorf("read backup archive: %w", err)
	}
	args := []string{
		"-h", d.cfg.Host,
		"-p", fmt.Sprintf("%d", d.cfg.Port),
		"-U", d.cfg.User,
		"-d", d.cfg.DBName,
		"--single-transaction",
		"--set=ON_ERROR_STOP=1",
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, "psql", args...)
	if d.cfg.Password != "" {
		cmd.Env = append(cmd.Environ(), "PGPASSWORD="+d.cfg.Password)
	}
	if d.cfg.SSLMode != "" {
		cmd.Env = append(cmd.Environ(), "PGSSLMODE="+d.cfg.SSLMode)
	}

	// Keep stdin open until copying finishes or cancellation has been requested;
	// ordinary EOF lets the client's file action finish and reach its transaction decision.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		// A failed process start leaves no child to consume stdin. Release its pipe
		// while retaining the start error as the actionable failure for the restore caller.
		_ = stdin.Close()
		return err
	}
	// Older dumps can omit usage_user_activity's DROP while it references users.
	// Send that derived-table removal ahead of the archive under the same psql transaction options.
	input := io.MultiReader(strings.NewReader("DROP TABLE IF EXISTS public.usage_user_activity;\n"), archive)
	_, copyErr := io.Copy(stdin, input)
	if copyErr != nil {
		// Closing stdin can let psql commit the consumed SQL. Request cancellation
		// first, then report both the input failure and the observed process outcome after waiting.
		cancel()
	}
	closeErr := stdin.Close()
	waitErr := cmd.Wait()
	if err := errors.Join(copyErr, closeErr, waitErr); err != nil {
		return fmt.Errorf("%w: %s", err, output.String())
	}
	return nil
}

// Close closes command stdout and waits once for pg_dump, returning its cached
// exit error on repeated calls. Closing early can make the command fail while
// writing output; stdout-close errors are ignored, and the supplied command context
// controls interruption if the process is still waiting on database work.
// Callers drain stdout before closing when a complete export is needed. Reading
// EOF alone does not establish successful process exit; that result is checked here.
func (c *cmdReadCloser) Close() error {
	c.once.Do(func() {
		// Close the read side before waiting so an abandoned output stream can stop
		// the exporter; retain the process result as the error shared by later Close callers.
		_ = c.ReadCloser.Close()
		if err := c.cmd.Wait(); err != nil {
			c.closeErr = fmt.Errorf("pg_dump exited with error: %w", err)
		}
	})
	return c.closeErr
}
