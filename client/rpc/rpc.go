package rpc

import (
	"micro/go-micro/client"
)

func NewClient(opts ...client.Option) client.Client {
	return client.NewClient(opts...)
}
