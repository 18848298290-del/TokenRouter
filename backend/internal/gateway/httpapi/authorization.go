package httpapi

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
)

// AuthorizationSubscriptions reads and validates subscriptions, including usage window maintenance.
type AuthorizationSubscriptions interface {
	admission.SubscriptionReader
	admission.SubscriptionValidator
}

// APIKeyAuthorizationOptions configures authentication and binding of the authorized key to the request.
type APIKeyAuthorizationOptions struct {
	Authentication keyhttp.AuthenticationOptions
	BindLegacyKey  func(*gin.Context, *apikey.APIKey)
}

// APIKeyIDFromContext returns the authenticated key ID, or zero for a missing or unexpected value.
func APIKeyIDFromContext(c *gin.Context) int64 {
	if c == nil {
		return 0
	}
	v, exists := c.Get("api_key")
	if !exists {
		return 0
	}
	apiKey, ok := v.(*apikey.APIKey)
	if !ok || apiKey == nil {
		return 0
	}
	return apiKey.ID
}

func (o APIKeyAuthorizationOptions) loaded(c *gin.Context, k *apikey.APIKey) {
	if o.Authentication.Loaded != nil {
		o.Authentication.Loaded(c, k)
	}
}

func (o APIKeyAuthorizationOptions) business(c *gin.Context) {
	if o.Authentication.BusinessLimited != nil {
		o.Authentication.BusinessLimited(c, "api_key_group_unavailable")
	}
}

func (o APIKeyAuthorizationOptions) reject(c *gin.Context, reason string) {
	if o.Authentication.Rejected != nil {
		o.Authentication.Rejected(c, reason)
	}
}

func (o APIKeyAuthorizationOptions) bind(c *gin.Context, k *apikey.APIKey) {
	c.Set("gateway_effective_key", k)
	if o.BindLegacyKey != nil {
		o.BindLegacyKey(c, k)
	}
}

// EffectiveAPIKey returns the final key view saved after admission.
func EffectiveAPIKey(c *gin.Context) (*apikey.APIKey, bool) {
	value, ok := c.Get("gateway_effective_key")
	if !ok {
		return nil, false
	}
	key, ok := value.(*apikey.APIKey)
	return key, ok
}

