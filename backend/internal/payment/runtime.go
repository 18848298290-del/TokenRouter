package payment

type Runtime struct {
	*Checkout
	*OrderQueries
	*RefundWorkflow
	*OrderLifecycle
	*ProviderBindings
}

// ResumeService returns the lifecycle's signing instance for HTTP handlers.
func (r *Runtime) ResumeService() *PaymentResumeService { return r.OrderLifecycle.resume }
