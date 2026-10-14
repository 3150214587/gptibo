package providers

import (
	"context"
	"gptibo/platform/internal/commerce"
)

// A driver owns external execution only. It never writes wallet or ledger balances.
type Driver interface {
	Execute(context.Context, commerce.Order, commerce.Snapshot, commerce.Credential, *Job, func() error) (Result, error)
}
type DriverFunc func(context.Context, commerce.Order, commerce.Snapshot, commerce.Credential, *Job, func() error) (Result, error)

func (f DriverFunc) Execute(ctx context.Context, o commerce.Order, s commerce.Snapshot, c commerce.Credential, j *Job, checkpoint func() error) (Result, error) {
	return f(ctx, o, s, c, j, checkpoint)
}
func (e *Engine) builtinDrivers() map[string]Driver {
	return map[string]Driver{
		"gpt": DriverFunc(e.gpt),
		"x": DriverFunc(func(ctx context.Context, o commerce.Order, s commerce.Snapshot, _ commerce.Credential, j *Job, checkpoint func() error) (Result, error) {
			return e.x(ctx, o, s, j, checkpoint)
		}),
	}
}
