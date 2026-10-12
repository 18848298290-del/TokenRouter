package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/audit"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/creative"
	creativehttp "github.com/TokenFlux/TokenRouter/internal/creative/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
	"github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/notification"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	paymentcore "github.com/TokenFlux/TokenRouter/internal/payment"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
	"github.com/TokenFlux/TokenRouter/internal/server/runtimeconfig"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/settings/composite"
	settingshttp "github.com/TokenFlux/TokenRouter/internal/settings/httpapi"
	settingsdto "github.com/TokenFlux/TokenRouter/internal/settings/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/site"
)

// settingAtomicRepo injects write and post-commit read failures to check the persistence status returned by HTTP.
type settingAtomicRepo struct {
	*settingHandlerRepoStub
	writes    int
	failWrite bool
	failApply bool
}

type failingAuthSourceSettingsRepoStub struct {
	values map[string]string
	err    error
}

type creativeModelCandidateReaderStub struct {
	candidates     []creative.CreativeModelCandidate
	sanitizeGemini bool
}

type settingsResponseSource struct {
	*composite.Runtime
	snapshot     *composite.Snapshot
	fastPolicy   *tierpolicy.OpenAIFastPolicySettings
	responseRead int
	reads        int
	calls        *[]string
}

type settingsResponsePayment struct {
	config *paymentcore.PaymentConfig
	err    error
	calls  *[]string
}

type settingsResponseMonitoring struct {
	calls *[]string
}

func TestSettingsResponsesWSPatchAndNull(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{ws.SettingKey: `{"read_timeout_seconds":45,"max_ingress_connections_per_api_key":5}`})
	response := doUpdateSettings(t, h, map[string]any{"responses_ws": map[string]any{"read_timeout_seconds": nil, "max_ingress_connections_per_api_key": 0}}, nil)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.JSONEq(t, `{"max_ingress_connections_per_api_key":0}`, repo.values[ws.SettingKey])
	require.Contains(t, response.Body.String(), `"responses_ws_effective"`)
	response = doUpdateSettings(t, h, map[string]any{"responses_ws": nil}, nil)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.JSONEq(t, `{}`, repo.values[ws.SettingKey])
}

func TestSettingsResponsesWSInvalidPatchDoesNotWrite(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{ws.SettingKey: `{"read_timeout_seconds":45}`})
	response := doUpdateSettings(t, h, map[string]any{"responses_ws": map[string]any{"max_conns_per_provider": 1}}, nil)
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	require.JSONEq(t, `{"read_timeout_seconds":45}`, repo.values[ws.SettingKey])
}

func TestSettingsResponseFieldsAndPaymentErrors(t *testing.T) {
	for _, paymentCase := range []string{"configured", "absent service", "nil config", "read error", "config with error"} {
		t.Run(paymentCase, func(t *testing.T) {
			for _, method := range []string{http.MethodGet, http.MethodPut} {
				t.Run(method, func(t *testing.T) {
					repo := &settingHandlerRepoStub{values: map[string]string{}}
					options, _ := newCompositeSettingsHTTPFixture(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
					base, ok := options.Settings.(*composite.Runtime)
					require.True(t, ok)
					authDefaults, err := base.GetAuthSourceDefaultSettings(context.Background())
					require.NoError(t, err)
					snapshot := &composite.Snapshot{}
					fillSettingsResponseScalars(snapshot)
					snapshot.CustomMenuItems = `[{"id":"menu-1","label":"Help","icon_svg":"","url":"/help","visibility":"user","sort_order":2}]`
					snapshot.CustomEndpoints = `[{"id":"endpoint-1","name":"API","endpoint":"https://api.example.com","description":"Example"}]`
					snapshot.FooterLinks = `[{"id":"group-1","title":"Links","links":[{"id":"link-1","label":"Help","url":"/help"}]}]`
					snapshot.HomeFeaturedModels = `["example-model"]`
					snapshot.ResponsesWS = json.RawMessage(`{"read_timeout_seconds":45}`)
					snapshot.ProviderSchedulingThresholds = map[string]int{"openai": 75}
					snapshot.ProviderQuotaNotifyEmails = []identity.NotifyEmailEntry{{Email: "quota@example.com", Verified: true}}
					if paymentCase == "configured" {
						snapshot.DefaultSubscriptions = []billing.DefaultSubscriptionSetting{{PlanID: 17}}
					}
					var calls []string
					source := &settingsResponseSource{Runtime: base, snapshot: snapshot, responseRead: 1, calls: &calls}
					if paymentCase == "configured" {
						source.fastPolicy = &tierpolicy.OpenAIFastPolicySettings{Rules: []tierpolicy.OpenAIFastPolicyRule{{ServiceTier: "priority", Action: "filter", Scope: "all", UserIDs: []int64{42}}}}
					}
					if method == http.MethodPut {
						source.responseRead = 2
					}
					options.Settings = source
					options.Monitoring = &settingsResponseMonitoring{calls: &calls}
					payment := &settingsResponsePayment{calls: &calls}
					if paymentCase == "configured" || paymentCase == "config with error" {
						payment.config = &paymentcore.PaymentConfig{}
						fillSettingsResponseScalars(payment.config)
						payment.config.EnabledTypes = []string{"alipay", "wxpay"}
						payment.config.MethodFees = paymentcore.MethodFeeSettings{"alipay": {Enabled: true, FixedFee: 0.25, FeeRate: 1.5}}
					}
					if strings.Contains(paymentCase, "error") {
						payment.err = errors.New("payment read failed")
					}
					if paymentCase != "absent service" {
						options.Payment = payment
					}
					h := settingshttp.NewHandler(options)
					var rec *httptest.ResponseRecorder
					if method == http.MethodPut {
						rec = doUpdateSettings(t, h, map[string]any{}, nil)
					} else {
						rec = httptest.NewRecorder()
						ctx, _ := gin.CreateTestContext(rec)
						ctx.Request = httptest.NewRequest(http.MethodGet, "/settings", nil)
						h.GetSettings(ctx)
					}
					wantCalls := []string{"settings", "auth", "monitoring"}
					if method == http.MethodPut {
						wantCalls = []string{"settings", "auth", "settings", "auth"}
					}
					if paymentCase != "absent service" {
						wantCalls = append(wantCalls, "payment")
					}
					if method == http.MethodPut && payment.err != nil {
						require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
						require.Equal(t, wantCalls, calls)
						return
					}
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					require.Equal(t, append(wantCalls, "totp", "fast"), calls)
					var response struct {
						Data map[string]json.RawMessage `json:"data"`
					}
					require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
					for _, field := range []string{"smtp_password", "turnstile_secret_key", "oidc_connect_client_secret"} {
						require.NotContains(t, response.Data, field)
					}
					assertSettingsResponseFields(t, response.Data, source, payment.config, authDefaults, method)
				})
			}
		})
	}
}

func fillSettingsResponseScalars(target any) {
	value := reflect.ValueOf(target).Elem()
	for i := range value.NumField() {
		field := value.Field(i)
		switch field.Kind() {
		case reflect.Bool:
			field.SetBool(true)
		case reflect.String:
			field.SetString("fixture." + value.Type().Field(i).Name)
		case reflect.Int:
			field.SetInt(int64(i + 10))
		case reflect.Float64:
			field.SetFloat(float64(i) + 0.25)
		}
	}
}

func assertSettingsResponseFields(t *testing.T, data map[string]json.RawMessage, source *settingsResponseSource, payment *paymentcore.PaymentConfig, authDefaults *identity.AuthSourceDefaultSettings, method string) {
	t.Helper()
	snapshot := source.snapshot
	if payment == nil {
		payment = &paymentcore.PaymentConfig{}
	}
	settingsValue := reflect.ValueOf(snapshot).Elem()
	paymentValue := reflect.ValueOf(payment).Elem()
	responseType := reflect.TypeFor[settingsdto.SystemSettings]()
	for field := range responseType.Fields() {
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if (field.Name == "OpenAIFastPolicySettings" && source.fastPolicy == nil) || (field.Name == "ProviderSchedulingThresholds" && method == http.MethodPut) {
			require.NotContains(t, data, name)
			continue
		}
		var expected any
		switch field.Name {
		case "OpenAIFastPolicySettings":
			expected = source.fastPolicy
		case "TotpEncryptionKeyConfigured":
			expected = true
		case "OpsMonitoringEnabled":
			expected = method == http.MethodPut
		case "DefaultSubscriptions":
			expected = snapshot.DefaultSubscriptions
			if snapshot.DefaultSubscriptions == nil {
				expected = []any{}
			}
		case "LoginAgreementDocuments":
			expected = []any{}
		case "CustomMenuItems", "CustomEndpoints", "FooterLinks", "HomeFeaturedModels":
			expected = json.RawMessage(settingsValue.FieldByName(field.Name).String())
		case "PaymentMethodFees":
			expected = payment.MethodFees
			if payment.MethodFees == nil {
				expected = map[string]any{}
			}
		default:
			value := settingsValue.FieldByName(field.Name)
			if !value.IsValid() {
				paymentName := strings.TrimPrefix(field.Name, "Payment")
				if field.Name == "PaymentLoadBalanceStrat" {
					paymentName = "LoadBalanceStrategy"
				}
				value = paymentValue.FieldByName(paymentName)
			}
			require.True(t, value.IsValid(), field.Name)
			expected = value.Interface()
		}
		encoded, err := json.Marshal(expected)
		require.NoError(t, err)
		require.Contains(t, data, name)
		require.JSONEq(t, string(encoded), string(data[name]), name)
		delete(data, name)
	}
	for _, provider := range []struct {
		name  string
		value identity.ProviderDefaultGrantSettings
	}{
		{"email", authDefaults.Email},
		{"linuxdo", authDefaults.LinuxDo},
		{"oidc", authDefaults.OIDC},
		{"wechat", authDefaults.WeChat},
		{"github", authDefaults.GitHub},
		{"google", authDefaults.Google},
		{"dingtalk", authDefaults.DingTalk},
	} {
		for key, value := range map[string]any{
			"balance": provider.value.Balance, "concurrency": provider.value.Concurrency, "subscriptions": provider.value.Subscriptions,
			"grant_on_signup": provider.value.GrantOnSignup, "grant_on_first_bind": provider.value.GrantOnFirstBind,
		} {
			name := "auth_source_default_" + provider.name + "_" + key
			encoded, err := json.Marshal(value)
			require.NoError(t, err)
			require.JSONEq(t, string(encoded), string(data[name]), name)
			delete(data, name)
		}
	}
	require.JSONEq(t, "false", string(data["force_email_on_third_party_signup"]))
	delete(data, "force_email_on_third_party_signup")
	require.Empty(t, data)
}

func (s *settingsResponseSource) GetAllSettings(ctx context.Context) (*composite.Snapshot, error) {
	*s.calls = append(*s.calls, "settings")
	s.reads++
	if s.reads >= s.responseRead {
		return s.snapshot, nil
	}
	return s.Runtime.GetAllSettings(ctx)
}

func (s *settingsResponseSource) GetAuthSourceDefaultSettings(ctx context.Context) (*identity.AuthSourceDefaultSettings, error) {
	*s.calls = append(*s.calls, "auth")
	return s.Runtime.GetAuthSourceDefaultSettings(ctx)
}

func (s *settingsResponseSource) IsTotpEncryptionKeyConfigured() bool {
	*s.calls = append(*s.calls, "totp")
	return true
}

func (s *settingsResponseSource) GetOpenAIFastPolicySettings(context.Context) (*tierpolicy.OpenAIFastPolicySettings, error) {
	*s.calls = append(*s.calls, "fast")
	return s.fastPolicy, nil
}

func (s *settingsResponsePayment) GetPaymentConfig(context.Context) (*paymentcore.PaymentConfig, error) {
	*s.calls = append(*s.calls, "payment")
	return s.config, s.err
}

func (s *settingsResponseMonitoring) IsMonitoringEnabled(context.Context) bool {
	*s.calls = append(*s.calls, "monitoring")
	return false
}

func (s *settingsResponseMonitoring) SetMonitoringEnabled(bool) {}

// newCompositeSettingsHTTPFixture binds settings reads, preparation, and persistence with a no-op plan validator.
// The fixture publishes trusted-proxy settings and update notifications.
func newCompositeSettingsHTTPFixture(repo settingscore.Repository, cfg *config.Config) (settingshttp.HandlerOptions, *settingscore.Store) {
	store := settingscore.New(repo)
	oauth := provideOAuthSettings(store, cfg)
	grants := identity.NewGrantSettings(store, identity.GrantSettingsOptions{DefaultBalance: cfg.Default.UserBalance, DefaultConcurrency: cfg.Default.UserConcurrency})
	gatewayRuntime := provideGatewaySettings(store)
	rules := provideGatewayAdminRules()
	defaults := provideSchedulerAdminDefaults(cfg)
	forwarded := provideForwardedSettings(store, cfg)
	read := composite.ReadOptions{
		OAuth: oauth, Gateway: *rules, Scheduler: *defaults,
		DefaultBalance:     func() float64 { return cfg.Default.UserBalance },
		DefaultConcurrency: func() int { return cfg.Default.UserConcurrency },
		Forwarded: func() runtimeconfig.ForwardedInput {
			value := cfg.ForwardedClientIPSettings()
			return runtimeconfig.ForwardedInput{APIKeyACLTrustForwardedIP: value.TrustForwardedIP, ForwardedClientIPHeaders: value.Headers}
		},
	}
	prepare := composite.PrepareOptions{
		ReadValues: store.GetAll, Gateway: *rules, Scheduler: *defaults,
		ValidatePlans: func(context.Context, []billing.DefaultSubscriptionSetting) error { return nil },
	}
	applications := []composite.Application{
		{Module: "server", Apply: func(_ context.Context, value *composite.Snapshot) error {
			forwarded.Apply(runtimeconfig.ForwardedInput{APIKeyACLTrustForwardedIP: value.APIKeyACLTrustForwardedIP, ForwardedClientIPHeaders: value.ForwardedClientIPHeaders})
			return nil
		}},
		{Module: "site", Apply: func(context.Context, *composite.Snapshot) error { store.NotifyUpdated(); return nil }},
	}
	source := composite.NewRuntime(store, read, prepare, grants, gatewayRuntime, cfg.Totp.EncryptionKeyConfigured, applications)
	participants := staticSettingsParticipants(&paymentcore.Runtime{}, grants, defaults, rules)
	// Use the payment preparer with channel refresh disabled for this fixture.
	participants[len(participants)-1] = paymentcore.SettingsParticipant(nil)
	registry, err := settingscore.NewRegistry(participants...)
	return settingshttp.HandlerOptions{Settings: source, Participants: registry, ParticipantError: err}, store
}

// TestSettingsFieldOwnership checks complete, unique registration of flattened request fields.
func TestSettingsFieldOwnership(t *testing.T) {
	defaults := scheduler.DefaultAdminSettingsDefaults()
	participants := staticSettingsParticipants(&paymentcore.Runtime{}, nil, &defaults, provideGatewayAdminRules())
	_, err := settingscore.NewRegistry(participants...)
	require.NoError(t, err)
	owners := map[string]string{}
	for _, participant := range participants {
		encoded, err := json.Marshal(struct {
			Module       string
			Fields, Keys []string
		}{participant.Module, participant.Fields, participant.Keys})
		require.NoError(t, err)
		t.Logf("TEST_PARTICIPANT %s", encoded)
		for _, field := range participant.Fields {
			require.Empty(t, owners[field], "字段 %s 有多个所有者", field)
			owners[field] = participant.Module
		}
	}
	input := reflect.TypeFor[settingsdto.UpdateSettingsRequest]()
	count := 0
	for field := range input.Fields() {
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		require.Contains(t, owners, name, "输入字段 %s 没有静态所有者", field.Name)
		delete(owners, name)
		count++
	}
	require.Equal(t, 283, count, "HTTP 字段变化需要同步字段登记")
	require.Empty(t, owners, "参与者不应声明不存在的输入字段")
}

// TestSettingsRejectedFastPolicyHasNoWrites checks that later validation failure prevents earlier fields from being saved.
func TestSettingsRejectedFastPolicyHasNoWrites(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{site.SettingKeySiteName: "before"})
	rec := doUpdateSettings(t, h, map[string]any{"site_name": "after", "openai_fast_policy_settings": map[string]any{"rules": []map[string]any{{"service_tier": "priority", "action": "bogus", "scope": "all"}}}}, nil)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Equal(t, "before", repo.values[site.SettingKeySiteName], "后段校验拒绝后不应保存站点名称")
}

