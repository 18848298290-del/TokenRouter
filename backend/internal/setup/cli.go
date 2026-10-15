package setup

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// cli.go collects interactive installation inputs and runs the confirmed setup
// sequence.
// Interactive installation collects and validates storage fields before running
// connection probes. The PostgreSQL probe can create its target database during
// this wizard, while persistent configuration and administrator creation wait
// for the final installation confirmation and the Install sequence.

// RunCLI runs the stdin/stdout installation wizard and returns probe or
// installation errors. Invalid field input repeats its prompt, and declining
// final confirmation returns nil.
//
// PostgreSQL and Redis are probed before administrator and listener choices are
// finalized, and PostgreSQL probing can create the target database even if the
// operator later cancels. Install repeats both probes and performs migration,
// administrator creation, configuration output, and marker output. Password
// reads prefer terminal input with echo disabled, using trimmed line input when
// redirected or when terminal reading fails.
func RunCLI() error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════╗")
	fmt.Println("║     TokenRouter Installation Wizard       ║")
	fmt.Println("╚═══════════════════════════════════════════╝")
	fmt.Println()

	cfg := &SetupConfig{
		Server: ServerConfig{
			Host: "0.0.0.0",
			Port: 8080,
			Mode: "release",
		},
		JWT: JWTConfig{
			ExpireHour: 24,
		},
	}

	fmt.Println("── Database Configuration ──")

	// Collect valid host, port, user, database name, and TLS mode before probing.
	// Bootstrap interpolates these fields into DSNs and a database-creation statement, so rejected text
	// repeats its prompt before it can enter that probe.
	for {
		cfg.Database.Host = promptString(reader, "PostgreSQL Host", "localhost")
		if validateHostname(cfg.Database.Host) {
			break
		}
		fmt.Println("  Invalid hostname format. Use alphanumeric, dots, hyphens only.")
	}

	for {
		cfg.Database.Port = promptInt(reader, "PostgreSQL Port", 5432)
		if validatePort(cfg.Database.Port) {
			break
		}
		fmt.Println("  Invalid port. Must be between 1 and 65535.")
	}

	for {
		cfg.Database.User = promptString(reader, "PostgreSQL User", "postgres")
		if validateUsername(cfg.Database.User) {
			break
		}
		fmt.Println("  Invalid username. Use alphanumeric and underscores only.")
	}

	cfg.Database.Password = promptPassword("PostgreSQL Password")

	for {
		cfg.Database.DBName = promptString(reader, "Database Name", "tokenrouter")
		if validateDBName(cfg.Database.DBName) {
			break
		}
		fmt.Println("  Invalid database name. Start with letter, use alphanumeric and underscores.")
	}

	for {
		cfg.Database.SSLMode = promptString(reader, "SSL Mode", "disable")
		if validateSSLMode(cfg.Database.SSLMode) {
			break
		}
		fmt.Println("  Invalid SSL mode. Use: disable, require, verify-ca, or verify-full.")
	}

	fmt.Println()
	fmt.Print("Testing database connection... ")
	if err := TestDatabaseConnection(&cfg.Database); err != nil {
		fmt.Println("FAILED")
		return fmt.Errorf("database connection failed: %w", err)
	}
	fmt.Println("OK")

	fmt.Println()
	fmt.Println("── Redis Configuration ──")

	// Redis host and port prompts repeat until their syntax is accepted. Those
	// validated values then select the temporary client used by the later connection probe.
	for {
		cfg.Redis.Host = promptString(reader, "Redis Host", "localhost")
		if validateHostname(cfg.Redis.Host) {
			break
		}
		fmt.Println("  Invalid hostname format. Use alphanumeric, dots, hyphens only.")
	}

	for {
		cfg.Redis.Port = promptInt(reader, "Redis Port", 6379)
		if validatePort(cfg.Redis.Port) {
			break
		}
		fmt.Println("  Invalid port. Must be between 1 and 65535.")
	}

	cfg.Redis.Password = promptPassword("Redis Password (optional)")

	// The wizard shares the Web probe's zero-through-fifteen database range.
	for {
		cfg.Redis.DB = promptInt(reader, "Redis DB", 0)
		if cfg.Redis.DB >= 0 && cfg.Redis.DB <= 15 {
			break
		}
		fmt.Println("  Invalid Redis DB. Must be between 0 and 15.")
	}

	cfg.Redis.EnableTLS = promptConfirm(reader, "Enable Redis TLS?")

	fmt.Println()
	fmt.Print("Testing Redis connection... ")
	if err := TestRedisConnection(&cfg.Redis); err != nil {
		fmt.Println("FAILED")
		return fmt.Errorf("redis connection failed: %w", err)
	}
	fmt.Println("OK")

	fmt.Println()
	fmt.Println("── Admin Account ──")

	// Keep prompting until an address form accepted by the Web installer is supplied.
	// The identity bootstrap input receives that accepted text when installation proceeds.
	for {
		cfg.Admin.Email = promptString(reader, "Admin Email", "admin@example.com")
		if validateEmail(cfg.Admin.Email) {
			break
		}
		fmt.Println("  Invalid email format.")
	}

	for {
		cfg.Admin.Password = promptPassword("Admin Password")
		// Match the Web installer's byte range before confirming this password.
		// Repeating the prompt keeps an invalid credential out of the Install input.
		if len(cfg.Admin.Password) < 8 {
			fmt.Println("  Password must be at least 8 characters")
			continue
		}
		if len(cfg.Admin.Password) > 128 {
			fmt.Println("  Password must be at most 128 characters")
			continue
		}
		confirm := promptPassword("Confirm Password")
		if cfg.Admin.Password != confirm {
			fmt.Println("  Passwords do not match")
			continue
		}
		break
	}

	fmt.Println()
	fmt.Println("── Server Configuration ──")

	// Choose a usable listener port before showing the installation summary and
	// writing server settings, so the printed administrator URL uses the selected port.
	for {
		cfg.Server.Port = promptInt(reader, "Server Port", 8080)
		if validatePort(cfg.Server.Port) {
			break
		}
		fmt.Println("  Invalid port. Must be between 1 and 65535.")
	}

	fmt.Println()
	fmt.Println("── Configuration Summary ──")
	fmt.Printf("Database: %s@%s:%d/%s\n", cfg.Database.User, cfg.Database.Host, cfg.Database.Port, cfg.Database.DBName)
	fmt.Printf("Redis: %s:%d\n", cfg.Redis.Host, cfg.Redis.Port)
	fmt.Printf("Redis TLS: %s\n", map[bool]string{true: "enabled", false: "disabled"}[cfg.Redis.EnableTLS])
	fmt.Printf("Admin: %s\n", cfg.Admin.Email)
	fmt.Printf("Server: :%d\n", cfg.Server.Port)
	fmt.Println()

	if !promptConfirm(reader, "Proceed with installation?") {
		fmt.Println("Installation cancelled")
		return nil
	}

	fmt.Println()
	fmt.Print("Installing... ")
	if err := Install(cfg); err != nil {
		fmt.Println("FAILED")
		return err
	}
	fmt.Println("OK")

	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════╗")
	fmt.Println("║       Installation Complete!              ║")
	fmt.Println("╚═══════════════════════════════════════════╝")
	fmt.Println()
	fmt.Println("Start the server with:")
	fmt.Println("  ./tokenrouter")
	fmt.Println()
	fmt.Printf("Admin panel: http://localhost:%d\n", cfg.Server.Port)
	fmt.Println()

	return nil
}

