package setup

import (
	"strings"
	"testing"
)

// TestValidateHostname supplies accepted host characters, bare IPv6 text,
// length limits, and rejected syntax. Character-only strings can pass, while
// brackets, zones, Unicode, and injected separators must fail.
func TestValidateHostname(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string // subtest display name
		host string // supplied hostname syntax fixture
		want bool   // expected syntax acceptance flag
	}{
		{"empty", "", false},
		{"hostname", "db-1.example.com", true},
		{"IPv4", "127.0.0.1", true},
		{"IPv6", "2001:db8::1", true},
		{"IPv6 brackets", "[::1]", false},
		{"IPv6 zone", "fe80::1%eth0", false},
		{"character validation only", "...---::", true},
		{"maximum length", strings.Repeat("a", 253), true},
		{"over maximum length", strings.Repeat("a", 254), false},
		{"unicode", "数据库", false},
		{"underscore", "db_host", false},
		{"space", "db host", false},
		{"newline", "localhost\n", false},
		{"injection", "localhost;password=x", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validateHostname(tt.host); got != tt.want {
				t.Errorf("validateHostname(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

// TestValidateDBName supplies PostgreSQL database identifiers at and beyond the
// byte limit. Letter-leading ASCII names must pass, while digits first,
// punctuation, Unicode, whitespace, and empty values must fail.
func TestValidateDBName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string // supplied database identifier text
		want bool   // expected syntax acceptance flag
	}{
		{"", false},
		{"a", true},
		{"TokenRouter_123", true},
		{strings.Repeat("a", 63), true},
		{strings.Repeat("a", 64), false},
		{"1database", false},
		{"_database", false},
		{"my-database", false},
		{"my.database", false},
		{"数据库", false},
		{"database ", false},
		{"database\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validateDBName(tt.name); got != tt.want {
				t.Errorf("validateDBName(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// TestValidateUsername supplies ASCII usernames including digit-leading and
// underscore-only values. Accepted character forms within the byte limit must
// pass, while punctuation, Unicode, whitespace, and oversized values must fail.
func TestValidateUsername(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string // supplied database username text
		want bool   // expected syntax acceptance flag
	}{
		{"", false},
		{"1", true},
		{"_", true},
		{"Admin_123", true},
		{strings.Repeat("a", 63), true},
		{strings.Repeat("a", 64), false},
		{"my-user", false},
		{"my.user", false},
		{"管理员", false},
		{"user ", false},
		{"user\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validateUsername(tt.name); got != tt.want {
				t.Errorf("validateUsername(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// TestValidateEmail supplies plain, display-name, quoted-local-part, and
// Unicode display-name address forms. Valid syntax within the complete text
// byte limit must pass, while overflow, missing domains, and address lists must
// fail.
func TestValidateEmail(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string // subtest display name
		email string // supplied full address text
		want  bool   // expected syntax acceptance flag
	}{
		{"empty", "", false},
		{"plain address", "admin@example.com", true},
		{"display name", "Admin <admin@example.com>", true},
		{"unicode display name", "管理员 <admin@example.com>", true},
		{"quoted local part", `"admin name"@example.com`, true},
		{"maximum bytes", strings.Repeat("a", 240) + " <a@b.example>", true},
		{"over maximum bytes", strings.Repeat("a", 241) + " <a@b.example>", false},
		{"unicode maximum bytes", strings.Repeat("名", 80) + " <a@b.example>", true},
		{"unicode over maximum bytes", strings.Repeat("名", 80) + "a <a@b.example>", false},
		{"missing domain", "admin@", false},
		{"address list", "a@example.com,b@example.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validateEmail(tt.email); got != tt.want {
				t.Errorf("validateEmail(%q) = %v, want %v", tt.email, got, tt.want)
			}
		})
	}
}

// TestValidatePort supplies values below, at, and above the usable TCP port
// range. Zero and out-of-range values must fail while positive ports through
// 65535 pass.
func TestValidatePort(t *testing.T) {
	t.Parallel()
	tests := []struct {
		port int  // supplied TCP port number
		want bool // expected syntax acceptance flag
	}{
		{-1, false},
		{0, false},
		{1, true},
		{5432, true},
		{65535, true},
		{65536, false},
	}
	for _, tt := range tests {
		if got := validatePort(tt.port); got != tt.want {
			t.Errorf("validatePort(%d) = %v, want %v", tt.port, got, tt.want)
		}
	}
}

// TestValidateSSLMode supplies supported setup TLS spellings and unsupported
// libpq or formatted variants. Only the four exact accepted strings must pass,
// including rejection of case and surrounding whitespace variants.
func TestValidateSSLMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mode string // supplied TLS mode spelling
		want bool   // expected syntax acceptance flag
	}{
		{"", false},
		{"disable", true},
		{"require", true},
		{"verify-ca", true},
		{"verify-full", true},
		{"allow", false},
		{"prefer", false},
		{"Require", false},
		{"VERIFY-FULL", false},
		{" disable ", false},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			if got := validateSSLMode(tt.mode); got != tt.want {
				t.Errorf("validateSSLMode(%q) = %v, want %v", tt.mode, got, tt.want)
			}
		})
	}
}