func (r *settingAtomicRepo) SetMultiple(ctx context.Context, values map[string]string) error {
	r.writes++
	if r.failWrite {
		return errors.New("settings write failed")
	}
	return r.settingHandlerRepoStub.SetMultiple(ctx, values)
}

func (r *settingAtomicRepo) GetAll(ctx context.Context) (map[string]string, error) {
	if r.failApply && r.writes > 0 {
		return nil, errors.New("settings reload failed")
	}
	return r.settingHandlerRepoStub.GetAll(ctx)
}

func TestSettingsCombinedUpdateCommitsOnce(t *testing.T) {
	repo := &settingAtomicRepo{settingHandlerRepoStub: &settingHandlerRepoStub{values: map[string]string{site.SettingKeySiteName: "before"}}}
	options, _ := newCompositeSettingsHTTPFixture(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
	payment := paymentcore.NewConfigService(nil, repo, nil, nil, paymentcore.ConfigurationRuntime{})
	options.Payment = payment
	h := settingshttp.NewHandler(options)
	rec := doUpdateSettings(t, h, map[string]any{"site_name": "after", "payment_enabled": true, "openai_fast_policy_settings": map[string]any{"rules": []any{}}}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, repo.writes)
	require.Equal(t, "after", repo.values[site.SettingKeySiteName])
	require.Equal(t, "true", repo.values[paymentcore.SettingPaymentEnabled])
	require.Equal(t, `{"rules":[]}`, repo.values[gateway.SettingKeyOpenAIFastPolicySettings])
}

func TestSettingsCombinedWriteAndApplyErrors(t *testing.T) {
	for _, scenario := range []string{"write", "apply"} {
		t.Run(scenario, func(t *testing.T) {
			repo := &settingAtomicRepo{settingHandlerRepoStub: &settingHandlerRepoStub{values: map[string]string{site.SettingKeySiteName: "before"}}, failWrite: scenario == "write", failApply: scenario == "apply"}
			options, store := newCompositeSettingsHTTPFixture(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
			h := settingshttp.NewHandler(options)
			notified := false
			store.Subscribe(func() { notified = true })
			rec := doUpdateSettings(t, h, map[string]any{"site_name": "after"}, nil)
			require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
			require.False(t, notified)
			if scenario == "write" {
				require.Equal(t, "before", repo.values[site.SettingKeySiteName])
				return
			}
			require.Equal(t, "after", repo.values[site.SettingKeySiteName])
			require.Contains(t, rec.Body.String(), "SETTINGS_APPLY_FAILED")
			require.Contains(t, rec.Body.String(), "persisted")
		})
	}
}

func (s *failingAuthSourceSettingsRepoStub) Get(ctx context.Context, key string) (*settingscore.Setting, error) {
	panic("unexpected Get call")
}

func (s *failingAuthSourceSettingsRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	panic("unexpected GetValue call")
}

func (s *failingAuthSourceSettingsRepoStub) Set(ctx context.Context, key, value string) error {
	panic("unexpected Set call")
}

func (s *failingAuthSourceSettingsRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

func (s *failingAuthSourceSettingsRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	if _, ok := settings[identity.SettingKeyAuthSourceDefaultEmailBalance]; ok {
		return s.err
	}
	for key, value := range settings {
		if s.values == nil {
			s.values = map[string]string{}
		}
		s.values[key] = value
	}
	return nil
}

func (s *failingAuthSourceSettingsRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	out := make(map[string]string, len(s.values))
	maps.Copy(out, s.values)
	return out, nil
}

func (s *failingAuthSourceSettingsRepoStub) Delete(ctx context.Context, key string) error {
	panic("unexpected Delete call")
}

func TestSettingHandler_GetSettings_InjectsAuthSourceDefaults(t *testing.T) {
	repo := &settingHandlerRepoStub{
		values: map[string]string{
			identity.SettingKeyRegistrationEnabled:                 "true",
			promotion.SettingKeyPromoCodeEnabled:                   "true",
			identity.SettingKeyAuthSourceDefaultEmailBalance:       "9.5",
			identity.SettingKeyAuthSourceDefaultEmailConcurrency:   "8",
			identity.SettingKeyAuthSourceDefaultEmailSubscriptions: `[{"plan_id":31}]`,
			identity.SettingKeyForceEmailOnThirdPartySignup:        "true",
		},
	}
	options, _ := newCompositeSettingsHTTPFixture(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
	handler := settingshttp.NewHandler(options)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)

	handler.GetSettings(c)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp response.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	data, ok := resp.Data.(map[string]any)
	require.True(t, ok)
	require.Equal(t, 9.5, data["auth_source_default_email_balance"])
	require.Equal(t, float64(8), data["auth_source_default_email_concurrency"])
	require.Equal(t, true, data["force_email_on_third_party_signup"])

	subscriptions, ok := data["auth_source_default_email_subscriptions"].([]any)
	require.True(t, ok)
	require.Len(t, subscriptions, 1)
}

func TestSettingHandler_UpdateSettings_PreservesOmittedAuthSourceDefaults(t *testing.T) {
	repo := &settingHandlerRepoStub{
		values: map[string]string{
			identity.SettingKeyRegistrationEnabled:                    "false",
			promotion.SettingKeyPromoCodeEnabled:                      "true",
			identity.SettingKeyAuthSourceDefaultEmailBalance:          "9.5",
			identity.SettingKeyAuthSourceDefaultEmailConcurrency:      "8",
			identity.SettingKeyAuthSourceDefaultEmailSubscriptions:    `[{"plan_id":31}]`,
			identity.SettingKeyAuthSourceDefaultEmailGrantOnSignup:    "true",
			identity.SettingKeyAuthSourceDefaultEmailGrantOnFirstBind: "false",
			identity.SettingKeyForceEmailOnThirdPartySignup:           "true",
		},
	}
	options, _ := newCompositeSettingsHTTPFixture(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
	handler := settingshttp.NewHandler(options)

	body := map[string]any{
		"registration_enabled":              true,
		"promo_code_enabled":                true,
		"auth_source_default_email_balance": 12.75,
	}
	rawBody, err := json.Marshal(body)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(rawBody))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.UpdateSettings(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "12.75000000", repo.values[identity.SettingKeyAuthSourceDefaultEmailBalance])
	require.Equal(t, "8", repo.values[identity.SettingKeyAuthSourceDefaultEmailConcurrency])
	require.Equal(t, `[{"plan_id":31}]`, repo.values[identity.SettingKeyAuthSourceDefaultEmailSubscriptions])
	require.Equal(t, "true", repo.values[identity.SettingKeyForceEmailOnThirdPartySignup])

	var resp response.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	data, ok := resp.Data.(map[string]any)
	require.True(t, ok)
	require.Equal(t, 12.75, data["auth_source_default_email_balance"])
	require.Equal(t, float64(8), data["auth_source_default_email_concurrency"])
	require.Equal(t, true, data["force_email_on_third_party_signup"])
}

// TestSettingHandler_UpdateSettings_AcceptsMarkdownCustomMenuURL checks URL normalization and browser-generated content IDs.
func TestSettingHandler_UpdateSettings_AcceptsMarkdownCustomMenuURL(t *testing.T) {
	for _, test := range []struct {
		id     string
		status int
	}{
		{"guide", http.StatusOK},
		{"00112233445566778899aabbccddeeff", http.StatusOK},
		{"00112233-4455-6677-8899-aabbccddeeff", http.StatusBadRequest},
	} {
		t.Run(test.id, func(t *testing.T) {
			repo := &settingHandlerRepoStub{values: map[string]string{promotion.SettingKeyPromoCodeEnabled: "true"}}
			options, _ := newCompositeSettingsHTTPFixture(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
			handler := settingshttp.NewHandler(options)
			body := map[string]any{
				"promo_code_enabled": true,
				"custom_menu_items": []map[string]any{{
					"id": test.id, "label": "Guide", "icon_svg": "", "url": " md:guide ", "visibility": "user", "sort_order": 0,
				}},
			}
			rawBody, err := json.Marshal(body)
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(rawBody))
			c.Request.Header.Set("Content-Type", "application/json")
			handler.UpdateSettings(c)
			require.Equal(t, test.status, rec.Code, rec.Body.String())
			if test.status != http.StatusOK {
				require.NotContains(t, repo.values, site.SettingKeyCustomMenuItems)
				return
			}
			var saved []site.CustomMenuItem
			require.NoError(t, json.Unmarshal([]byte(repo.values[site.SettingKeyCustomMenuItems]), &saved))
			require.Equal(t, []site.CustomMenuItem{{ID: test.id, Label: "Guide", URL: "md:guide", Visibility: "user"}}, saved)
		})
	}
}

func TestSettingHandler_UpdateSettings_RejectsEmptyMarkdownCustomMenuSlug(t *testing.T) {
	repo := &settingHandlerRepoStub{
		values: map[string]string{
			promotion.SettingKeyPromoCodeEnabled: "true",
		},
	}
	options, _ := newCompositeSettingsHTTPFixture(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
	handler := settingshttp.NewHandler(options)

	body := map[string]any{
		"promo_code_enabled": true,
		"custom_menu_items": []map[string]any{
			{
				"id":         "guide",
				"label":      "Guide",
				"icon_svg":   "",
				"url":        "md:",
				"visibility": "user",
				"sort_order": 0,
			},
		},
	}
	rawBody, err := json.Marshal(body)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(rawBody))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.UpdateSettings(c)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.NotContains(t, repo.values, site.SettingKeyCustomMenuItems)
}

func TestSettingHandler_UpdateSettings_PersistsPaymentVisibleMethodsAndAdvancedScheduler(t *testing.T) {
	repo := &settingHandlerRepoStub{
		values: map[string]string{
			promotion.SettingKeyPromoCodeEnabled: "true",
			ops.SettingKeyOpsAdvancedSettings:    `{"data_retention":{"cleanup_enabled":true,"cleanup_schedule":"0 4 * * *","error_log_retention_days":12,"minute_metrics_retention_days":8,"hourly_metrics_retention_days":30},"aggregation":{"aggregation_enabled":true},"openai_provider_quota_auto_pause":{"default_threshold_5h":0.6,"default_threshold_7d":0.7},"ignore_count_tokens_errors":true,"ignore_context_canceled":true,"ignore_no_available_providers":false,"ignore_invalid_api_key_errors":false,"ignore_insufficient_balance_errors":true,"display_openai_token_stats":false,"display_alert_events":true,"auto_refresh_enabled":false,"auto_refresh_interval_seconds":30}`,
		},
	}
	options, _ := newCompositeSettingsHTTPFixture(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
	paymentConfigService := paymentcore.NewConfigService(nil, repo, nil, nil, paymentcore.ConfigurationRuntime{})
	options.Payment = paymentConfigService
	handler := settingshttp.NewHandler(options)

	body := map[string]any{
		"promo_code_enabled":                               true,
		"payment_visible_method_alipay_source":             "easypay",
		"payment_visible_method_wxpay_source":              "wxpay",
		"payment_visible_method_alipay_enabled":            true,
		"payment_visible_method_wxpay_enabled":             false,
		"advanced_scheduler_subscription_priority_enabled": true,
		"openai_provider_quota_auto_pause": map[string]any{
			"default_threshold_5h": 0.95,
			"default_threshold_7d": 0.9,
		},
	}
	rawBody, err := json.Marshal(body)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(rawBody))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.UpdateSettings(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, paymentcore.VisibleMethodSourceEasyPayAlipay, repo.values[paymentcore.SettingPaymentVisibleMethodAlipaySource])
	require.Equal(t, paymentcore.VisibleMethodSourceOfficialWechat, repo.values[paymentcore.SettingPaymentVisibleMethodWxpaySource])
	require.Equal(t, "true", repo.values[paymentcore.SettingPaymentVisibleMethodAlipayEnabled])
	require.Equal(t, "false", repo.values[paymentcore.SettingPaymentVisibleMethodWxpayEnabled])
	require.Equal(t, "true", repo.values[scheduler.SettingKeyAdvancedSchedulerSubscriptionPriorityEnabled])
	var advanced ops.OpsAdvancedSettings
	require.NoError(t, json.Unmarshal([]byte(repo.values[ops.SettingKeyOpsAdvancedSettings]), &advanced))
	require.True(t, advanced.DataRetention.CleanupEnabled)
	require.Equal(t, "0 4 * * *", advanced.DataRetention.CleanupSchedule)
	require.NotContains(t, repo.values[ops.SettingKeyOpsAdvancedSettings], `"aggregation"`)
	require.Equal(t, 0.95, advanced.OpenAIProviderQuotaAutoPause.DefaultThreshold5h)
	require.Equal(t, 0.9, advanced.OpenAIProviderQuotaAutoPause.DefaultThreshold7d)

	var resp response.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	data, ok := resp.Data.(map[string]any)
	require.True(t, ok)
	require.Equal(t, paymentcore.VisibleMethodSourceEasyPayAlipay, data["payment_visible_method_alipay_source"])
	require.Equal(t, paymentcore.VisibleMethodSourceOfficialWechat, data["payment_visible_method_wxpay_source"])
	require.Equal(t, true, data["payment_visible_method_alipay_enabled"])
	require.Equal(t, false, data["payment_visible_method_wxpay_enabled"])
	require.Equal(t, true, data["advanced_scheduler_subscription_priority_enabled"])
	quota, ok := data["openai_provider_quota_auto_pause"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, 0.95, quota["default_threshold_5h"])
	require.Equal(t, 0.9, quota["default_threshold_7d"])
}

func TestSettingHandler_UpdateSettings_PreservesLegacyBlankPaymentVisibleMethodSource(t *testing.T) {
	repo := &settingHandlerRepoStub{
		values: map[string]string{
			promotion.SettingKeyPromoCodeEnabled:                 "true",
			paymentcore.SettingPaymentVisibleMethodAlipayEnabled: "true",
			paymentcore.SettingPaymentVisibleMethodAlipaySource:  "",
			paymentcore.SettingPaymentVisibleMethodWxpayEnabled:  "false",
			paymentcore.SettingPaymentVisibleMethodWxpaySource:   "",
		},
	}
	options, _ := newCompositeSettingsHTTPFixture(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
	handler := settingshttp.NewHandler(options)

	body := map[string]any{
		"promo_code_enabled": false,
	}
	rawBody, err := json.Marshal(body)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(rawBody))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.UpdateSettings(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "", repo.values[paymentcore.SettingPaymentVisibleMethodAlipaySource])
	require.Equal(t, "true", repo.values[paymentcore.SettingPaymentVisibleMethodAlipayEnabled])
}

func TestSettingHandler_UpdateSettings_PersistsExplicitFalseOIDCCompatibilityFlags(t *testing.T) {
	repo := &settingHandlerRepoStub{
		values: map[string]string{
			promotion.SettingKeyPromoCodeEnabled:              "true",
			identity.SettingKeyOIDCConnectEnabled:             "true",
			identity.SettingKeyOIDCConnectProviderName:        "OIDC",
			identity.SettingKeyOIDCConnectClientID:            "oidc-client",
			identity.SettingKeyOIDCConnectClientSecret:        "oidc-secret",
			identity.SettingKeyOIDCConnectIssuerURL:           "https://issuer.example.com",
			identity.SettingKeyOIDCConnectAuthorizeURL:        "https://issuer.example.com/auth",
			identity.SettingKeyOIDCConnectTokenURL:            "https://issuer.example.com/token",
			identity.SettingKeyOIDCConnectUserInfoURL:         "https://issuer.example.com/userinfo",
			identity.SettingKeyOIDCConnectJWKSURL:             "https://issuer.example.com/jwks",
			identity.SettingKeyOIDCConnectScopes:              "openid email profile",
			identity.SettingKeyOIDCConnectRedirectURL:         "https://example.com/api/v1/auth/oauth/oidc/callback",
			identity.SettingKeyOIDCConnectFrontendRedirectURL: "/auth/oidc/callback",
			identity.SettingKeyOIDCConnectTokenAuthMethod:     "client_secret_post",
			identity.SettingKeyOIDCConnectUsePKCE:             "true",
			identity.SettingKeyOIDCConnectValidateIDToken:     "true",
			identity.SettingKeyOIDCConnectAllowedSigningAlgs:  "RS256",
			identity.SettingKeyOIDCConnectClockSkewSeconds:    "120",
		},
	}
	options, _ := newCompositeSettingsHTTPFixture(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
	handler := settingshttp.NewHandler(options)

	body := map[string]any{
		"promo_code_enabled":                true,
		"oidc_connect_enabled":              true,
		"oidc_connect_use_pkce":             false,
		"oidc_connect_validate_id_token":    false,
		"oidc_connect_allowed_signing_algs": "",
	}
	rawBody, err := json.Marshal(body)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(rawBody))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.UpdateSettings(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "false", repo.values[identity.SettingKeyOIDCConnectUsePKCE])
	require.Equal(t, "false", repo.values[identity.SettingKeyOIDCConnectValidateIDToken])

	var resp response.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	data, ok := resp.Data.(map[string]any)
	require.True(t, ok)
	require.Equal(t, false, data["oidc_connect_use_pkce"])
	require.Equal(t, false, data["oidc_connect_validate_id_token"])
}

func TestSettingHandler_UpdateSettings_DoesNotSolidifyImplicitOIDCSecurityDefaultsOnLegacyUpgrade(t *testing.T) {
	repo := &settingHandlerRepoStub{
		values: map[string]string{
			promotion.SettingKeyPromoCodeEnabled:               "true",
			identity.SettingKeyOIDCConnectEnabled:              "true",
			identity.SettingKeyOIDCConnectProviderName:         "OIDC",
			identity.SettingKeyOIDCConnectClientID:             "oidc-client",
			identity.SettingKeyOIDCConnectClientSecret:         "oidc-secret",
			identity.SettingKeyOIDCConnectIssuerURL:            "https://issuer.example.com",
			identity.SettingKeyOIDCConnectAuthorizeURL:         "https://issuer.example.com/auth",
			identity.SettingKeyOIDCConnectTokenURL:             "https://issuer.example.com/token",
			identity.SettingKeyOIDCConnectUserInfoURL:          "https://issuer.example.com/userinfo",
			identity.SettingKeyOIDCConnectJWKSURL:              "https://issuer.example.com/jwks",
			identity.SettingKeyOIDCConnectScopes:               "openid email profile",
			identity.SettingKeyOIDCConnectRedirectURL:          "https://example.com/api/v1/auth/oauth/oidc/callback",
			identity.SettingKeyOIDCConnectFrontendRedirectURL:  "/auth/oidc/callback",
			identity.SettingKeyOIDCConnectTokenAuthMethod:      "client_secret_post",
			identity.SettingKeyOIDCConnectAllowedSigningAlgs:   "RS256",
			identity.SettingKeyOIDCConnectClockSkewSeconds:     "120",
			identity.SettingKeyOIDCConnectRequireEmailVerified: "false",
			identity.SettingKeyOIDCConnectUserInfoEmailPath:    "",
			identity.SettingKeyOIDCConnectUserInfoIDPath:       "",
			identity.SettingKeyOIDCConnectUserInfoUsernamePath: "",
		},
	}
	options, _ := newCompositeSettingsHTTPFixture(repo, &config.Config{
		Default: config.DefaultConfig{UserConcurrency: 5},
		OIDC: config.OIDCConnectConfig{
			Enabled:             true,
			ProviderName:        "OIDC",
			ClientID:            "oidc-client",
			ClientSecret:        "oidc-secret",
			IssuerURL:           "https://issuer.example.com",
			AuthorizeURL:        "https://issuer.example.com/auth",
			TokenURL:            "https://issuer.example.com/token",
			UserInfoURL:         "https://issuer.example.com/userinfo",
			JWKSURL:             "https://issuer.example.com/jwks",
			Scopes:              "openid email profile",
			RedirectURL:         "https://example.com/api/v1/auth/oauth/oidc/callback",
			FrontendRedirectURL: "/auth/oidc/callback",
			TokenAuthMethod:     "client_secret_post",
			UsePKCE:             true,
			ValidateIDToken:     true,
			AllowedSigningAlgs:  "RS256",
			ClockSkewSeconds:    120,
		},
	})
	handler := settingshttp.NewHandler(options)

	body := map[string]any{
		"promo_code_enabled":   true,
		"oidc_connect_enabled": true,
	}
	rawBody, err := json.Marshal(body)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(rawBody))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.UpdateSettings(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "false", repo.values[identity.SettingKeyOIDCConnectUsePKCE])
	require.Equal(t, "false", repo.values[identity.SettingKeyOIDCConnectValidateIDToken])
}

func TestSettingHandler_UpdateSettings_RejectsInvalidPaymentVisibleMethodSource(t *testing.T) {
	repo := &settingHandlerRepoStub{
		values: map[string]string{
			promotion.SettingKeyPromoCodeEnabled: "true",
		},
	}
	options, _ := newCompositeSettingsHTTPFixture(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
	handler := settingshttp.NewHandler(options)

	body := map[string]any{
		"promo_code_enabled":                   true,
		"payment_visible_method_alipay_source": "bogus",
	}
	rawBody, err := json.Marshal(body)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(rawBody))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.UpdateSettings(c)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.NotContains(t, repo.values, paymentcore.SettingPaymentVisibleMethodAlipaySource)
}

func TestSettingHandler_UpdateSettings_DoesNotPersistPartialSystemSettingsWhenAuthSourceDefaultsFail(t *testing.T) {
	repo := &failingAuthSourceSettingsRepoStub{
		values: map[string]string{
			identity.SettingKeyRegistrationEnabled:                 "false",
			promotion.SettingKeyPromoCodeEnabled:                   "true",
			identity.SettingKeyAuthSourceDefaultEmailBalance:       "9.5",
			identity.SettingKeyAuthSourceDefaultEmailConcurrency:   "8",
			identity.SettingKeyAuthSourceDefaultEmailSubscriptions: `[{"plan_id":31}]`,
		},
		err: errors.New("write auth source defaults failed"),
	}
	options, _ := newCompositeSettingsHTTPFixture(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
	handler := settingshttp.NewHandler(options)

	body := map[string]any{
		"registration_enabled":              true,
		"promo_code_enabled":                true,
		"auth_source_default_email_balance": 12.75,
	}
	rawBody, err := json.Marshal(body)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(rawBody))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.UpdateSettings(c)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, "false", repo.values[identity.SettingKeyRegistrationEnabled])
	require.Equal(t, "9.5", repo.values[identity.SettingKeyAuthSourceDefaultEmailBalance])
}

func TestDiffSettings_IncludesAuthSourceDefaultsAndForceEmail(t *testing.T) {
	changed := settingshttp.DiffSettings(
		&composite.Snapshot{},
		&composite.Snapshot{},
		&identity.AuthSourceDefaultSettings{
			Email: identity.ProviderDefaultGrantSettings{
				Balance:          0,
				Concurrency:      5,
				Subscriptions:    nil,
				GrantOnSignup:    true,
				GrantOnFirstBind: false,
			},
			ForceEmailOnThirdPartySignup: false,
		},
		&identity.AuthSourceDefaultSettings{
			Email: identity.ProviderDefaultGrantSettings{
				Balance:          12.5,
				Concurrency:      7,
				Subscriptions:    []identity.DefaultSubscriptionSetting{{PlanID: 21}},
				GrantOnSignup:    false,
				GrantOnFirstBind: true,
			},
			ForceEmailOnThirdPartySignup: true,
		},
		settingsdto.UpdateSettingsRequest{},
	)

	require.Contains(t, changed, "auth_source_default_email_balance")
	require.Contains(t, changed, "auth_source_default_email_concurrency")
	require.Contains(t, changed, "auth_source_default_email_subscriptions")
	require.Contains(t, changed, "auth_source_default_email_grant_on_signup")
	require.Contains(t, changed, "auth_source_default_email_grant_on_first_bind")
	require.Contains(t, changed, "force_email_on_third_party_signup")
}

func newDingTalkSettingsHandler() (*settingshttp.Handler, *settingHandlerRepoStub) {
	repo := &settingHandlerRepoStub{values: map[string]string{}}
	options, _ := newCompositeSettingsHTTPFixture(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
	handler := settingshttp.NewHandler(options)
	return handler, repo
}

// baseValidDingTalkBody returns a minimal valid DingTalk settings body.
func baseValidDingTalkBody() map[string]any {
	return map[string]any{
		"dingtalk_connect_enabled":                 true,
		"dingtalk_connect_client_id":               "test-client-id",
		"dingtalk_connect_client_secret":           "test-client-secret",
		"dingtalk_connect_redirect_url":            "https://example.com/auth/dingtalk/callback",
		"dingtalk_connect_corp_restriction_policy": "none",
	}
}

// TestSettingsPUT_DingTalk_V3_InternalOnlyAllowsEmptyCorpID checks successful persistence
// when internal_only has an empty internal_corp_id.
func TestSettingsPUT_DingTalk_V3_InternalOnlyAllowsEmptyCorpID(t *testing.T) {
	handler, _ := newDingTalkSettingsHandler()

	body := baseValidDingTalkBody()
	body["dingtalk_connect_corp_restriction_policy"] = "internal_only"
	body["dingtalk_connect_internal_corp_id"] = ""

	rawBody, err := json.Marshal(body)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(rawBody))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.UpdateSettings(c)

	require.Equal(t, http.StatusOK, rec.Code)
}

// TestSettingsPUT_DingTalk_HappyPath_None checks successful persistence with the none policy.
func TestSettingsPUT_DingTalk_HappyPath_None(t *testing.T) {
	handler, _ := newDingTalkSettingsHandler()

	body := baseValidDingTalkBody()
	body["dingtalk_connect_corp_restriction_policy"] = "none"

	rawBody, err := json.Marshal(body)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(rawBody))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.UpdateSettings(c)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp response.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	data, ok := resp.Data.(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, data["dingtalk_connect_enabled"])
}

