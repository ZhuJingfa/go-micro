package consul

import (
	"micro/go-micro/registry"
)

func NewRegistry(opts ...registry.Option) registry.Registry {
	return registry.NewRegistry(opts...)
}
