package httpapi

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// user_handler.go handles current-account profile changes, identity binding,
// and notification recipient management.
// Current-account profile writes keep ordinary display edits separate from
// sign-in email binding. Email field presence is rejected by profile update,
// while preferred locale uses raw JSON to distinguish an omitted field from a
// null request to clear the preference.
//
// Responses combine account DTO fields with provider identity summaries and
// inferred profile-source labels. Matching display values against bound
// provider hints selects the first provider in a fixed order; those labels
// describe a match, not a stored history of who last edited the profile.
// Compatibility binding maps share one map, and username, display-name, and
// nickname source labels share the same inferred pointer.

// UserHandler serves current-account profiles, sign-in identity binding, and
// notification recipients.
type UserHandler struct {
	userService  *identity.UserService             // account profile and binding service
	authService  *identity.AuthService             // sign-in email and session service
	emailService identity.NotifyVerificationSender // notification recipient proof sender
	emailCache   identity.EmailCache               // notification recipient challenge storage
}

// ChangePasswordRequest supplies current password proof and a replacement
// account password.
type ChangePasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required"`       // current account password proof
	NewPassword string `json:"new_password" binding:"required,min=6"` // replacement account plaintext password
}

// UpdateProfileRequest carries account display edits and raw locale or email
// field presence.
type UpdateProfileRequest struct {
	// PreferredLocale retains raw JSON presence. Omission leaves the preference
	// alone; null selects clearing, while a supplied string is validated by the
	// profile service.
	PreferredLocale json.RawMessage `json:"preferred_locale"` // raw account language preference JSON
	// Email retains field presence, including JSON null. UpdateProfile rejects
	// any supplied email value so credential changes use the verified binding
	// operation.
	Email json.RawMessage `json:"email"` // account or recipient email address
	// Username, AvatarURL, BalanceNotifyEnabled, and BalanceNotifyThreshold
	// retain nonnil edits, including zero or empty values. Omission and JSON null
	// decode as nil for those fields; PreferredLocale uses separate raw presence
	// handling.
	Username               *string  `json:"username"`                 // optional account display name edit
	AvatarURL              *string  `json:"avatar_url"`               // profile avatar URL value
	BalanceNotifyEnabled   *bool    `json:"balance_notify_enabled"`   // optional balance alert switch
	BalanceNotifyThreshold *float64 `json:"balance_notify_threshold"` // optional alert threshold edit
}

// UserProfileResponse combines account DTO data, identity summaries, and
// inferred profile sources.
type UserProfileResponse struct {
	dto.User[json.RawMessage]        // ordinary account response fields
	AvatarURL                 string `json:"avatar_url,omitempty"` // profile avatar URL value
	// AvatarSource and UsernameSource come from matching current values against
	// bound provider hints. DisplayNameSource and NicknameSource share
	// UsernameSource, and ProfileSources reuses the selected source pointers.
	AvatarSource      *UserProfileSourceContext            `json:"avatar_source,omitempty"`       // inferred profile avatar provider
	UsernameSource    *UserProfileSourceContext            `json:"username_source,omitempty"`     // inferred account name provider
	DisplayNameSource *UserProfileSourceContext            `json:"display_name_source,omitempty"` // compatibility display-name provider label
	NicknameSource    *UserProfileSourceContext            `json:"nickname_source,omitempty"`     // compatibility nickname provider label
	ProfileSources    map[string]*UserProfileSourceContext `json:"profile_sources,omitempty"`     // inferred provider labels by field
	Identities        identity.UserIdentitySummarySet      `json:"identities"`                    // loaded provider identity summary set
	// AuthBindings and IdentityBindings reference the same newly allocated
	// provider-summary map. Mutating either map changes the other; nested summary
	// data retains the domain value sharing.
	AuthBindings     map[string]identity.UserIdentitySummary `json:"auth_bindings"`     // provider summaries by type
	IdentityBindings map[string]identity.UserIdentitySummary `json:"identity_bindings"` // compatibility provider summary map
	EmailBound       bool                                    `json:"email_bound"`       // email identity presence flag
	LinuxDoBound     bool                                    `json:"linuxdo_bound"`     // LinuxDo identity presence flag
	OIDCBound        bool                                    `json:"oidc_bound"`        // OIDC identity presence flag
	WeChatBound      bool                                    `json:"wechat_bound"`      // WeChat identity presence flag
	DingTalkBound    bool                                    `json:"dingtalk_bound"`    // DingTalk identity presence flag
}