// TestSettingsPUT_DingTalk_HappyPath_InternalOnly_WithCorpID checks internal_only with a corporate ID.
func TestSettingsPUT_DingTalk_HappyPath_InternalOnly_WithCorpID(t *testing.T) {
	handler, _ := newDingTalkSettingsHandler()

	body := baseValidDingTalkBody()
	body["dingtalk_connect_corp_restriction_policy"] = "internal_only"
	body["dingtalk_connect_internal_corp_id"] = "ding-corp-123"

	rawBody, err := json.Marshal(body)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(rawBody))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.UpdateSettings(c)

	require.Equal(t, http.StatusOK, rec.Code)
}

// TestSettingsPUT_DingTalk_BypassRegistration_RoundTrip checks persistence and response of bypass_registration.
// The internal_only policy permits bypass_registration.
func TestSettingsPUT_DingTalk_BypassRegistration_RoundTrip(t *testing.T) {
	handler, _ := newDingTalkSettingsHandler()

	body := baseValidDingTalkBody()
	body["dingtalk_connect_corp_restriction_policy"] = "internal_only"
	body["dingtalk_connect_bypass_registration"] = true

	rawBody, err := json.Marshal(body)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(rawBody))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.UpdateSettings(c)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp response.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	data, ok := resp.Data.(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, data["dingtalk_connect_bypass_registration"])
}

