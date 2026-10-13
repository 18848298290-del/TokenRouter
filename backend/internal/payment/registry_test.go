package payment

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// Registry method bindings can be replaced while readers retain the previously
// published provider pointers. These fixtures check registration, key lookup,
// method enumeration and overwrite using per-test registries, so parallel
// tests do not share mutable tables. Missing methods/keys retain their
// distinct empty/error results.
//
// Concurrent access joins separate writer and reader goroutines before
// counting registered methods; reader lookup errors are deliberately ignored
// for absent fixture methods. Complete replacement is tested with a one-use
// provider whose SupportedTypes signals entry and waits for release. The
// reader checks old pointers while candidate construction is blocked, then
// joins replacement before checking both new pointers. Provider-object
// behavior outside those fixture operations is not covered by these
// assertions.

// mockProvider supplies fixture identity/method bindings and silent payment
// operations for registry tests.
type mockProvider struct {
	name string // fixture display provider name
	key  string // fixture registry provider key
	// supportedTypes is returned as the supplied shared slice. Registry fixtures
	// treat these method labels as immutable while parallel reads or candidate
	// construction use the provider.
	supportedTypes []PaymentType
}

// blockedProvider pauses one SupportedTypes call while registry replacement
// constructs its candidate table.
type blockedProvider struct {
	mockProvider // embedded fixture provider operations
	// entered is closed when candidate method construction starts, then
	// SupportedTypes waits on release. The test reads old bindings before closing
	// release and joins replacement before new-binding assertions.
	//
	// entered: candidate construction entry signal. release: candidate
	// construction release gate.
	entered, release chan struct{}
}

// TestRegistryRegisterAndGetProvider registers one provider supporting Alipay
// and WeChat. Both method lookups must report its provider key, protecting
// multi-method registration of the same fixture provider.
func TestRegistryRegisterAndGetProvider(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	p := &mockProvider{
		name:           "TestPay",
		key:            "testpay",
		supportedTypes: []PaymentType{TypeAlipay, TypeWxpay},
	}
	r.Register(p)

	got, err := r.GetProvider(TypeAlipay)
	if err != nil {
		t.Fatalf("GetProvider(alipay) error: %v", err)
	}
	if got.ProviderKey() != "testpay" {
		t.Fatalf("GetProvider(alipay) key = %q, want %q", got.ProviderKey(), "testpay")
	}

	got2, err := r.GetProvider(TypeWxpay)
	if err != nil {
		t.Fatalf("GetProvider(wxpay) error: %v", err)
	}
	if got2.ProviderKey() != "testpay" {
		t.Fatalf("GetProvider(wxpay) key = %q, want %q", got2.ProviderKey(), "testpay")
	}
}

// TestRegistryGetProviderNotFound looks up a method in a fresh empty registry.
// It requires an error, protecting the missing-method result consumed by
// payment selection and compatibility callers.
func TestRegistryGetProviderNotFound(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	_, err := r.GetProvider("nonexistent")
	if err == nil {
		t.Fatal("GetProvider for unregistered type should return error")
	}
}

// TestRegistryGetProviderByKey registers one EasyPay fixture and reads it by
// its key. The returned display name must match that provider, protecting key
// lookup through registered method bindings.
func TestRegistryGetProviderByKey(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	p := &mockProvider{
		name:           "EasyPay",
		key:            "easypay",
		supportedTypes: []PaymentType{TypeAlipay},
	}
	r.Register(p)

	got, err := r.GetProviderByKey("easypay")
	if err != nil {
		t.Fatalf("GetProviderByKey error: %v", err)
	}
	if got.Name() != "EasyPay" {
		t.Fatalf("GetProviderByKey name = %q, want %q", got.Name(), "EasyPay")
	}
}

// TestRegistryGetProviderByKeyNotFound looks up an absent provider key in an
// empty registry. It requires an error, protecting the key-based
// missing-provider result independently of method lookup.
func TestRegistryGetProviderByKeyNotFound(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	_, err := r.GetProviderByKey("nonexistent")
	if err == nil {
		t.Fatal("GetProviderByKey for unknown key should return error")
	}
}

// TestRegistryGetProviderKeyUnknownType asks an empty registry for an unbound
// method's provider key. It requires empty text, protecting the compatibility
// caller's no-key observation.
func TestRegistryGetProviderKeyUnknownType(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	key := r.GetProviderKey("unknown_type")
	if key != "" {
		t.Fatalf("GetProviderKey for unknown type should return empty, got %q", key)
	}
}

