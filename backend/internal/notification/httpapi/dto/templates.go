package dto

// EmailTemplatePreviewResponse contains a rendered template preview.
type EmailTemplatePreviewResponse struct {
	Subject string `json:"subject"`
	HTML    string `json:"html"`
}

// PreviewEmailTemplateRequest contains unsaved template content to preview.
type PreviewEmailTemplateRequest struct {
	Event     string            `json:"event"`
	Locale    string            `json:"locale"`
	Subject   string            `json:"subject"`
	HTML      string            `json:"html"`
	Variables map[string]string `json:"variables,omitempty"`
}

// UpdateEmailTemplateRequest contains a template override.
type UpdateEmailTemplateRequest struct {
	Subject string `json:"subject"`
	HTML    string `json:"html"`
}

// EmailTemplateDetail describes a template for an event and locale.
type EmailTemplateDetail struct {
	Event        string   `json:"event"`
	Locale       string   `json:"locale"`
	Subject      string   `json:"subject"`
	HTML         string   `json:"html"`
	IsCustom     bool     `json:"is_custom,omitempty"`
	UpdatedAt    string   `json:"updated_at,omitempty"`
	Placeholders []string `json:"placeholders,omitempty"`
}

// EmailTemplateListResponse contains editable events, locales, and template summaries.
type EmailTemplateListResponse struct {
	Events       []EmailTemplateEventOption `json:"events"`
	Locales      []string                   `json:"locales"`
	Templates    []EmailTemplateSummary     `json:"templates,omitempty"`
	Placeholders []string                   `json:"placeholders,omitempty"`
}

// EmailTemplateSummary contains the template fields shown in the admin list.
type EmailTemplateSummary struct {
	Event     string `json:"event"`
	Locale    string `json:"locale"`
	Subject   string `json:"subject"`
	IsCustom  bool   `json:"is_custom,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// EmailTemplateEventOption describes an editable notification event.
type EmailTemplateEventOption struct {
	Value       string `json:"value"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	Category    string `json:"category,omitempty"`
	Optional    bool   `json:"optional,omitempty"`
}