// TestSettingsPUT_DingTalk_Disabled_SkipsValidation checks that disabled DingTalk accepts incomplete credentials.
// An empty client_id is accepted here; enabling DingTalk would reject it with
// "Client ID is required when enabled".
func TestSettingsPUT_DingTalk_Disabled_SkipsValidation(t *testing.T) {
	handler, _ := newDingTalkSettingsHandler()

	body := map[string]any{
		"dingtalk_connect_enabled":                 false,
		"dingtalk_connect_client_id":               "",
		"dingtalk_connect_corp_restriction_policy": "internal_only",
	}

	rawBody, err := json.Marshal(body)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(rawBody))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.UpdateSettings(c)

	require.Equal(t, http.StatusOK, rec.Code)
}

// TestSettingsPUT_DingTalk_SyncFlags_InternalOnly_RoundTrip checks all three sync switches under internal_only.
func TestSettingsPUT_DingTalk_SyncFlags_InternalOnly_RoundTrip(t *testing.T) {
	handler, _ := newDingTalkSettingsHandler()

	body := baseValidDingTalkBody()
	body["dingtalk_connect_corp_restriction_policy"] = "internal_only"
	body["dingtalk_connect_sync_corp_email"] = true
	body["dingtalk_connect_sync_display_name"] = true
	body["dingtalk_connect_sync_dept"] = true

	rawBody, err := json.Marshal(body)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(rawBody))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.UpdateSettings(c)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp response.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	data, ok := resp.Data.(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, data["dingtalk_connect_sync_corp_email"], "sync_corp_email should be true for internal_only")
	require.Equal(t, true, data["dingtalk_connect_sync_display_name"], "sync_display_name should be true for internal_only")
	require.Equal(t, true, data["dingtalk_connect_sync_dept"], "sync_dept should be true for internal_only")
}

