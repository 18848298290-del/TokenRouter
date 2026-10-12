package content

import "github.com/TokenFlux/TokenRouter/internal/pkg/locale"

// LoginAgreementCopy stores an agreement's title and body as one language version.
type LoginAgreementCopy struct {
	Title     string `json:"title"`
	ContentMD string `json:"content_md"`
}

type LoginAgreementDocument struct {
	Resolution   *locale.Resolution                 `json:"localization_resolution,omitempty"`
	Localization *locale.Update[LoginAgreementCopy] `json:"localization,omitempty"`
	ID           string                             `json:"id"`
	Title        string                             `json:"title"`
	ContentMD    string                             `json:"content_md"`
}