// promptString prints prompt and reads trimmed line text through reader. Empty
// text returns defaultVal, and read errors are discarded while any returned
// text still participates in this choice.
func promptString(reader *bufio.Reader, prompt, defaultVal string) string {
	if defaultVal != "" {
		fmt.Printf("  %s [%s]: ", prompt, defaultVal)
	} else {
		fmt.Printf("  %s: ", prompt)
	}

	// Use available string text when EOF accompanies an unterminated final line.
	// Trimming then distinguishes that value from empty input selecting the shown
	// default.
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)

	if input == "" {
		return defaultVal
	}
	return input
}

// promptInt reads a trimmed decimal integer after showing the numeric default.
// Empty or malformed text returns defaultVal, and read errors are discarded
// even when a partial line is returned.
func promptInt(reader *bufio.Reader, prompt string, defaultVal int) int {
	fmt.Printf("  %s [%d]: ", prompt, defaultVal)

	// A final integer token can arrive with EOF before a newline. Keep the
	// returned text for parsing so a usable token can override the numeric
	// default.
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)

	if input == "" {
		return defaultVal
	}

	val, err := strconv.Atoi(input)
	if err != nil {
		// RunCLI checks the returned port or database number with its normal range
		// validation. Failed parsing therefore sends the displayed default through
		// that validation before a connection probe or installation can use it.
		return defaultVal
	}
	return val
}

// promptPassword reads a credential with terminal echo disabled when stdin is a
// terminal. Successful terminal bytes are returned unchanged; redirection or a
// terminal-read error selects a new buffered reader whose line text is trimmed,
// and line-read errors are discarded.
func promptPassword(prompt string) string {
	fmt.Printf("  %s: ", prompt)

	// Terminal password input hides credentials from the echoed display. Its result
	// keeps the exact bytes because trimming is part of the redirected-line path below.
	if term.IsTerminal(int(os.Stdin.Fd())) {
		password, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err == nil {
			return string(password)
		}
	}

	// Redirection or terminal-read failure selects line input so the wizard can
	// still obtain a credential. This new reader trims the line and discards read errors.
	reader := bufio.NewReader(os.Stdin)
	// Redirected password input can end after credential bytes without a newline.
	// Use and trim those returned bytes even when the line reader also reports an
	// error.
	input, _ := reader.ReadString('\n')
	return strings.TrimSpace(input)
}

// promptConfirm prints a default-no confirmation and reads one trimmed line
// from reader. Case-insensitive y or yes returns true; other text returns
// false, and any read error is discarded.
func promptConfirm(reader *bufio.Reader, prompt string) bool {
	fmt.Printf("%s [y/N]: ", prompt)
	// An unterminated final answer can still carry y or yes. Retain that text
	// after a read error so confirmation can recognize the operator's affirmative
	// choice.
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToLower(input))
	return input == "y" || input == "yes"
}