// UserProfileSourceContext labels the provider whose current hint matches a
// profile value.
type UserProfileSourceContext struct {
	// Provider and Source carry the same trimmed matched provider name.
	// BuildUserProfileSourceContext emits both current and compatibility labels,
	// while a blank provider yields no source context.
	Provider string `json:"provider,omitempty"` // matched profile provider label
	Source   string `json:"source,omitempty"`   // compatibility provider source label
}

// StartIdentityBindingRequest selects a third-party provider and the requested
// local return path.
type StartIdentityBindingRequest struct {
	Provider   string `json:"provider" binding:"required"` // requested identity binding provider
	RedirectTo string `json:"redirect_to"`                 // requested local binding return path
}

// BindEmailIdentityRequest supplies verified email proof and password input
// for account binding.
type BindEmailIdentityRequest struct {
	Email      string `json:"email" binding:"required,email"` // account or recipient email address
	VerifyCode string `json:"verify_code" binding:"required"` // email identity verification code
	Password   string `json:"password" binding:"required"`    // submitted account password input
}

// SendEmailBindingCodeRequest names the email address submitted for sign-in
// binding proof.
type SendEmailBindingCodeRequest struct {
	Email string `json:"email" binding:"required,email"` // account or recipient email address
}

// SendNotifyEmailCodeRequest names the additional notification address whose
// proof is requested.
type SendNotifyEmailCodeRequest struct {
	Email string `json:"email" binding:"required,email"` // account or recipient email address
}

// VerifyNotifyEmailRequest supplies a recipient address and its six-character
// verification code.
type VerifyNotifyEmailRequest struct {
	Email string `json:"email" binding:"required,email"` // account or recipient email address
	Code  string `json:"code" binding:"required,len=6"`  // notification recipient verification code
}

// RemoveNotifyEmailRequest identifies the saved notification recipient
// selected for removal.
type RemoveNotifyEmailRequest struct {
	Email string `json:"email" binding:"required,email"` // account or recipient email address
}

// ToggleNotifyEmailRequest sets delivery eligibility for the supplied saved
// notification recipient.
type ToggleNotifyEmailRequest struct {
	Email    string `json:"email" binding:"required,email"` // account or recipient email address
	Disabled bool   `json:"disabled"`                       // recipient delivery disabled flag
}

// NewUserHandler retains account, authentication, mail-sending, and
// challenge-storage dependencies for current-user requests. Email binding
// checks auth availability, while notification operations pass sender and
// cache capabilities to the user service.
func NewUserHandler(users *identity.UserService, auth *identity.AuthService, email identity.NotifyVerificationSender, cache identity.EmailCache) *UserHandler {
	return &UserHandler{users, auth, email, cache}
}

// GetProfile writes current account data and provider identity summaries for
// the authenticated subject. Account or summary read failures stop the
// response before its profile and compatibility fields are assembled.
func (h *UserHandler) GetProfile(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	userData, err := h.userService.GetProfile(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	profileResp, err := h.BuildUserProfileResponse(c.Request.Context(), subject.UserID, userData)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, profileResp)
}

// ChangePassword validates current and replacement password input for the
// authenticated account. The user service owns credential checks and
// persistence; a successful result writes the panel completion message.
func (h *UserHandler) ChangePassword(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	var req ChangePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	svcReq := identity.ChangePasswordRequest{
		CurrentPassword: req.OldPassword,
		NewPassword:     req.NewPassword,
	}
	err := h.userService.ChangePassword(c.Request.Context(), subject.UserID, svcReq)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "Password changed successfully"})
}