func NewAPIKeyAuthorization(apiKeyService *apikey.APIKeyService, subscriptionService AuthorizationSubscriptions, options APIKeyAuthorizationOptions) gin.HandlerFunc {
	options.Authentication.Google = false
	return func(c *gin.Context) {
		access, ok := keyhttp.Authenticate(c, apiKeyService, options.Authentication)
		if !ok {
			return
		}
		apiKey := access.KeyView()
		var err error

		apiKey, err = ResolveCompositeAPIKeyRequest(c, apiKeyService, apiKey)
		if err != nil {
			AbortCompositeKeyError(c, err)
			return
		}
		options.loaded(c, apiKey)
		if abortAuthorizationGroupUnavailable(c, apiKey, options) {
			return
		}
		if abortAuthorizationGroupNotAllowed(c, apiKey, options) {
			return
		}
		// Bind the effective payer and Fast policy after final group authorization.
		requestAccess := *access
		requestAccess.PayerUserID = apiKey.User.ID
		ctx := apikey.WithAccessSnapshot(c.Request.Context(), requestAccess)
		c.Request = c.Request.WithContext(apikey.WithFastModePolicy(ctx, apiKey.FastModePolicy))
		ApplyAPIKeyModelRedirect(c, apiKey)
		// Batch management remains accessible after quota exhaustion so users can retrieve results or cancel work.
		skipBilling := IsAPIKeyUsageRequest(c.Request.Method, c.Request.URL.Path) ||
			IsBatchImageBillingBypassRequest(c.Request.Method, c.Request.URL.Path) ||
			(apiKey.IsComposite && IsGrokVideoTaskRead(c.Request.Method, c.Request.URL.Path))

		billingContext, billingErr := admission.ResolveFundingFromKey(c.Request.Context(), apiKey, subscriptionService, !skipBilling)
		if billingErr != nil {
			switch {
			case errors.Is(billingErr, billing.ErrPreferredSubscriptionGroup):
				keyhttp.AbortWithError(c, 403, "PREFERRED_SUBSCRIPTION_GROUP_NOT_ALLOWED", billingErr.Error())
			case errors.Is(billingErr, billing.ErrPreferredSubscriptionInvalid):
				keyhttp.AbortWithError(c, 403, "PREFERRED_SUBSCRIPTION_INVALID", billingErr.Error())
			default:
				keyhttp.AbortWithError(c, 500, "INTERNAL_ERROR", "Failed to validate subscription")
			}
			return
		}
		var subscription *billing.UserSubscription
		if billingContext != nil {
			subscription = billingContext.Subscription
		}

		// Check funding and key limits for consumption endpoints.

		if !skipBilling {
			checked, failure := admission.CheckConsumption(c.Request.Context(), admission.ConsumptionInput{Status: apiKey.Status, Limits: apiKey, Balance: apiKey.User.Balance, Subscription: subscription}, subscriptionService)
			if failure != nil {
				switch failure.Kind {
				case admission.KeyQuotaExceeded:
					AbortAPIKeyQuotaError(c)
				case admission.KeyExpired:
					keyhttp.AbortWithError(c, 403, "API_KEY_EXPIRED", "The API key has expired.")
				case admission.MaintenanceFailed:
					keyhttp.AbortWithError(c, 500, "SUBSCRIPTION_MAINTENANCE_FAILED", "Failed to maintain subscription usage windows")
				case admission.SubscriptionLimitExceeded:
					keyhttp.AbortWithError(c, 429, "USAGE_LIMIT_EXCEEDED", failure.Error())
				case admission.SubscriptionInvalid:
					keyhttp.AbortWithError(c, 403, "SUBSCRIPTION_INVALID", failure.Error())
				case admission.InsufficientBalance:
					keyhttp.AbortWithError(c, 403, "INSUFFICIENT_BALANCE", "Insufficient account balance")
				}
				return
			}
			subscription = checked
			if billingContext != nil && subscription != nil {
				billingContext.Subscription = subscription
				billingContext.Available = true
			}
		}

		if billingContext != nil {
			c.Set("api_key_billing", billingContext)
		}
		if subscription != nil {
			c.Set("subscription", subscription)
		}
		c.Set("api_key", apiKey)
		c.Set("user", authctx.AuthSubject{
			UserID:      apiKey.User.ID,
			Concurrency: apiKey.User.Concurrency,
		})
		c.Set("user_role", apiKey.User.Role)
		options.bind(c, apiKey)
		_ = apiKeyService.TouchLastUsed(c.Request.Context(), apiKey.ID)

		keyhttp.SetAccessPrincipal(c, access)
		release, ok := acquireKeyRequest(c, apiKeyService, apiKey, options)
		if !ok {
			return
		}
		defer release()
		c.Next()
	}
}

