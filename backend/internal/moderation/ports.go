package moderation

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/moderation/contract"
	notice "github.com/TokenFlux/TokenRouter/internal/notification/contract"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// Moderation requests consult settings, identity accounts, groups, proxies, and
// notification delivery through capabilities bound by app. Ordinary account
// actions use status commands and later invalidation, while Cyber persistence
// has a separate transaction participant in the PostgreSQL adapter.

// User status labels and moderation setting keys connect risk actions and
// configuration reads with identity and settings adapters.
const (
	StatusActive = "active" // eligible identity status label
	// disabled identity status label.
	StatusDisabled = "disabled"
	// serialized moderation policy settings.
	SettingKeyContentModerationConfig = "content_moderation_config"
	// risk center master switch.
	SettingKeyRiskControlEnabled = "risk_control_enabled"
)

// ErrSettingNotFound identifies absent settings so loadConfig can choose a
// normalized default moderation policy.
var ErrSettingNotFound = settings.ErrSettingNotFound

type (
	// ContentModerationMedia shares the image snapshot representation used by media
	// retrieval, persistence, and administrator review across moderation adapters.
	ContentModerationMedia = contract.ContentModerationMedia
	// SettingRepository is the settings storage capability used by moderation
	// configuration reads, batch runtime loads, and dedicated configuration writes.
	SettingRepository = settings.Repository
	// Setting is the shared stored key and value representation returned by the
	// settings repository and its moderation test fixtures.
	Setting = settings.Setting
)

// UserSnapshot contains identity data consulted by ordinary ban and
// administrator unban operations.
type UserSnapshot struct {
	ID int64 // risk action account identifier
	// account role, request eligibility status, and recipient email
	Role, Status, Email string
}

// UserCommands reads and changes the identity account targeted by ordinary
// moderation actions.
type UserCommands interface {
	// GetByID reads the user targeted by a risk action. Successful lookup supplies
	// a nonnil snapshot; the assembled MissingUser predicate classifies errors for
	// administrator unban responses, while automatic-ban errors are logged.
	GetByID(context.Context, int64) (*UserSnapshot, error)
	// SetStatus persists the supplied status for the given account. Moderation calls
	// authentication invalidation after a successful status command; a returned
	// error prevents its successful-ban or successful-unban result from being reported.
	SetStatus(context.Context, int64, string) error
}

// GroupRepository checks whether group references in administrator moderation
// configuration can be resolved.
type GroupRepository interface {
	// CheckGroup reports whether the supplied group reference can be loaded. The
	// configuration validator calls it for selected positive IDs when all-group
	// scope is disabled and translates a returned error to invalid configuration.
	CheckGroup(context.Context, int64) error
}

// ProxyInfo supplies the resolved audit URL and proxy observations used by
// moderation diagnostics.
type ProxyInfo struct {
	// dial URL, display name, stored status, and credential-free diagnostic
	// address
	URL, Name, Status, Address string
	// Active and Expired describe the proxy at the lookup time.
	// resolveModerationProxyURL logs inactive or expired observations and still
	// uses URL when the lookup succeeds.
	Active, Expired bool // activity and sampled expiry observations
}

// ProxyRepository resolves stored proxy selections for moderation audit
// requests and configuration validation.
type ProxyRepository interface {
	// Lookup returns the selected proxy and its observations at the supplied time.
	// Resolution errors abort an audit attempt; successful inactive or expired
	// records still supply their URL to resolveModerationProxyURL after diagnostic logging.
	Lookup(context.Context, int64, time.Time) (ProxyInfo, error)
}
type (
	// APIKeyAuthCacheInvalidator removes cached authentication after moderation
	// applies a user status change or an administrator confirms active status.
	APIKeyAuthCacheInvalidator interface {
		// InvalidateAuthCacheByUserID requests removal of cached keys for an account.
		// Its result has no error channel; moderation invokes it after a successful
		// status command or committed Cyber ban and then continues side effects.
		InvalidateAuthCacheByUserID(context.Context, int64)
	}
	// AuditTransport executes one JSON moderation exchange and returns status,
	// bounded failure bytes, or a transport or decode error to the core.
	AuditTransport interface {
		// Execute posts serialized input using the supplied credential and proxy resolver.
		// The resolver is required and may return an empty direct URL; a resolver or
		// client error aborts the exchange. Successful replies decode into output.
		Execute(context.Context, string, string, []byte, func() (string, error), any) (int, []byte, error)
	}
)

// Runtime binds the transport, media, background owner, and upstream
// classifiers supplied by app assembly.
type Runtime struct {
	// MissingRow and MissingUser optionally translate repository failures for
	// administrator review and unban responses. Nil predicates leave those failures
	// on the corresponding helper's ordinary error path.
	MissingRow  func(error) bool // absent review row classifier
	MissingUser func(error) bool // absent identity user classifier
	// Audit, SnapshotMedia, Background, CyberText, and CyberPolicy are invoked by
	// their corresponding enabled service paths. App supplies all five before
	// requests or background workers begin; enabled paths require usable
	// implementations.
	Audit AuditTransport // external moderation request executor
	// retained image snapshot operation
	SnapshotMedia func(context.Context, []ContentModerationMedia) []ContentModerationMedia
	// Background accepts snapshot refresh work while its caller holds
	// runtimeRefreshMu. The launcher must arrange execution so the callback
	// releases that lock. App uses lifecycle.Tasks and discards its admission
	// result, so callers avoid triggering refresh after that tracker seals.
	Background func(string, func()) // runtime snapshot refresh launcher
	CyberText  func(string) bool    // upstream warning text classifier
	// upstream Cyber code classifier
	CyberPolicy func([]byte) (bool, string, string)
	// ErrorMessage is retained in the assembled Runtime value.
	// ExtractCyberWarningText currently calls upstream.ExtractErrorMessage
	// directly, so this callback is not the extractor used by that helper.
	ErrorMessage func([]byte) string // assembled upstream error extractor
}

// RiskSender delivers notices for the risk observations and account changes
// already selected by moderation.
type RiskSender interface {
	// SendViolationEmail sends the ordinary hit notice for a nonnil log observation.
	// Fresh ordinary logs still have no stored ID at this phase, so assembled
	// notification delivery can proceed without source-key deduplication for that input.
	SendViolationEmail(context.Context, *notice.RiskPolicy, *notice.RiskLog) error
	// SendAccountDisabledEmail reports an ordinary ban already applied by the caller.
	// The assembled sender requires a nonnil log; the caller records delivery
	// errors and retains the completed status change when delivery fails.
	SendAccountDisabledEmail(context.Context, *notice.RiskPolicy, *notice.RiskLog) error
	// SendCyberAccountDisabledEmail reports a committed Cyber ban using its warning identity.
	// The warning is nonnil and already stored; a delivery error leaves that
	// transaction committed and prevents the later email-sent marker update.
	SendCyberAccountDisabledEmail(context.Context, *notice.RiskPolicy, *notice.RiskWarning) error
}

// Decision names the moderation result consumed by gateway request admission
// and response handling.
type Decision = ContentModerationDecision