// UpdateProfile validates display edits for the authenticated account and
// rejects any email field presence. Preferred locale uses raw JSON so omission
// retains a value and null requests clearing.
//
// Pointer edits pass zero and empty values through to the profile service.
// After a successful account mutation, identity-summary lookup assembles the
// response; a later lookup failure returns an error even though the profile
// write already completed. The handler keeps sign-in email and password proof
// in their dedicated operations.
func (h *UserHandler) UpdateProfile(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	var req UpdateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if len(req.Email) > 0 {
		// Primary email changes affect sign-in and require the verified email binding flow.
		response.ErrorFrom(c, identity.ErrProfileEmailChangeForbidden)
		return
	}

	var preferredLocale *string
	if len(req.PreferredLocale) > 0 {
		if err := json.Unmarshal(req.PreferredLocale, &preferredLocale); err != nil {
			response.ErrorFrom(c, apperror.BadRequest("INVALID_LOCALE", "Language is not supported."))
			return
		}
	}
	svcReq := identity.UpdateProfileRequest{
		ClearPreferredLocale:   len(req.PreferredLocale) > 0 && preferredLocale == nil,
		PreferredLocale:        preferredLocale,
		Username:               req.Username,
		AvatarURL:              req.AvatarURL,
		BalanceNotifyEnabled:   req.BalanceNotifyEnabled,
		BalanceNotifyThreshold: req.BalanceNotifyThreshold,
	}
	updatedUser, err := h.userService.UpdateProfile(c.Request.Context(), subject.UserID, svcReq)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	profileResp, err := h.BuildUserProfileResponse(c.Request.Context(), subject.UserID, updatedUser)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, profileResp)
}

// StartIdentityBinding returns the provider binding-start URL for an
// authenticated account request. The user service validates provider and
// return-path choices; its result prepares browser navigation to the protected
// binding flow.
func (h *UserHandler) StartIdentityBinding(c *gin.Context) {
	if _, ok := authctx.GetAuthSubjectFromContext(c); !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	var req StartIdentityBindingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	result, err := h.userService.PrepareIdentityBindingStart(c.Request.Context(), identity.StartUserIdentityBindingRequest{
		Provider:   req.Provider,
		RedirectTo: req.RedirectTo,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, result)
}

// BindEmailIdentity submits email proof and password input for the
// authenticated account binding operation. Missing authentication service is
// an internal error; a later profile-summary failure can follow a completed
// binding write.
func (h *UserHandler) BindEmailIdentity(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if h.authService == nil {
		response.InternalError(c, "Auth service not configured")
		return
	}

	var req BindEmailIdentityRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	updatedUser, err := h.authService.BindEmailIdentity(
		c.Request.Context(),
		subject.UserID,
		req.Email,
		req.VerifyCode,
		req.Password,
	)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	profileResp, err := h.BuildUserProfileResponse(c.Request.Context(), subject.UserID, updatedUser)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, profileResp)
}

// UnbindIdentity removes a permitted provider from the current account and
// optionally requests refresh-session revocation after an actual removal.
// Profile response lookup follows those operations, so later revocation or
// read errors can follow completed identity deletion.
func (h *UserHandler) UnbindIdentity(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	updatedUser, unbound, err := h.userService.UnbindUserAuthProviderWithResult(
		c.Request.Context(),
		subject.UserID,
		c.Param("provider"),
	)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// Refresh-session cleanup follows actual provider removal. A cleanup error
	// returns to the client after that removal, so the response is not a rollback
	// signal for identity deletion.
	if unbound && h.authService != nil {
		if err := h.authService.RevokeAllUserTokens(c.Request.Context(), subject.UserID); err != nil {
			response.ErrorFrom(c, err)
			return
		}
	}

	profileResp, err := h.BuildUserProfileResponse(c.Request.Context(), subject.UserID, updatedUser)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, profileResp)
}

// SendEmailBindingCode requests sign-in email proof for the authenticated
// account and supplied address. Authentication must be configured;
// Accept-Language is forwarded, and service validation failures stop the
// success response.
func (h *UserHandler) SendEmailBindingCode(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if h.authService == nil {
		response.InternalError(c, "Auth service not configured")
		return
	}

	var req SendEmailBindingCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	if err := h.authService.SendEmailIdentityBindCode(c.Request.Context(), subject.UserID, req.Email, c.GetHeader("Accept-Language")); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "Verification code sent successfully"})
}