// NewGoogleAPIKeyAuthorization authenticates Google endpoints and reports failures in Google format.
func NewGoogleAPIKeyAuthorization(apiKeyService *apikey.APIKeyService, subscriptionService AuthorizationSubscriptions, options APIKeyAuthorizationOptions) gin.HandlerFunc {
	options.Authentication.Google = true
	return func(c *gin.Context) {
		access, ok := keyhttp.Authenticate(c, apiKeyService, options.Authentication)
		if !ok {
			return
		}
		apiKey := access.KeyView()
		var err error

		apiKey, err = ResolveCompositeAPIKeyRequest(c, apiKeyService, apiKey)
		if err != nil {
			AbortCompositeKeyGoogleError(c, err)
			return
		}
		options.loaded(c, apiKey)
		if code, message, ok := admission.GroupAvailable(apiKey); !ok {
			options.business(c)
			if code == "GROUP_DELETED" {
				options.reject(c, "group_deleted")
			} else {
				options.reject(c, "group_disabled")
			}
			keyhttp.AbortGoogleError(c, 403, message)
			return
		}
		// Recheck exclusive-group access so revoked membership cannot authorize this request.
		if !admission.GroupAllowed(apiKey) {
			options.business(c)
			options.reject(c, "group_not_allowed")
			keyhttp.AbortGoogleError(c, 403, "You do not have access to the selected group.")
			return
		}
		ApplyAPIKeyModelRedirect(c, apiKey)
		skipBilling := IsAPIKeyUsageRequest(c.Request.Method, c.Request.URL.Path) ||
			IsBatchImageBillingBypassRequest(c.Request.Method, c.Request.URL.Path) ||
			(apiKey.IsComposite && IsGrokVideoTaskRead(c.Request.Method, c.Request.URL.Path))

		// Non-consuming requests can read or manage existing data even when their configured funding source is invalid.
		billingContext, billingErr := admission.ResolveFundingFromKey(c.Request.Context(), apiKey, subscriptionService, !skipBilling)
		if billingErr != nil {
			switch {
			case errors.Is(billingErr, billing.ErrPreferredSubscriptionGroup):
				keyhttp.AbortGoogleError(c, 403, "Preferred subscription does not allow this group")
			case errors.Is(billingErr, billing.ErrPreferredSubscriptionInvalid):
				keyhttp.AbortGoogleError(c, 403, "Preferred subscription is unavailable")
			default:
				keyhttp.AbortGoogleError(c, 500, "Failed to validate subscription")
			}
			return
		}
		var subscription *billing.UserSubscription
		if billingContext != nil {
			subscription = billingContext.Subscription
		}
		if billingContext != nil {
			c.Set("api_key_billing", billingContext)
		}
		if !skipBilling {
			checked, failure := admission.CheckConsumption(c.Request.Context(), admission.ConsumptionInput{Status: apiKey.Status, Limits: apiKey, Balance: apiKey.User.Balance, Subscription: subscription}, subscriptionService)
			if failure != nil {
				status := 403
				message := ""
				switch failure.Kind {
				case admission.KeyQuotaExceeded:
					status = 429
					message = "The API key quota has been exhausted."
				case admission.KeyExpired:
					message = "The API key has expired."
				case admission.MaintenanceFailed:
					status = 500
					message = "Failed to maintain subscription usage windows"
				case admission.SubscriptionLimitExceeded:
					status = 429
					message = failure.Error()
				case admission.SubscriptionInvalid:
					message = failure.Error()
				case admission.InsufficientBalance:
					message = "Insufficient account balance"
				}
				keyhttp.AbortGoogleError(c, status, message)
				return
			}
			subscription = checked
			if subscription != nil {
				billingContext.Subscription = subscription
			}
		}
		if subscription != nil {
			c.Set("subscription", subscription)
		}

		c.Set("api_key", apiKey)
		c.Set("user", authctx.AuthSubject{
			UserID:      apiKey.User.ID,
			Concurrency: apiKey.User.Concurrency,
		})
		c.Set("user_role", apiKey.User.Role)
		options.bind(c, apiKey)
		_ = apiKeyService.TouchLastUsed(c.Request.Context(), apiKey.ID)
		keyhttp.SetAccessPrincipal(c, access)
		release, ok := acquireKeyRequest(c, apiKeyService, apiKey, options)
		if !ok {
			return
		}
		defer release()
		c.Next()
	}
}

func AbortAPIKeyQuotaError(c *gin.Context) {
	const message = "The API key quota has been exhausted."
	if IsOpenAICompatibleAPIKeyRequest(c) {
		AbortOpenAIQuotaError(c, http.StatusTooManyRequests, message)
		return
	}
	keyhttp.AbortWithError(c, http.StatusTooManyRequests, "API_KEY_QUOTA_EXHAUSTED", message)
}

func IsOpenAICompatibleAPIKeyRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}

	path := strings.TrimRight(c.Request.URL.Path, "/")
	for _, root := range []string{
		"/v1/responses",
		"/openai/v1/responses",
		"/responses",
		"/backend-api/codex/responses",
	} {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

// AbortOpenAIQuotaError writes an OpenAI-compatible insufficient-quota response.
func AbortOpenAIQuotaError(c *gin.Context, statusCode int, message string) {
	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"message": message,
			"type":    "insufficient_quota",
			"param":   nil,
			"code":    "insufficient_quota",
		},
	})
	c.Abort()
}

