package transfer

import "github.com/TokenFlux/TokenRouter/internal/egress"

type CRSExportResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	Message string `json:"message"`
	Data    struct {
		ExportedAt               string                       `json:"exportedAt"`
		ClaudeProviders          []CRSClaudeProvider          `json:"claudeAccounts"`
		ClaudeConsoleProviders   []CRSConsoleProvider         `json:"claudeConsoleAccounts"`
		OpenAIOAuthProviders     []CRSOpenAIOAuthProvider     `json:"openaiOAuthAccounts"`
		OpenAIResponsesProviders []CRSOpenAIResponsesProvider `json:"openaiResponsesAccounts"`
		GeminiOAuthProviders     []CRSGeminiOAuthProvider     `json:"geminiOAuthAccounts"`
		GeminiAPIKeyProviders    []CRSGeminiAPIKeyProvider    `json:"geminiApiKeyAccounts"`
	} `json:"data"`
}

// CRSProxy uses the egress import representation and its JSON field names.
type CRSProxy = egress.CRSProxySpec

type CRSClaudeProvider struct {
	Kind        string         `json:"kind"`
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Platform    string         `json:"platform"`
	AuthType    string         `json:"authType"` // oauth/setup-token
	IsActive    bool           `json:"isActive"`
	Schedulable bool           `json:"schedulable"`
	Priority    int            `json:"priority"`
	Status      string         `json:"status"`
	Proxy       *CRSProxy      `json:"proxy"`
	Credentials map[string]any `json:"credentials"`
	Extra       map[string]any `json:"extra"`
}

type CRSConsoleProvider struct {
	Kind               string         `json:"kind"`
	ID                 string         `json:"id"`
	Name               string         `json:"name"`
	Description        string         `json:"description"`
	Platform           string         `json:"platform"`
	IsActive           bool           `json:"isActive"`
	Schedulable        bool           `json:"schedulable"`
	Priority           int            `json:"priority"`
	Status             string         `json:"status"`
	MaxConcurrentTasks int            `json:"maxConcurrentTasks"`
	Proxy              *CRSProxy      `json:"proxy"`
	Credentials        map[string]any `json:"credentials"`
}

type CRSOpenAIResponsesProvider struct {
	Kind        string         `json:"kind"`
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Platform    string         `json:"platform"`
	IsActive    bool           `json:"isActive"`
	Schedulable bool           `json:"schedulable"`
	Priority    int            `json:"priority"`
	Status      string         `json:"status"`
	Proxy       *CRSProxy      `json:"proxy"`
	Credentials map[string]any `json:"credentials"`
	Extra       map[string]any `json:"extra"`
}

type CRSOpenAIOAuthProvider struct {
	Kind        string         `json:"kind"`
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Platform    string         `json:"platform"`
	AuthType    string         `json:"authType"` // oauth
	IsActive    bool           `json:"isActive"`
	Schedulable bool           `json:"schedulable"`
	Priority    int            `json:"priority"`
	Status      string         `json:"status"`
	Proxy       *CRSProxy      `json:"proxy"`
	Credentials map[string]any `json:"credentials"`
	Extra       map[string]any `json:"extra"`
}

type CRSGeminiOAuthProvider struct {
	Kind        string         `json:"kind"`
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Platform    string         `json:"platform"`
	AuthType    string         `json:"authType"` // oauth
	IsActive    bool           `json:"isActive"`
	Schedulable bool           `json:"schedulable"`
	Priority    int            `json:"priority"`
	Status      string         `json:"status"`
	Proxy       *CRSProxy      `json:"proxy"`
	Credentials map[string]any `json:"credentials"`
	Extra       map[string]any `json:"extra"`
}

type CRSGeminiAPIKeyProvider struct {
	Kind        string         `json:"kind"`
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Platform    string         `json:"platform"`
	IsActive    bool           `json:"isActive"`
	Schedulable bool           `json:"schedulable"`
	Priority    int            `json:"priority"`
	Status      string         `json:"status"`
	Proxy       *CRSProxy      `json:"proxy"`
	Credentials map[string]any `json:"credentials"`
	Extra       map[string]any `json:"extra"`
}