// SendNotifyEmailCode requests proof for an additional notification recipient
// of the authenticated account. The user service receives the sender,
// challenge cache, address, and Accept-Language before the panel acceptance
// message is returned.
func (h *UserHandler) SendNotifyEmailCode(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	var req SendNotifyEmailCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	err := h.userService.SendNotifyEmailCode(c.Request.Context(), subject.UserID, req.Email, h.emailService, h.emailCache, c.GetHeader("Accept-Language"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "Verification code sent successfully"})
}

// VerifyNotifyEmail validates recipient proof and adds the address through the
// user service. Account and identity-summary reads follow the mutation, so
// their failures can return errors after the recipient was saved.
func (h *UserHandler) VerifyNotifyEmail(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	var req VerifyNotifyEmailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	err := h.userService.VerifyAndAddNotifyEmail(c.Request.Context(), subject.UserID, req.Email, req.Code, h.emailCache)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	updatedUser, err := h.userService.GetByID(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	profileResp, err := h.BuildUserProfileResponse(c.Request.Context(), subject.UserID, updatedUser)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, profileResp)
}

// RemoveNotifyEmail removes the submitted saved recipient from the
// authenticated account. The handler reloads account and identity summaries
// for its profile response, retaining any later read failure after successful
// removal.
func (h *UserHandler) RemoveNotifyEmail(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	var req RemoveNotifyEmailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	err := h.userService.RemoveNotifyEmail(c.Request.Context(), subject.UserID, req.Email)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	updatedUser, err := h.userService.GetByID(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	profileResp, err := h.BuildUserProfileResponse(c.Request.Context(), subject.UserID, updatedUser)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, profileResp)
}

// ToggleNotifyEmail sets recipient delivery eligibility for the authenticated
// account using the submitted disabled flag. The handler reloads profile data
// afterward; later reads may fail after the saved recipient was updated.
func (h *UserHandler) ToggleNotifyEmail(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	var req ToggleNotifyEmailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	err := h.userService.ToggleNotifyEmail(c.Request.Context(), subject.UserID, req.Email, req.Disabled)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	updatedUser, err := h.userService.GetByID(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	profileResp, err := h.BuildUserProfileResponse(c.Request.Context(), subject.UserID, updatedUser)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, profileResp)
}

// BuildUserProfileResponse loads provider summaries for the supplied account
// snapshot and builds its response. Summary-read failures stop conversion,
// while successful mapping retains the documented account pointer and
// compatibility-map sharing.
func (h *UserHandler) BuildUserProfileResponse(ctx context.Context, userID int64, user *identity.User) (UserProfileResponse, error) {
	identities, err := h.userService.GetProfileIdentitySummaries(ctx, userID, user)
	if err != nil {
		return UserProfileResponse{}, err
	}
	return UserProfileResponseFromService(user, identities), nil
}

// UserProfileResponseFromService combines account DTO fields and supplied
// identity summaries, returning an empty response for a nil user. Both binding
// maps share their allocation, and name-source compatibility fields reuse the
// inferred source pointer.
func UserProfileResponseFromService(user *identity.User, identities identity.UserIdentitySummarySet) UserProfileResponse {
	base := dto.UserFromIdentity[json.RawMessage](user, nil)
	if base == nil {
		return UserProfileResponse{}
	}
	bindings := UserProfileBindingMap(identities)
	profileSources, avatarSource, usernameSource := InferUserProfileSources(user, identities)
	return UserProfileResponse{
		User:              *base,
		AvatarURL:         user.AvatarURL,
		AvatarSource:      avatarSource,
		UsernameSource:    usernameSource,
		DisplayNameSource: usernameSource,
		NicknameSource:    usernameSource,
		ProfileSources:    profileSources,
		Identities:        identities,
		AuthBindings:      bindings,
		IdentityBindings:  bindings,
		EmailBound:        identities.Email.Bound,
		LinuxDoBound:      identities.LinuxDo.Bound,
		OIDCBound:         identities.OIDC.Bound,
		WeChatBound:       identities.WeChat.Bound,
		DingTalkBound:     identities.DingTalk.Bound,
	}
}

