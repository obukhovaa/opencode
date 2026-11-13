package tools

import "context"

type flowOwnershipKey struct{}

// WithFlowOwnershipGuard installs a read-only admission check for model/tool
// calls. Durable writes still need transactional fencing; this check prevents a
// known stale worker from starting more external work between lease renewals.
func WithFlowOwnershipGuard(ctx context.Context, guard func(context.Context) error) context.Context {
	return context.WithValue(ctx, flowOwnershipKey{}, guard)
}
func CheckFlowOwnership(ctx context.Context) error {
	guard, _ := ctx.Value(flowOwnershipKey{}).(func(context.Context) error)
	if guard == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return guard(ctx)
}
func inheritFlowOwnership(dst, src context.Context) context.Context {
	if guard := src.Value(flowOwnershipKey{}); guard != nil {
		return context.WithValue(dst, flowOwnershipKey{}, guard)
	}
	return dst
}

func HasFlowOwnershipGuard(ctx context.Context) bool {
	guard, _ := ctx.Value(flowOwnershipKey{}).(func(context.Context) error)
	return guard != nil
}