func abortAuthorizationGroupUnavailable(c *gin.Context, key *apikey.APIKey, o APIKeyAuthorizationOptions) bool {
	// Reject an unbound ordinary key before funding checks, while allowing existing-task and local usage access.
	if key != nil && !key.IsComposite && key.GroupID == nil && !IsAPIKeyUsageRequest(c.Request.Method, c.Request.URL.Path) && !IsBatchImageBillingBypassRequest(c.Request.Method, c.Request.URL.Path) && !IsGrokVideoTaskRead(c.Request.Method, c.Request.URL.Path) {
		o.business(c)
		o.reject(c, "group_unassigned")
		keyhttp.AbortWithError(c, http.StatusForbidden, "GROUP_REQUIRED", "Select a group for this API key in the console")
		return true
	}

	code, message, ok := admission.GroupAvailable(key)
	if ok {
		return false
	}
	o.business(c)
	if code == "GROUP_DELETED" {
		o.reject(c, "group_deleted")
	} else {
		o.reject(c, "group_disabled")
	}
	keyhttp.AbortWithError(c, 403, code, message)
	return true
}

func abortAuthorizationGroupNotAllowed(c *gin.Context, key *apikey.APIKey, o APIKeyAuthorizationOptions) bool {
	if admission.GroupAllowed(key) {
		return false
	}
	o.business(c)
	o.reject(c, "group_not_allowed")
	keyhttp.AbortWithError(c, 403, "GROUP_NOT_ALLOWED", "You do not have access to the selected group.")
	return true
}

// acquireKeyRequest reserves request capacity before protocol handling; non-consuming and per-turn endpoints bypass it.
func acquireKeyRequest(c *gin.Context, keys *apikey.APIKeyService, key *apikey.APIKey, options APIKeyAuthorizationOptions) (func(), bool) {
	if IsAPIKeyNonConsumingRequest(c.Request.Method, c.Request.URL.Path) || isResponsesTurnAdmission(c) {
		return func() {}, true
	}
	ctx, release, retry, err := keys.AcquireRequest(c.Request.Context(), key)
	if err == nil {
		c.Request = c.Request.WithContext(ctx)
		return release, true
	}
	status := http.StatusTooManyRequests
	code := "API_KEY_RPM_LIMIT_EXCEEDED"
	switch {
	case errors.Is(err, apikey.ErrKeyConcurrencyExceeded):
		code = "API_KEY_CONCURRENCY_LIMIT_EXCEEDED"
	case errors.Is(err, apikey.ErrKeyRPMExceeded):
	default:
		status = http.StatusServiceUnavailable
		code = "API_KEY_LIMITER_UNAVAILABLE"
	}
	if retry > 0 {
		c.Header("Retry-After", strconv.Itoa(int(math.Ceil(retry.Seconds()))))
	}
	if status == http.StatusTooManyRequests && options.Authentication.BusinessLimited != nil {
		options.Authentication.BusinessLimited(c, code)
	}
	if options.Authentication.Google {
		keyhttp.AbortGoogleError(c, status, err.Error())
	} else {
		keyhttp.AbortWithError(c, status, code, err.Error())
	}
	return nil, false
}

// isResponsesTurnAdmission identifies Responses upgrades whose key limits are enforced on each turn.
func isResponsesTurnAdmission(c *gin.Context) bool {
	if c.Request.Method != http.MethodGet || !IsResponsesWSUpgrade(c.Request) {
		return false
	}
	switch strings.TrimRight(c.Request.URL.Path, "/") {
	case "/responses", "/v1/responses", "/backend-api/codex/responses":
		return true
	default:
		return false
	}
}

// GetExecutionAPIKey reads the authenticated API key from the request context.
func GetExecutionAPIKey(c interface{ Get(string) (any, bool) }) *apikey.APIKey {
	if c == nil {
		return nil
	}
	v, exists := c.Get("api_key")
	if !exists {
		return nil
	}
	apiKey, _ := v.(*apikey.APIKey)
	return apiKey
}