// UserProfileBindingMap allocates the email, LinuxDo, OIDC, WeChat, and
// DingTalk summary map. The summary structs are copied by value, while any
// referenced data within those values retains its original sharing.
func UserProfileBindingMap(identities identity.UserIdentitySummarySet) map[string]identity.UserIdentitySummary {
	return map[string]identity.UserIdentitySummary{
		"email":    identities.Email,
		"linuxdo":  identities.LinuxDo,
		"oidc":     identities.OIDC,
		"wechat":   identities.WeChat,
		"dingtalk": identities.DingTalk,
	}
}

// InferUserProfileSources returns inferred avatar and name provider labels
// plus their field map. Nil account input returns nil results, and blank
// current values do not select a source.
//
// The first exact trimmed hint match among bound LinuxDo, OIDC, WeChat, and
// DingTalk summaries wins separately for avatar and username. Username
// matching reads provider DisplayName, and its pointer is reused for
// display_name and nickname labels. These matches describe current value
// equality; callers cannot use them as persisted editing provenance.
func InferUserProfileSources(user *identity.User, identities identity.UserIdentitySummarySet) (
	map[string]*UserProfileSourceContext,
	*UserProfileSourceContext,
	*UserProfileSourceContext,
) {
	if user == nil {
		return nil, nil, nil
	}

	thirdParty := ThirdPartyIdentityProviders(identities)
	var avatarSource *UserProfileSourceContext
	avatarValue := strings.TrimSpace(user.AvatarURL)
	// Several providers can expose the same avatar URL. The ordered
	// bound-provider list makes the first matching current hint the displayed
	// avatar source.
	for _, summary := range thirdParty {
		if avatarValue != "" && avatarValue == strings.TrimSpace(summary.AvatarURL) {
			avatarSource = BuildUserProfileSourceContext(summary.Provider)
			break
		}
	}

	usernameValue := strings.TrimSpace(user.Username)
	var usernameSource *UserProfileSourceContext
	// Infer the account name separately from avatar matching. Different current
	// hint matches can therefore select different bound providers for the two
	// displayed source fields.
	for _, summary := range thirdParty {
		if usernameValue != "" && usernameValue == strings.TrimSpace(summary.DisplayName) {
			usernameSource = BuildUserProfileSourceContext(summary.Provider)
			break
		}
	}

	profileSources := map[string]*UserProfileSourceContext{}
	if avatarSource != nil {
		profileSources["avatar"] = avatarSource
	}
	if usernameSource != nil {
		profileSources["username"] = usernameSource
		profileSources["display_name"] = usernameSource
		profileSources["nickname"] = usernameSource
	}
	if len(profileSources) == 0 {
		return nil, avatarSource, usernameSource
	}
	return profileSources, avatarSource, usernameSource
}

// ThirdPartyIdentityProviders returns bound LinuxDo, OIDC, WeChat, and
// DingTalk summaries in that fixed order. It allocates an outer slice while
// retaining referenced summary data; source inference uses the first matching
// returned entry.
func ThirdPartyIdentityProviders(identities identity.UserIdentitySummarySet) []identity.UserIdentitySummary {
	out := make([]identity.UserIdentitySummary, 0, 3)
	for _, summary := range []identity.UserIdentitySummary{identities.LinuxDo, identities.OIDC, identities.WeChat, identities.DingTalk} {
		if summary.Bound {
			out = append(out, summary)
		}
	}
	return out
}

// BuildUserProfileSourceContext returns a new source label using trimmed
// provider text for both fields. Empty provider text returns nil, so callers
// omit an unusable inferred source from response maps.
func BuildUserProfileSourceContext(provider string) *UserProfileSourceContext {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return nil
	}
	return &UserProfileSourceContext{
		Provider: provider,
		Source:   provider,
	}
}
