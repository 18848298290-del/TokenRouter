package content

import "github.com/TokenFlux/TokenRouter/internal/pkg/locale"

// legal.go defines localized login-agreement documents and their stable
// identifiers.
// Login agreements keep a stable document ID while administrators edit language
// versions of their title and Markdown body together. Read responses carry the
// selected copy and language resolution, while updates carry localization edits.

// LoginAgreementCopy stores an agreement's title and body as one language version.
type LoginAgreementCopy struct {
	Title     string `json:"title"`      // displayed agreement title
	ContentMD string `json:"content_md"` // agreement Markdown body
}

// LoginAgreementDocument identifies an agreement and carries its selected localized copy.
type LoginAgreementDocument struct {
	// Resolution describes the language selected for Title and ContentMD on
	// reads; localization input is handled separately when administrators submit edits.
	Resolution *locale.Resolution `json:"localization_resolution,omitempty"`
	// Localization carries requested source and translation edits for the
	// agreement; validation applies those changes before resolving a displayed copy.
	Localization *locale.Update[LoginAgreementCopy] `json:"localization,omitempty"`
	ID           string                             `json:"id"`         // stable agreement identifier
	Title        string                             `json:"title"`      // resolved agreement title
	ContentMD    string                             `json:"content_md"` // resolved Markdown body
}
