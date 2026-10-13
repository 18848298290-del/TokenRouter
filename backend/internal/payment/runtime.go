package payment

// Payment HTTP handlers call the use cases assembled by the application
// through one runtime value. Embedded service pointers expose checkout, order
// reads, refunds, recovery, and provider resolution, while resume lookup
// returns the lifecycle's shared signer.

// Runtime holds the payment use cases assembled by the application.
type Runtime struct {
	// Checkout, OrderQueries, RefundWorkflow, OrderLifecycle, and
	// ProviderBindings refer to application-assembled instances. Their embedded
	// methods share the order stores and provider bindings selected by that
	// assembly.
	*Checkout         // checkout creation service
	*OrderQueries     // order reading service
	*RefundWorkflow   // refund orchestration service
	*OrderLifecycle   // payment recovery service
	*ProviderBindings // provider resolution service
}

// ResumeService returns the signing instance held by OrderLifecycle for HTTP
// recovery handlers. The receiver and its lifecycle need to be initialized;
// the returned service pointer is shared with checkout and lifecycle callers.
func (r *Runtime) ResumeService() *PaymentResumeService { return r.OrderLifecycle.resume }