// TestSettingsPUT_DingTalk_SyncFlags_PolicyNone_CoercedToFalse checks that none disables all three sync switches.
func TestSettingsPUT_DingTalk_SyncFlags_PolicyNone_CoercedToFalse(t *testing.T) {
	handler, _ := newDingTalkSettingsHandler()

	body := baseValidDingTalkBody()
	body["dingtalk_connect_corp_restriction_policy"] = "none"
	body["dingtalk_connect_sync_corp_email"] = true
	body["dingtalk_connect_sync_display_name"] = true
	body["dingtalk_connect_sync_dept"] = true

	rawBody, err := json.Marshal(body)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(rawBody))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.UpdateSettings(c)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp response.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	data, ok := resp.Data.(map[string]any)
	require.True(t, ok)
	require.Equal(t, false, data["dingtalk_connect_sync_corp_email"], "sync_corp_email must be coerced to false when policy=none")
	require.Equal(t, false, data["dingtalk_connect_sync_display_name"], "sync_display_name must be coerced to false when policy=none")
	require.Equal(t, false, data["dingtalk_connect_sync_dept"], "sync_dept must be coerced to false when policy=none")
}

// TestSettingsPUT_DingTalk_StaleWhitelist_CoercedToNone checks compatibility with whitelist input.
// An API request with corp_restriction_policy=whitelist
// is normalized to none before persistence.
func TestSettingsPUT_DingTalk_StaleWhitelist_CoercedToNone(t *testing.T) {
	handler, repo := newDingTalkSettingsHandler()

	body := baseValidDingTalkBody()
	body["dingtalk_connect_corp_restriction_policy"] = "whitelist"

	rawBody, err := json.Marshal(body)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(rawBody))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.UpdateSettings(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "none", repo.values[identity.SettingKeyDingTalkConnectCorpRestrictionPolicy],
		"stale whitelist 应在写入路径被 coerce 为 none")
}

// TestSettingsPUT_DingTalk_SyncAttrKey_RoundTrip checks custom attribute keys and defaults for empty values.
func TestSettingsPUT_DingTalk_SyncAttrKey_RoundTrip(t *testing.T) {
	t.Run("custom_attr_keys_saved", func(t *testing.T) {
		handler, repo := newDingTalkSettingsHandler()

		body := baseValidDingTalkBody()
		body["dingtalk_connect_corp_restriction_policy"] = "internal_only"
		body["dingtalk_connect_sync_corp_email"] = true
		body["dingtalk_connect_sync_display_name"] = true
		body["dingtalk_connect_sync_dept"] = true
		body["dingtalk_connect_sync_corp_email_attr_key"] = "my_email_attr"
		body["dingtalk_connect_sync_display_name_attr_key"] = "my_name_attr"
		body["dingtalk_connect_sync_dept_attr_key"] = "my_dept_attr"

		rawBody, err := json.Marshal(body)
		require.NoError(t, err)

		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(rawBody))
		c.Request.Header.Set("Content-Type", "application/json")

		handler.UpdateSettings(c)

		require.Equal(t, http.StatusOK, rec.Code)

		require.Equal(t, "my_email_attr", repo.values[identity.SettingKeyDingTalkConnectSyncCorpEmailAttrKey])
		require.Equal(t, "my_name_attr", repo.values[identity.SettingKeyDingTalkConnectSyncDisplayNameAttrKey])
		require.Equal(t, "my_dept_attr", repo.values[identity.SettingKeyDingTalkConnectSyncDeptAttrKey])

		var resp response.Response
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		data, ok := resp.Data.(map[string]any)
		require.True(t, ok)
		require.Equal(t, "my_email_attr", data["dingtalk_connect_sync_corp_email_attr_key"])
		require.Equal(t, "my_name_attr", data["dingtalk_connect_sync_display_name_attr_key"])
		require.Equal(t, "my_dept_attr", data["dingtalk_connect_sync_dept_attr_key"])
	})

	t.Run("empty_attr_keys_fallback_to_defaults", func(t *testing.T) {
		handler, repo := newDingTalkSettingsHandler()

		body := baseValidDingTalkBody()
		body["dingtalk_connect_corp_restriction_policy"] = "internal_only"
		// Explicit empty attribute keys request the defaults.
		body["dingtalk_connect_sync_corp_email_attr_key"] = ""
		body["dingtalk_connect_sync_display_name_attr_key"] = ""
		body["dingtalk_connect_sync_dept_attr_key"] = ""

		rawBody, err := json.Marshal(body)
		require.NoError(t, err)

		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(rawBody))
		c.Request.Header.Set("Content-Type", "application/json")

		handler.UpdateSettings(c)

		require.Equal(t, http.StatusOK, rec.Code)

		// The resolved defaults are persisted.
		require.Equal(t, "dingtalk_email", repo.values[identity.SettingKeyDingTalkConnectSyncCorpEmailAttrKey])
		require.Equal(t, "dingtalk_name", repo.values[identity.SettingKeyDingTalkConnectSyncDisplayNameAttrKey])
		require.Equal(t, "dingtalk_department", repo.values[identity.SettingKeyDingTalkConnectSyncDeptAttrKey])
	})
}

