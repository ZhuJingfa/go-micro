package http

import (
	"micro/go-micro/transport"
)

func NewTransport(opts ...transport.Option) transport.Transport {
	return transport.NewTransport(opts...)
}
