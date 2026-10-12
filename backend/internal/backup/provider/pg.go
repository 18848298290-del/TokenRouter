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

// DatabaseOptions is the database connection configuration supplied at construction.
type DatabaseOptions struct {
	Host                            string
	Port                            int
	User, Password, DBName, SSLMode string
}

// PgDumper implements database export and restore through pg_dump and psql.
type PgDumper struct {
	cfg *DatabaseOptions
}

// cmdReadCloser wraps a command stdout pipe and waits for the process on Close.
type cmdReadCloser struct {
	io.ReadCloser
	cmd      *exec.Cmd
	once     sync.Once
	closeErr error
}

// NewPgDumper creates a new PgDumper.
func NewPgDumper(cfg DatabaseOptions) backup.DBDumper {
	return &PgDumper{cfg: &cfg}
}

// Dump executes pg_dump and returns a streaming reader of the output.
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
		// Exclude data while retaining table definitions, constraints, and indexes for restore.
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

// Restore drops derived tables and restores the archive in one psql transaction, canceling on input failure.
// @project-doc docs/operations/deployment_and_migrations.md#maintenance_execution
func (d *PgDumper) Restore(ctx context.Context, data io.Reader) error {
	// Reject empty archives before dropping derived tables.
	archive := bufio.NewReader(data)
	if _, err := archive.Peek(1); err != nil {
		return fmt.Errorf("读取备份归档: %w", err)
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

	// Cancel psql before closing stdin on input failure, because EOF can commit an incomplete archive.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return err
	}
	// Older archives omit this table's DROP; remove its foreign-key dependency on users before restoring.
	// The prefix and archive SQL share one transaction and roll back together.
	input := io.MultiReader(strings.NewReader("DROP TABLE IF EXISTS public.usage_user_activity;\n"), archive)
	_, copyErr := io.Copy(stdin, input)
	if copyErr != nil {
		cancel()
	}
	closeErr := stdin.Close()
	waitErr := cmd.Wait()
	if err := errors.Join(copyErr, closeErr, waitErr); err != nil {
		return fmt.Errorf("%w: %s", err, output.String())
	}
	return nil
}

func (c *cmdReadCloser) Close() error {
	c.once.Do(func() {
		_ = c.ReadCloser.Close()
		if err := c.cmd.Wait(); err != nil {
			c.closeErr = fmt.Errorf("pg_dump exited with error: %w", err)
		}
	})
	return c.closeErr
}
