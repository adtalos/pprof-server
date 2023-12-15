package registry

import (
	"context"
	"time"
)

type Host struct {
	Name    string
	Address string
	Age     time.Duration
}

type Registry interface {
	ListNamespaces(ctx context.Context) ([]string, error)
	ListHosts(ctx context.Context, namespace string) ([]Host, error)
}
