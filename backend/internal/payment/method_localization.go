package payment

import (
	"encoding/json"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// prepareMethodNames validates translations by stable entry ID; payment configuration validates upstream types.
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
		_ = json.Unmarshal([]byte(previous["customMethods"]), &old)
	}
	byID := map[string]ConfigEasyPayCustomMethodConfig{}
	for _, item := range old {
		if item.ID == "" {
			item.ID = item.Type
		}
		byID[item.ID] = item
	}
	seen := map[string]bool{}
	for i := range methods {
		item := &methods[i]
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