func TestDiffSettingsTracksGoogleOneTapSwitch(t *testing.T) {
	before := &composite.Snapshot{GoogleOneTapEnabled: false}
	after := &composite.Snapshot{GoogleOneTapEnabled: true}

	changed := settingshttp.DiffSettings(before, after, nil, nil, settingsdto.UpdateSettingsRequest{})

	require.Contains(t, changed, "google_one_tap_enabled")
}

func TestUpdateSettingsNormalizedModuleFields(t *testing.T) {
	tests := []struct {
		field         string
		stored        string
		storedValue   any
		emptyValue    any
		emptyResponse any
		emptyStored   string
		pointerField  bool
	}{
		{field: "default_balance", stored: "12.00000000", storedValue: float64(12), emptyValue: float64(0), emptyStored: "0.00000000"},
		{field: "default_subscriptions", stored: `[{"plan_id":17}]`, storedValue: []any{map[string]any{"plan_id": float64(17)}}, emptyValue: []any{}, emptyStored: "[]"},
		{field: "balance_low_notify_enabled", stored: "true", storedValue: true, emptyValue: false, emptyStored: "false", pointerField: true},
		{field: "balance_low_notify_threshold", stored: "3.00000000", storedValue: float64(3), emptyValue: float64(0), emptyStored: "0.00000000", pointerField: true},
		{field: "enable_model_fallback", stored: "true", storedValue: true, emptyValue: false, emptyStored: "false"},
		{field: "fallback_model_openai", stored: "gpt-fallback", storedValue: "gpt-fallback", emptyValue: "", emptyResponse: "gpt-4o", emptyStored: ""},
		{field: "site_name", stored: "Example Gateway", storedValue: "Example Gateway", emptyValue: "", emptyResponse: "TokenRouter", emptyStored: ""},
		{field: "min_claude_code_version", stored: "1.2.3", storedValue: "1.2.3", emptyValue: "", emptyStored: ""},
		{field: "backend_mode_enabled", stored: "true", storedValue: true, emptyValue: false, emptyStored: "false"},
	}
	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			emptyResponse := tt.emptyValue
			if tt.emptyResponse != nil {
				emptyResponse = tt.emptyResponse
			}
			for _, mode := range []string{"omitted", "empty", "null"} {
				t.Run(mode, func(t *testing.T) {
					h, repo := newStepUpSwitchTestHandler(t, map[string]string{tt.field: tt.stored})
					payload := map[string]any{"risk_control_enabled": true}
					wantStored, wantValue := tt.stored, tt.storedValue
					if mode == "empty" {
						payload[tt.field] = tt.emptyValue
						wantStored, wantValue = tt.emptyStored, emptyResponse
					}
					if mode == "null" {
						payload[tt.field] = nil
						if !tt.pointerField {
							wantStored, wantValue = tt.emptyStored, emptyResponse
						}
					}
					rec := doUpdateSettings(t, h, payload, nil)
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					require.Equal(t, wantStored, repo.values[tt.field])
					var response struct {
						Data map[string]any `json:"data"`
					}
					require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
					require.Equal(t, wantValue, response.Data[tt.field])
				})
			}
		})
	}
}

func TestUpdateSettingsPartialPayloadKeepsUnsentKeys(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		site.SettingKeySiteName:             "Example Gateway",
		site.SettingKeySiteSubtitle:         "Example Gateway Platform",
		notification.SettingKeySMTPHost:     "smtp.example.com",
		notification.SettingKeySMTPFrom:     "noreply@example.com",
		identity.SettingKeyTurnstileEnabled: "true",
	})

	rec := doUpdateSettings(t, h, map[string]any{"risk_control_enabled": true}, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, "true", repo.values[moderation.SettingKeyRiskControlEnabled],
		"调用方明确发送的字段必须写入")

	require.Equal(t, "Example Gateway", repo.values[site.SettingKeySiteName])
	require.Equal(t, "Example Gateway Platform", repo.values[site.SettingKeySiteSubtitle])
	require.Equal(t, "smtp.example.com", repo.values[notification.SettingKeySMTPHost])
	require.Equal(t, "noreply@example.com", repo.values[notification.SettingKeySMTPFrom])
	require.Equal(t, "true", repo.values[identity.SettingKeyTurnstileEnabled])
}

// TestUpdateSettingsFullPayloadStillClearsSentEmptyFields checks that an explicit empty value clears the setting.
func TestUpdateSettingsFullPayloadStillClearsSentEmptyFields(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		site.SettingKeySiteName: "Example Gateway",
	})

	rec := doUpdateSettings(t, h, map[string]any{"site_name": ""}, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, "", repo.values[site.SettingKeySiteName],
		"明确发送的空值表示主动清空，而不是省略字段")
}

// TestUpdateSettingsSMTPFromAliasIsWritable checks the smtp_from_email request alias.
// The alias maps the request field to the SMTP sender storage key.
func TestUpdateSettingsSMTPFromAliasIsWritable(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		notification.SettingKeySMTPFrom: "old@example.com",
	})

	rec := doUpdateSettings(t, h, map[string]any{"smtp_from_email": "new@example.com"}, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, "new@example.com", repo.values[notification.SettingKeySMTPFrom])
}

// TestUpdateSettingsCreativeEnabledPartialSemantics checks that explicit values are saved and omission retains stored values.
func TestUpdateSettingsCreativeEnabledPartialSemantics(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		creative.SettingKeyCreativeEnabled: "true",
	})

	rec := doUpdateSettings(t, h, map[string]any{"creative_enabled": false}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "false", repo.values[creative.SettingKeyCreativeEnabled])

	h2, repo2 := newStepUpSwitchTestHandler(t, map[string]string{
		creative.SettingKeyCreativeEnabled: "false",
	})
	rec = doUpdateSettings(t, h2, map[string]any{"risk_control_enabled": true}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "false", repo2.values[creative.SettingKeyCreativeEnabled],
		"未发送 creative_enabled 时必须保留存储值")
}

func TestUpdateSettingsCreativeModelSettingsPartialSemantics(t *testing.T) {
	stored := `[{"group_id":12,"model":"gpt-image-2","operations":["generate"]}]`
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		creative.SettingKeyCreativeModelSettings: stored,
	})

	// Omitting the allowlist retains the stored model settings.
	rec := doUpdateSettings(t, h, map[string]any{"risk_control_enabled": true}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, stored, repo.values[creative.SettingKeyCreativeModelSettings])

	// An empty array disables all creative image models.
	rec = doUpdateSettings(t, h, map[string]any{"creative_model_settings": []any{}}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, "[]", repo.values[creative.SettingKeyCreativeModelSettings])

	// Invalid capabilities are rejected before the stored settings are replaced.
	rec = doUpdateSettings(t, h, map[string]any{
		"creative_model_settings": []map[string]any{{
			"group_id":   12,
			"model":      "gpt-image-2",
			"operations": []string{"upscale"},
		}},
	}, nil)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.JSONEq(t, "[]", repo.values[creative.SettingKeyCreativeModelSettings])
}

// TestUpdateSettingsCreativeWorkerCountPartialSemantics checks that omission retains the stored worker count.
func TestUpdateSettingsCreativeWorkerCountPartialSemantics(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		creative.SettingKeyCreativeWorkerCount: "4",
	})

	rec := doUpdateSettings(t, h, map[string]any{"creative_worker_count": 7}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "7", repo.values[creative.SettingKeyCreativeWorkerCount])

	rec = doUpdateSettings(t, h, map[string]any{"risk_control_enabled": true}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "7", repo.values[creative.SettingKeyCreativeWorkerCount])
}

// TestUpdateSettingsRejectsInvalidCreativeWorkerCount checks rejection of nonpositive worker counts.
func TestUpdateSettingsRejectsInvalidCreativeWorkerCount(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		creative.SettingKeyCreativeWorkerCount: "4",
	})

	for _, value := range []int{0, -1} {
		rec := doUpdateSettings(t, h, map[string]any{"creative_worker_count": value}, nil)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, "4", repo.values[creative.SettingKeyCreativeWorkerCount])
	}
}