// TestRegistryGetProviderKeyKnownType registers the Stripe method on one
// fixture provider. Method-key lookup must return stripe, protecting the key
// observation used by later provider resolution.
func TestRegistryGetProviderKeyKnownType(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	p := &mockProvider{
		name:           "Stripe",
		key:            "stripe",
		supportedTypes: []PaymentType{TypeStripe},
	}
	r.Register(p)

	key := r.GetProviderKey(TypeStripe)
	if key != "stripe" {
		t.Fatalf("GetProviderKey(stripe) = %q, want %q", key, "stripe")
	}
}

// TestRegistrySupportedTypes registers EasyPay and Stripe with three distinct
// methods. It checks count and set membership without asserting map
// enumeration order, protecting the registry's complete method set.
func TestRegistrySupportedTypes(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	p1 := &mockProvider{
		name:           "EasyPay",
		key:            "easypay",
		supportedTypes: []PaymentType{TypeAlipay, TypeWxpay},
	}
	p2 := &mockProvider{
		name:           "Stripe",
		key:            "stripe",
		supportedTypes: []PaymentType{TypeStripe},
	}
	r.Register(p1)
	r.Register(p2)

	types := r.SupportedTypes()
	if len(types) != 3 {
		t.Fatalf("SupportedTypes() len = %d, want 3", len(types))
	}

	typeSet := make(map[PaymentType]bool)
	for _, tp := range types {
		typeSet[tp] = true
	}
	for _, expected := range []PaymentType{TypeAlipay, TypeWxpay, TypeStripe} {
		if !typeSet[expected] {
			t.Fatalf("SupportedTypes() missing %q", expected)
		}
	}
}

// TestRegistrySupportedTypesEmpty enumerates a newly constructed registry
// before registration. It requires zero methods, protecting empty availability
// without requiring a particular nil-versus-empty slice representation.
func TestRegistrySupportedTypesEmpty(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	types := r.SupportedTypes()
	if len(types) != 0 {
		t.Fatalf("SupportedTypes() on empty registry should be empty, got %d", len(types))
	}
}

// TestRegistryOverwriteExisting registers two providers for the same method in
// sequence. The final lookup must return the later display name, protecting
// replacement of that method's binding.
func TestRegistryOverwriteExisting(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	p1 := &mockProvider{
		name:           "OldPay",
		key:            "old",
		supportedTypes: []PaymentType{TypeAlipay},
	}
	p2 := &mockProvider{
		name:           "NewPay",
		key:            "new",
		supportedTypes: []PaymentType{TypeAlipay},
	}
	r.Register(p1)
	r.Register(p2)

	got, err := r.GetProvider(TypeAlipay)
	if err != nil {
		t.Fatalf("GetProvider error: %v", err)
	}
	if got.Name() != "NewPay" {
		t.Fatalf("expected overwritten provider, got %q", got.Name())
	}
}

// TestRegistryConcurrentAccess starts distinct-method writers alongside
// enumeration and absent-method readers, then joins every goroutine. It
// requires the final method count to equal writer count; it does not assert
// shared provider internals or successful absent lookups.
func TestRegistryConcurrentAccess(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	// Use fifty writers and fifty readers for the fixture.
	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Start one writer per distinct method and mark each task done on exit. The
	// final join counts completed registrations independently of concurrent
	// absent-method reads.
	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			p := &mockProvider{
				name:           fmt.Sprintf("Provider-%d", idx),
				key:            fmt.Sprintf("key-%d", idx),
				supportedTypes: []PaymentType{fmt.Sprintf("type-%d", idx)},
			}
			r.Register(p)
		}(i)
	}

	// Start readers alongside writers and discard errors for their deliberately
	// absent method. Their done signals join with writers before the final
	// supported-method count is inspected.
	for range goroutines {
		go func() {
			defer wg.Done()
			_ = r.SupportedTypes()
			_, _ = r.GetProvider("some-type")
			_ = r.GetProviderKey("some-type")
		}()
	}

	// Join all fixture work before enumerating the final table. That makes the
	// distinct method count an observation after every writer completed, rather
	// than an in-progress snapshot.
	wg.Wait()

	types := r.SupportedTypes()
	if len(types) != goroutines {
		t.Fatalf("after concurrent registration, expected %d types, got %d", goroutines, len(types))
	}
}

