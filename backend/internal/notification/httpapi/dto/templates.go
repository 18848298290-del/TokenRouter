package dto

// templates.go defines administrator request and response shapes for email
// template editing and previews.
// Administrator template views separate event choices, list summaries, complete
// editable content, and rendered previews. Save times are serialized as display
// strings, and placeholder collections let the editor use the same admitted
// variable names as notification rendering.

// EmailTemplatePreviewResponse contains rendered subject and HTML values for administrator inspection.
type EmailTemplatePreviewResponse struct {
	Subject string `json:"subject"` // template subject text
	HTML    string `json:"html"`    // template HTML content
}

// PreviewEmailTemplateRequest supplies unsaved template copy and variable replacements for administrator rendering.
type PreviewEmailTemplateRequest struct {
	Event  string `json:"event"`  // notification event identifier
	Locale string `json:"locale"` // requested template language
	// Subject and HTML can each be blank in a preview request. The service
	// independently fills blank fields from the selected event/language template
	// before validating and rendering the combined content.
	Subject string `json:"subject"` // template subject text
	HTML    string `json:"html"`    // template HTML content
	// Variables replaces matching sample names during preview preparation;
	// rendering copies these values into its working map before applying substitutions.
	Variables map[string]string `json:"variables,omitempty"` // preview variable replacements
}

// UpdateEmailTemplateRequest supplies subject and HTML copy for one persisted template override.
type UpdateEmailTemplateRequest struct {
	Subject string `json:"subject"` // template subject text
	HTML    string `json:"html"`    // template HTML content
}

// EmailTemplateDetail describes a template for an event and locale.
type EmailTemplateDetail struct {
	Event    string `json:"event"`               // notification event identifier
	Locale   string `json:"locale"`              // selected template language
	Subject  string `json:"subject"`             // template subject text
	HTML     string `json:"html"`                // template HTML content
	IsCustom bool   `json:"is_custom,omitempty"` // selected override indicator
	// UpdatedAt is empty when the selected template has no saved timestamp.
	// The detail mapper otherwise formats whole seconds with the timestamp's offset.
	UpdatedAt string `json:"updated_at,omitempty"` // formatted save timestamp
	// Placeholders retains the service template's returned slice in detail
	// mapping; consumers copy it before editing metadata independently of that template value.
	Placeholders []string `json:"placeholders,omitempty"` // admitted variable names
}

// EmailTemplateListResponse contains editable events, locales, and template summaries.
type EmailTemplateListResponse struct {
	Events       []EmailTemplateEventOption `json:"events"`                 // ordered event choices
	Locales      []string                   `json:"locales"`                // ordered supported languages
	Templates    []EmailTemplateSummary     `json:"templates,omitempty"`    // event-language template summaries
	Placeholders []string                   `json:"placeholders,omitempty"` // admitted variable names
}

// EmailTemplateSummary contains ordered template metadata shown in the administrator event/language list.
type EmailTemplateSummary struct {
	Event    string `json:"event"`               // notification event identifier
	Locale   string `json:"locale"`              // selected template language
	Subject  string `json:"subject"`             // template subject text
	IsCustom bool   `json:"is_custom,omitempty"` // selected override indicator
	// UpdatedAt is empty when the selected template has no saved timestamp.
	// Summary mapping otherwise formats whole seconds with the timestamp's offset.
	UpdatedAt string `json:"updated_at,omitempty"` // formatted save timestamp
}

// EmailTemplateEventOption describes an editable event and whether recipient preference handling applies.
type EmailTemplateEventOption struct {
	Value       string `json:"value"`                 // notification event identifier
	Label       string `json:"label,omitempty"`       // administrator event label
	Description string `json:"description,omitempty"` // administrator event explanation
	Category    string `json:"category,omitempty"`    // administrator event category
	Optional    bool   `json:"optional,omitempty"`    // recipient preference eligibility
}