// TestUpdateSettingsNormalizesGeminiInpaintBeforeSave checks that normalization removes unsupported Gemini inpaint before saving.
func TestUpdateSettingsNormalizesGeminiInpaintBeforeSave(t *testing.T) {
	repo := &settingHandlerRepoStub{values: map[string]string{}}
	options, _ := newCompositeSettingsHTTPFixture(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
	options.Creative = &creativeModelCandidateReaderStub{sanitizeGemini: true}
	h := settingshttp.NewHandler(options)

	rec := doUpdateSettings(t, h, map[string]any{
		"creative_model_settings": []map[string]any{{
			"group_id":   12,
			"model":      "gemini-3.1-flash-image",
			"operations": []string{"generate", "inpaint"},
		}},
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `[{"group_id":12,"model":"gemini-3.1-flash-image","operations":["generate"]}]`, repo.values[creative.SettingKeyCreativeModelSettings])
}

func (s *creativeModelCandidateReaderStub) ListCreativeModelCandidates(context.Context) ([]creative.CreativeModelCandidate, error) {
	return s.candidates, nil
}

func (s *creativeModelCandidateReaderStub) NormalizeCreativeModelSettingsForSave(_ context.Context, input []creative.CreativeModelSetting) ([]creative.CreativeModelSetting, error) {
	if !s.sanitizeGemini {
		return input, nil
	}
	normalized, err := creative.NormalizeCreativeModelSettings(input)
	if err != nil {
		return nil, err
	}
	for i := range normalized {
		if normalized[i].GroupID != 12 {
			continue
		}
		filtered := normalized[i].Operations[:0]
		for _, operation := range normalized[i].Operations {
			if operation != creative.CreativeOperationInpaint {
				filtered = append(filtered, operation)
			}
		}
		normalized[i].Operations = filtered
	}
	return normalized, nil
}

func TestListCreativeModelCandidates(t *testing.T) {
	h := creativehttp.NewSettingsHandler(&creativeModelCandidateReaderStub{candidates: []creative.CreativeModelCandidate{{
		GroupID:    12,
		GroupName:  "Exclusive Images",
		Platform:   "grok",
		Model:      "grok-imagine",
		Operations: []string{creative.CreativeOperationGenerate},
	}}}, nil)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/creative-model-candidates", nil)
	h.ListCreativeModelCandidates(c)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "Exclusive Images")
	require.Contains(t, rec.Body.String(), "grok-imagine")
}

// TestGetCreativeWorkerStatus checks callback snapshots and the inactive zero status returned by a nil worker.
func TestGetCreativeWorkerStatus(t *testing.T) {
	var worker *creative.CreativeWorkerRuntime
	h := creativehttp.NewSettingsHandler(nil, worker.Status)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/creative-worker-status", nil)
	h.GetCreativeWorkerStatus(c)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"running":false`)
	require.Contains(t, rec.Body.String(), `"worker_count":0`)
	require.Contains(t, rec.Body.String(), `"busy_workers":0`)

	h = creativehttp.NewSettingsHandler(nil, func() creative.CreativeWorkerStatus {
		return creative.CreativeWorkerStatus{Running: true, WorkerCount: 128, BusyWorkers: 60}
	})
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/creative-worker-status", nil)
	h.GetCreativeWorkerStatus(c)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"running":true`)
	require.Contains(t, rec.Body.String(), `"worker_count":128`)
	require.Contains(t, rec.Body.String(), `"busy_workers":60`)
}

func TestUpdateSettingsGrokDefaultBaseURLModeIsWritable(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		gateway.SettingKeyGrokDefaultBaseURLMode: "cli",
	})

	rec := doUpdateSettings(t, h, map[string]any{
		"grok_default_base_url_mode": "eu-west-1",
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "eu-west-1", repo.values[gateway.SettingKeyGrokDefaultBaseURLMode])
}

func TestUpdateSettingsRejectsTwoCaptchaProviders(t *testing.T) {
	h, _ := newStepUpSwitchTestHandler(t, map[string]string{
		identity.SettingKeyTurnstileEnabled:   "true",
		identity.SettingKeyTurnstileSiteKey:   "site-key",
		identity.SettingKeyTurnstileSecretKey: "turnstile-secret",
	})

	rec := doUpdateSettings(t, h, map[string]any{
		"turnstile_enabled":                true,
		"turnstile_site_key":               "site-key",
		"turnstile_secret_key":             "turnstile-secret",
		"tencent_captcha_enabled":          true,
		"tencent_captcha_app_id":           "123456789",
		"tencent_captcha_app_secret_key":   "app-secret",
		"tencent_captcha_cloud_secret_id":  "cloud-secret-id",
		"tencent_captcha_cloud_secret_key": "cloud-secret-key",
	}, nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "cannot be enabled at the same time")
}

func TestUpdateSettingsRequiresFourTencentCaptchaCredentialsWhenEnabled(t *testing.T) {
	h, _ := newStepUpSwitchTestHandler(t, map[string]string{})

	rec := doUpdateSettings(t, h, map[string]any{
		"tencent_captcha_enabled": true,
		"tencent_captcha_app_id":  "123456789",
	}, nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "AppSecretKey")
}

func TestUpdateSettingsRetainsStoredTencentCaptchaCredentialsWhenInputsEmpty(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		identity.SettingKeyTencentCaptchaAppSecretKey:   "stored-app-secret",
		identity.SettingKeyTencentCaptchaCloudSecretID:  "stored-cloud-secret-id",
		identity.SettingKeyTencentCaptchaCloudSecretKey: "stored-cloud-secret-key",
	})

	rec := doUpdateSettings(t, h, map[string]any{
		"tencent_captcha_enabled":          true,
		"tencent_captcha_app_id":           "123456789",
		"tencent_captcha_app_secret_key":   "",
		"tencent_captcha_cloud_secret_id":  "",
		"tencent_captcha_cloud_secret_key": "",
	}, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "stored-app-secret", repo.values[identity.SettingKeyTencentCaptchaAppSecretKey])
	require.Equal(t, "stored-cloud-secret-id", repo.values[identity.SettingKeyTencentCaptchaCloudSecretID])
	require.Equal(t, "stored-cloud-secret-key", repo.values[identity.SettingKeyTencentCaptchaCloudSecretKey])
}

// TestUpdateSettingsPartialPayloadKeepsTencentCaptchaRegion checks that a partial update retains the configured region.
// Resetting an international deployment to China would select the wrong SDK and verification endpoint.
func TestUpdateSettingsPartialPayloadKeepsTencentCaptchaRegion(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		identity.SettingKeyTencentCaptchaRegion: identity.TencentCaptchaRegionINTL,
	})

	rec := doUpdateSettings(t, h, map[string]any{"risk_control_enabled": true}, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, identity.TencentCaptchaRegionINTL,
		repo.values[identity.SettingKeyTencentCaptchaRegion])
}

func TestUpdateSettingsNormalizesUnknownTencentCaptchaRegion(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		identity.SettingKeyTencentCaptchaRegion: identity.TencentCaptchaRegionINTL,
	})

	rec := doUpdateSettings(t, h, map[string]any{"tencent_captcha_region": "sgp"}, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, identity.TencentCaptchaRegionCN,
		repo.values[identity.SettingKeyTencentCaptchaRegion],
		"未知站点必须落回中国站，不能写入无法识别的值")
}

func TestUpdateSettingsWritesTencentCaptchaRegionWhenSent(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})

	rec := doUpdateSettings(t, h, map[string]any{"tencent_captcha_region": "intl"}, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, identity.TencentCaptchaRegionINTL,
		repo.values[identity.SettingKeyTencentCaptchaRegion])
}

func TestUpdateSettingsValidatesTencentCaptchaAppIDWhenEnabledFlagIsOmitted(t *testing.T) {
	h, _ := newStepUpSwitchTestHandler(t, map[string]string{
		identity.SettingKeyTencentCaptchaEnabled:        "true",
		identity.SettingKeyTencentCaptchaAppID:          "123456789",
		identity.SettingKeyTencentCaptchaAppSecretKey:   "stored-app-secret",
		identity.SettingKeyTencentCaptchaCloudSecretID:  "stored-cloud-secret-id",
		identity.SettingKeyTencentCaptchaCloudSecretKey: "stored-cloud-secret-key",
	})

	rec := doUpdateSettings(t, h, map[string]any{
		"tencent_captcha_app_id": "not-a-number",
	}, nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "positive integer")
}

func newStepUpSwitchTestHandler(t *testing.T, stored map[string]string) (*settingshttp.Handler, *settingHandlerRepoStub) {
	t.Helper()

	repo := &settingHandlerRepoStub{values: stored}
	options, _ := newCompositeSettingsHTTPFixture(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
	return settingshttp.NewHandler(options), repo
}

// TestUpdateSettingsEnableStepUpRejectsWithoutSession checks the dedicated error for enabling step-up without authentication.
func TestUpdateSettingsEnableStepUpRejectsWithoutSession(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})

	rec := doUpdateSettings(t, h, map[string]any{"step_up_enabled": true}, nil)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_ENABLE_REQUIRES_TOTP")
	require.NotEqual(t, "true", repo.values[identity.SettingKeyStepUpEnabled])
}

// TestUpdateSettingsEnableStepUpRejectsAdminAPIKey checks the dedicated reason for rejecting admin API key authentication.
func TestUpdateSettingsEnableStepUpRejectsAdminAPIKey(t *testing.T) {
	h, _ := newStepUpSwitchTestHandler(t, map[string]string{})

	rec := doUpdateSettings(t, h, map[string]any{"step_up_enabled": true}, func(c *gin.Context) {
		c.Set("auth_method", audit.AuditAuthMethodAdminAPIKey)
	})

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_ADMIN_API_KEY_FORBIDDEN")
}

// TestUpdateSettingsEnableStepUpFailsClosedWithoutUserService checks a 500 response when the authenticated user cannot be validated.
func TestUpdateSettingsEnableStepUpFailsClosedWithoutUserService(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})

	rec := doUpdateSettings(t, h, map[string]any{"step_up_enabled": true}, func(c *gin.Context) {
		c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: 1})
	})

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.NotEqual(t, "true", repo.values[identity.SettingKeyStepUpEnabled])
}