// TestRegistryReplacePublishesOnlyCompleteMap blocks construction of the
// second replacement candidate after old bindings are registered. Reads must
// keep both old pointers before release, then expose the complete pair of new
// pointers after replacement joins.
func TestRegistryReplacePublishesOnlyCompleteMap(t *testing.T) {
	registry := NewRegistry()
	old := &mockProvider{name: "old", supportedTypes: []string{TypeAlipay, TypeWxpay}}
	registry.Register(old)
	next := &mockProvider{name: "new-a", supportedTypes: []string{TypeAlipay}}
	blocked := &blockedProvider{
		mockProvider: mockProvider{name: "new-b", supportedTypes: []string{TypeWxpay}},
		entered:      make(chan struct{}),
		release:      make(chan struct{}),
	}
	done := make(chan struct{})
	// Run replacement separately while the blocked candidate holds method
	// construction. Entry signals that readers can inspect the old table before
	// release allows complete publication and join.
	go func() { registry.Replace([]Provider{next, blocked}); close(done) }()
	<-blocked.entered
	a, ea := registry.GetProvider(TypeAlipay)
	b, eb := registry.GetProvider(TypeWxpay)
	// Release candidate construction after both old-table reads, then join
	// replacement before inspecting new pointers. The ordering distinguishes
	// unpublished candidate data from the committed table observation.
	close(blocked.release)
	<-done
	if ea != nil || eb != nil || a != old || b != old {
		t.Fatal("候选未就绪时不能发布空表或半张表")
	}
	a, ea = registry.GetProvider(TypeAlipay)
	b, eb = registry.GetProvider(TypeWxpay)
	if ea != nil || eb != nil || a != next || b != blocked {
		t.Fatal("发布后必须包含完整候选")
	}
}

// Name returns the fixture display name used in registry lookup assertions.
func (m *mockProvider) Name() string { return m.name }

// ProviderKey returns the fixture key used for registry account resolution.
func (m *mockProvider) ProviderKey() string { return m.key }

// SupportedTypes returns the shared fixture method slice without making a
// copy. Tests keep those labels unchanged while registration and replacement
// read them, including blocked and concurrent registry scenarios.
func (m *mockProvider) SupportedTypes() []PaymentType { return m.supportedTypes }

// CreatePayment returns nil response and nil error for this registry-only
// fixture. Tests register provider identities without initiating payment; a
// caller invoking this method receives no usable initiation details.
func (m *mockProvider) CreatePayment(_ context.Context, _ CreatePaymentRequest) (*CreatePaymentResponse, error) {
	return nil, nil
}

// QueryOrder returns nil observation and nil error for the registry-only
// fixture. It makes no provider request, and scenario callers that need
// payment state supply a different provider implementation.
func (m *mockProvider) QueryOrder(_ context.Context, _ string) (*QueryOrderResponse, error) {
	return nil, nil
}

// VerifyNotification returns a nil notification with nil error for the
// registry fixture. If used for verification, that result means accepted
// irrelevant input rather than an authenticated order observation.
func (m *mockProvider) VerifyNotification(_ context.Context, _ string, _ map[string]string) (*PaymentNotification, error) {
	return nil, nil
}

// Refund returns nil response and nil error for this registry-only provider
// fixture. It supplies interface coverage without a channel operation or
// usable refund outcome for workflow processing.
func (m *mockProvider) Refund(_ context.Context, _ RefundRequest) (*RefundResponse, error) {
	return nil, nil
}

// SupportedTypes signals construction entry, waits for the release gate and
// returns its embedded shared method slice. This fixture supports one call
// because closing entered again would panic.
//
// The test waits for entry before reading old registry pointers, then closes
// release to let replacement finish. Returning methods precedes whole-table
// publication, so the separate done signal joins that publication before
// new-binding assertions. Callers keep the method slice immutable and provide
// initialized channels for this coordination.
func (p *blockedProvider) SupportedTypes() []PaymentType {
	// Signal entry before waiting on release so the test knows replacement is
	// building candidates. The shared methods return only after the reader has
	// observed the old published table.
	close(p.entered)
	<-p.release
	return p.supportedTypes
}
