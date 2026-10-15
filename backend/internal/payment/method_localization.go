package payment

import (
	"encoding/json"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// method_localization.go prepares EasyPay custom-method translations while
// matching prior content by stable method identity.
// Custom payment methods need stable content IDs so changing list
// order or display text keeps translations tied to the same entry.
// Older entries without an ID use their payment type as that identity
// for both current and previous lists. Duplicate current IDs are
// rejected before any configuration string is replaced.
//
// Localized display names are prepared against the previous entry's
// content revision, then the prepared source text becomes DisplayName.
// Source and submitted translations must be nonblank and fit the
// code-point limit checked here. Only a nonempty prepared list is
// serialized back to config; malformed current JSON and preparation
// errors are returned, while errors decoding previous compatibility
// data are ignored.

// prepareMethodNames validates and prepares custom-method display
// localization, updating config only after successful serialization.
// Nil config and an empty method list produce no write. Callers own
// the mutable map and separately validate provider payment types.
//
// Missing IDs inherit Type before previous-content matching and duplicate
// checks. A localization update uses the old entry's recorded content
// or legacy DisplayName and delegates revision handling to locale.Prepare.
// The helper installs the prepared source and content on its local
// decoded entry, preserving stable identity when the input list is
// reordered.
func prepareMethodNames(config, previous map[string]string) error {
	if config == nil {
		return nil
	}
	var methods, old []ConfigEasyPayCustomMethodConfig
	if raw := strings.TrimSpace(config["customMethods"]); raw != "" {
		if err := json.Unmarshal([]byte(raw), &methods); err != nil {
			return err
		}
	}
	if previous != nil {
		// Previous configuration is compatibility input. Ignore its decode
		// error and use any retained entries for the prior-content lookup;
		// validation of the submitted current JSON still returns its error.
		_ = json.Unmarshal([]byte(previous["customMethods"]), &old)
	}
	byID := map[string]ConfigEasyPayCustomMethodConfig{}
	for _, item := range old {
		// Use Type for a legacy entry's missing ID so current entries keyed
		// by that same Type can recover the prior display-name content
		// before revision validation.
		if item.ID == "" {
			item.ID = item.Type
		}
		byID[item.ID] = item
	}
	seen := map[string]bool{}
	for i := range methods {
		item := &methods[i]
		// Normalize the current identity before duplicate detection and prior
		// content lookup, so omitted IDs participate in the same identity
		// rules as supplied IDs.
		if item.ID == "" {
			item.ID = item.Type
		}
		if seen[item.ID] {
			return apperror.BadRequest("DUPLICATE_CONTENT_ID", "Content ID is duplicated.")
		}
		seen[item.ID] = true
		if item.DisplayNameLocalization == nil {
			continue
		}
		prior := byID[item.ID]
		current := locale.Original(prior.DisplayName)
		if prior.DisplayNameLocalization != nil {
			current = prior.DisplayNameLocalization.Content
		}
		next, err := locale.Prepare(current, *item.DisplayNameLocalization, func(value string) error {
			// Limit each validated display name to one hundred Unicode code points.
			if strings.TrimSpace(value) == "" || len([]rune(value)) > 100 {
				return apperror.BadRequest("PAYMENT_METHOD_NAME_REQUIRED", "Payment method name is required and must not exceed 100 characters.")
			}
			return nil
		})
		if err != nil {
			return err
		}
		item.DisplayNameLocalization = &locale.Update[string]{Content: next}
		item.DisplayName = next.Source
	}
	if len(methods) == 0 {
		return nil
	}
	data, err := json.Marshal(methods)
	if err != nil {
		return err
	}
	config["customMethods"] = string(data)
	return nil
}