// TestUpdateSettingsDisableStepUpRequiresStepUp checks a 401 response when disabling step-up without authentication.
func TestUpdateSettingsDisableStepUpRequiresStepUp(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		identity.SettingKeyStepUpEnabled: "true",
	})

	rec := doUpdateSettings(t, h, map[string]any{"step_up_enabled": false}, nil)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, "true", repo.values[identity.SettingKeyStepUpEnabled])
}

// TestUpdateSettingsDisableStepUpRejectsAdminAPIKey checks a 403 response when an admin API key disables step-up.
func TestUpdateSettingsDisableStepUpRejectsAdminAPIKey(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		identity.SettingKeyStepUpEnabled: "true",
	})

	rec := doUpdateSettings(t, h, map[string]any{"step_up_enabled": false}, func(c *gin.Context) {
		c.Set("auth_method", audit.AuditAuthMethodAdminAPIKey)
	})

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_ADMIN_API_KEY_FORBIDDEN")
	require.Equal(t, "true", repo.values[identity.SettingKeyStepUpEnabled])
}

// TestUpdateSettingsStepUpNoTransitionSkipsGate checks ordinary saving when step-up stays disabled.
func TestUpdateSettingsStepUpNoTransitionSkipsGate(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})

	rec := doUpdateSettings(t, h, map[string]any{"step_up_enabled": false}, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "false", repo.values[identity.SettingKeyStepUpEnabled])
	// Session IP/User-Agent binding defaults to false and is persisted when omitted.
	require.Equal(t, "false", repo.values[identity.SettingKeySessionBindingEnabled])
}

// TestUpdateSettingsStepUpKeepEnabledSkipsGate checks ordinary saving when step-up stays enabled.
func TestUpdateSettingsStepUpKeepEnabledSkipsGate(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		identity.SettingKeyStepUpEnabled: "true",
	})

	rec := doUpdateSettings(t, h, map[string]any{"step_up_enabled": true}, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "true", repo.values[identity.SettingKeyStepUpEnabled])
}

// TestUpdateSettingsOmittedSecuritySwitchesKeepStoredValues checks that omitted security switches retain their stored values.
// Keeping the switches enabled does not trigger transition checks.
func TestUpdateSettingsOmittedSecuritySwitchesKeepStoredValues(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		identity.SettingKeyStepUpEnabled:                       "true",
		identity.SettingKeySessionBindingEnabled:               "true",
		identity.SettingKeyRegistrationEmailDomainQuotaEnabled: "true",
		identity.SettingKeyUserEmailChangeEnabled:              "true",
	})

	rec := doUpdateSettings(t, h, map[string]any{"registration_enabled": true}, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "true", repo.values[identity.SettingKeyStepUpEnabled])
	require.Equal(t, "true", repo.values[identity.SettingKeySessionBindingEnabled])
	require.Equal(t, "true", repo.values[identity.SettingKeyRegistrationEmailDomainQuotaEnabled])
	require.Equal(t, "true", repo.values[identity.SettingKeyUserEmailChangeEnabled])
}

// TestUpdateSettingsOmittedSecuritySwitchesKeepDisabled checks disabled defaults for omitted switches.
func TestUpdateSettingsOmittedSecuritySwitchesKeepDisabled(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})

	rec := doUpdateSettings(t, h, map[string]any{"registration_enabled": true}, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "false", repo.values[identity.SettingKeyStepUpEnabled])
	require.Equal(t, "false", repo.values[identity.SettingKeySessionBindingEnabled])
	require.Equal(t, "false", repo.values[identity.SettingKeyRegistrationEmailDomainQuotaEnabled])
	require.Equal(t, "false", repo.values[identity.SettingKeyUserEmailChangeEnabled])
}

// TestUpdateSettingsEnablesUserEmailChange checks that a partial update can enable email changes.
func TestUpdateSettingsEnablesUserEmailChange(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})

	rec := doUpdateSettings(t, h, map[string]any{"user_email_change_enabled": true}, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "true", repo.values[identity.SettingKeyUserEmailChangeEnabled])
}

// TestUpdateSettingsOmittedDefaultUserAPIKeyLimitKeepsStoredValue checks that omission retains the stored default key limit.
func TestUpdateSettingsOmittedDefaultUserAPIKeyLimitKeepsStoredValue(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		identity.SettingKeyDefaultUserAPIKeyLimit: "33",
	})

	rec := doUpdateSettings(t, h, map[string]any{"registration_enabled": true}, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "33", repo.values[identity.SettingKeyDefaultUserAPIKeyLimit])
}

// TestUpdateSettingsExplicitZeroDefaultUserAPIKeyLimit checks that zero sets an unlimited default key count.
func TestUpdateSettingsExplicitZeroDefaultUserAPIKeyLimit(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		identity.SettingKeyDefaultUserAPIKeyLimit: "33",
	})

	rec := doUpdateSettings(t, h, map[string]any{"default_user_api_key_limit": 0}, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "0", repo.values[identity.SettingKeyDefaultUserAPIKeyLimit])
}

// TestUpdateSettingsRejectsNegativeDefaultUserAPIKeyLimit checks rejection before persistence.
func TestUpdateSettingsRejectsNegativeDefaultUserAPIKeyLimit(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		identity.SettingKeyDefaultUserAPIKeyLimit: "33",
	})

	rec := doUpdateSettings(t, h, map[string]any{"default_user_api_key_limit": -1}, nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "INVALID_API_KEY_LIMIT")
	require.Equal(t, "33", repo.values[identity.SettingKeyDefaultUserAPIKeyLimit])
}

// TestUpdateSettingsRejectsDefaultUserAPIKeyLimitAboveDatabaseRange checks rejection of limits beyond PostgreSQL INTEGER range.
func TestUpdateSettingsRejectsDefaultUserAPIKeyLimitAboveDatabaseRange(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		identity.SettingKeyDefaultUserAPIKeyLimit: "33",
	})

	rec := doUpdateSettings(t, h, map[string]any{"default_user_api_key_limit": identity.MaxUserAPIKeyLimit + 1}, nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "INVALID_API_KEY_LIMIT")
	require.Equal(t, "33", repo.values[identity.SettingKeyDefaultUserAPIKeyLimit])
}

func TestUpdateSettingsForwardedClientIPHeadersOmittedPreservesAndEmptyClears(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		runtimeconfig.SettingKeyForwardedClientIPHeaders: `["X-Cdn-Ip","True-Client-Ip"]`,
	})

	preserved := doUpdateSettings(t, h, map[string]any{"registration_enabled": true}, nil)
	require.Equal(t, http.StatusOK, preserved.Code)
	require.JSONEq(t, `["X-Cdn-Ip","True-Client-Ip"]`, repo.values[runtimeconfig.SettingKeyForwardedClientIPHeaders])
	require.Contains(t, preserved.Body.String(), `"forwarded_client_ip_headers":["X-Cdn-Ip","True-Client-Ip"]`)

	cleared := doUpdateSettings(t, h, map[string]any{"forwarded_client_ip_headers": []string{}}, nil)
	require.Equal(t, http.StatusOK, cleared.Code)
	require.JSONEq(t, `[]`, repo.values[runtimeconfig.SettingKeyForwardedClientIPHeaders])
	require.Contains(t, cleared.Body.String(), `"forwarded_client_ip_headers":[]`)
}

func TestUpdateSettingsMalformedForwardedClientIPHeadersRemainFailClosedWhenOmitted(t *testing.T) {
	cfg := &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}}
	repo := &settingHandlerRepoStub{values: map[string]string{
		runtimeconfig.SettingKeyAPIKeyACLTrustForwardedIP: "true",
		runtimeconfig.SettingKeyForwardedClientIPHeaders:  `{"not":"an array"}`,
	}}
	options, store := newCompositeSettingsHTTPFixture(repo, cfg)
	require.ErrorContains(t, provideForwardedSettings(store, cfg).LoadForwardedClientIPSettings(context.Background()), "load forwarded client ip headers")
	require.False(t, cfg.ForwardedClientIPSettings().TrustForwardedIP)
	h := settingshttp.NewHandler(options)

	rec := doUpdateSettings(t, h, map[string]any{"registration_enabled": true}, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "false", repo.values[runtimeconfig.SettingKeyAPIKeyACLTrustForwardedIP])
	require.JSONEq(t, `[]`, repo.values[runtimeconfig.SettingKeyForwardedClientIPHeaders])
	runtimeSettings := cfg.ForwardedClientIPSettings()
	require.False(t, runtimeSettings.TrustForwardedIP)
	require.Empty(t, runtimeSettings.Headers)
	require.Contains(t, rec.Body.String(), `"api_key_acl_trust_forwarded_ip":false`)
	require.Contains(t, rec.Body.String(), `"forwarded_client_ip_headers":[]`)
}

func TestUpdateSettingsRejectsInvalidForwardedClientIPHeader(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		runtimeconfig.SettingKeyForwardedClientIPHeaders: `["X-Existing-IP"]`,
	})

	rec := doUpdateSettings(t, h, map[string]any{
		"forwarded_client_ip_headers": []string{"X Invalid"},
	}, nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.JSONEq(t, `["X-Existing-IP"]`, repo.values[runtimeconfig.SettingKeyForwardedClientIPHeaders])
}

// TestUpdateSettingsRejectsDeprecatedAdvancedSchedulerFields checks dedicated errors for retired experimental scheduler fields.
func TestUpdateSettingsRejectsDeprecatedAdvancedSchedulerFields(t *testing.T) {
	for _, field := range []string{
		"advanced_scheduler_enabled",
		"openai_advanced_scheduler_enabled",
		"openai_advanced_scheduler_weight_priority",
	} {
		t.Run(field, func(t *testing.T) {
			h, _ := newStepUpSwitchTestHandler(t, map[string]string{})

			rec := doUpdateSettings(t, h, map[string]any{field: true}, nil)

			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Contains(t, rec.Body.String(), "DEPRECATED_ADVANCED_SCHEDULER_SETTING")
			require.Contains(t, rec.Body.String(), field)
		})
	}
}
