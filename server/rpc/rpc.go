package rpc

import (
  "micro/go-micro/server"
)

func NewServer(opts ...server.Option) server.Server {
	return server.NewServer(opts...)
}
