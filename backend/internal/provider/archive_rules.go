package provider

import (
	"errors"
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/provider/transfer"
)

// archive_rules.go validates imported provider records and fills missing OpenAI
// credential identity from decoded token hints.
// Provider archive entries can carry OpenAI ID tokens alongside explicitly
// supplied identity settings. Decoding those tokens supplies hints for missing
// credential keys; imported nonempty string values remain authoritative, and a
// non-string or empty value can receive a hint. The injected decoder supplies
// import metadata; authenticating upstream requests uses credentials in the
// separate request-execution path.
//
// Archive validation checks required fields, recognized credential kinds,
// Qoder's platform/type pairing, and nonnegative scheduling inputs before
// creation. Platform casing is normalized only for the Qoder pairing check.
// ID-token selection tolerates trimmed OpenAI/OAuth spelling while returning
// the original token text to the injected decoder.

// ArchiveIdentityHints holds decoded OpenAI identity metadata used to fill
// imported credentials.
type ArchiveIdentityHints struct {
	Email, // decoded account email
	PlanType, // decoded subscription plan
	ChatGPTAccountID, // decoded ChatGPT workspace identifier
	ChatGPTUserID, // decoded ChatGPT user identifier
	OrganizationID string // decoded organization identity hint
}

// ValidateArchiveProvider rejects missing required archive fields and
// unsupported credential kinds before creation. It checks Qoder's type pairing
// and negative numeric inputs; platform normalization is local to the pairing
// check.
func ValidateArchiveProvider(item transfer.DataProvider) error {
	if strings.TrimSpace(item.Name) == "" {
		return errors.New("provider name is required")
	}
	if strings.TrimSpace(item.Platform) == "" {
		return errors.New("provider platform is required")
	}
	if strings.TrimSpace(item.Type) == "" {
		return errors.New("provider type is required")
	}
	if len(item.Credentials) == 0 {
		return errors.New("provider credentials is required")
	}
	switch item.Type {
	case ProviderTypeOAuth, ProviderTypeSetupToken, ProviderTypeAPIKey, ProviderTypeUpstream,
		ProviderTypeBedrock, ProviderTypeServiceAccount, ProviderTypeCosy:
	default:
		return fmt.Errorf("provider type is invalid: %s", item.Type)
	}
	platform := strings.ToLower(strings.TrimSpace(item.Platform))
	if platform == PlatformQoder && item.Type != ProviderTypeCosy {
		return fmt.Errorf("qoder providers require %s provider type", ProviderTypeCosy)
	}
	if platform != PlatformQoder && item.Type == ProviderTypeCosy {
		return fmt.Errorf("%s provider type requires %s platform", ProviderTypeCosy, PlatformQoder)
	}
	if item.RateMultiplier != nil && *item.RateMultiplier < 0 {
		return errors.New("rate_multiplier must be >= 0")
	}
	if item.Concurrency != nil && *item.Concurrency < 0 {
		return errors.New("concurrency must be >= 0")
	}
	if item.Priority != nil && *item.Priority < 0 {
		return errors.New("priority must be >= 0")
	}
	return nil
}

// ArchiveIDToken returns the supplied string ID token for OpenAI OAuth archive
// entries. Platform and type matching ignores surrounding whitespace and case;
// the token text is returned unchanged when nonblank.
func ArchiveIDToken(item *transfer.DataProvider) string {
	if item == nil || item.Credentials == nil || strings.ToLower(strings.TrimSpace(item.Platform)) != PlatformOpenAI || strings.ToLower(strings.TrimSpace(item.Type)) != ProviderTypeOAuth {
		return ""
	}
	token, _ := item.Credentials["id_token"].(string)
	if strings.TrimSpace(token) == "" {
		return ""
	}
	return token
}

// FillArchiveIdentity fills empty or non-string imported identity keys from
// nonempty decoded hints. Nil entries, nil hints, and missing credential maps
// are skipped. Existing nonempty strings, including whitespace-only strings,
// retain their supplied values.
//
// The caller decodes the ID token before invoking this helper. Each key is
// decided independently so a token hint cannot replace an explicit
// administrator identity string while other missing keys are populated. The
// function mutates the entry's credentials and performs no token
// authentication.
func FillArchiveIdentity(item *transfer.DataProvider, hints *ArchiveIdentityHints) {
	if item == nil || hints == nil || item.Credentials == nil {
		return
	}
	set := func(key, value string) {
		if value == "" {
			return
		}
		if existing, _ := item.Credentials[key].(string); existing == "" {
			item.Credentials[key] = value
		}
	}
	set("email", hints.Email)
	set("plan_type", hints.PlanType)
	set("chatgpt_account_id", hints.ChatGPTAccountID)
	set("chatgpt_user_id", hints.ChatGPTUserID)
	set("organization_id", hints.OrganizationID)
}
