package payment

import (
	"testing"
)

// TestIsOfficialAlipayProviderInstance supplies nil, official, normalized
// official, and EasyPay instance identities. Recognition follows the trimmed
// case-insensitive provider key, while an EasyPay route remains a different
// implementation.
func TestIsOfficialAlipayProviderInstance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string            // case diagnostic name
		instance *ProviderInstance // supplied provider instance
		want     bool              // expected official identity result
	}{
		{name: "nil instance", instance: nil, want: false},
		{name: "official alipay", instance: &ProviderInstance{ProviderKey: TypeAlipay}, want: true},
		{name: "normalized official alipay", instance: &ProviderInstance{ProviderKey: " ALIPAY "}, want: true},
		{name: "easypay alipay route", instance: &ProviderInstance{ProviderKey: TypeEasyPay}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ConfigIsOfficialAlipayProviderInstance(tt.instance); got != tt.want {
				t.Fatalf("payment.ConfigIsOfficialAlipayProviderInstance() = %v, want %v", got, tt.want)
			}
		})
	}
}
