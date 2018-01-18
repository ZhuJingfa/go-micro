package micro

import (
	"time"

  "micro/go-micro/server"
	"golang.org/x/net/context"
)

type function struct {
	cancel context.CancelFunc
	Service
}

func fnHandlerWrapper(f Function) server.HandlerWrapper {
	return func(h server.HandlerFunc) server.HandlerFunc {
		return func(ctx context.Context, req server.Request, rsp interface{}) error {
			defer f.Done()
			return h(ctx, req, rsp)
		}
	}
}

func newFunction(opts ...Option) Function {
	ctx, cancel := context.WithCancel(context.Background())

	// force ttl/interval
	fopts := []Option{
		RegisterTTL(time.Minute),
		RegisterInterval(time.Second * 30),
	}

	// prepend to opts
	fopts = append(fopts, opts...)

	// make context the last thing
	fopts = append(fopts, Context(ctx))

	service := newService(fopts...)

	fn := &function{
		cancel:  cancel,
		Service: service,
	}

	service.Server().Init(
		// ensure the service waits for requests to finish
		server.Wait(true),
		// wrap handlers and subscribers to finish execution
		server.WrapHandler(fnHandlerWrapper(fn)),
	)

	return fn
}

func (f *function) Done() error {
	f.cancel()
	return nil
}

func (f *function) Handle(v interface{}) error {
	return f.Service.Server().Handle(
		f.Service.Server().NewHandler(v),
	)
}