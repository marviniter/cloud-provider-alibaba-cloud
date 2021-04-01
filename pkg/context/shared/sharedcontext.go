package shared

import (
	"k8s.io/cloud-provider-alibaba-cloud/pkg/context/base"
	"k8s.io/cloud-provider-alibaba-cloud/pkg/provider"
)

func NewSharedContext(
	prvd provider.Provider,
) *SharedContext {
	ctxs := SharedContext{}
	ctxs.SetKV(Provider, prvd)
	return &ctxs
}

const (
	Provider = "Provider"
)

type SharedContext struct{ base.Context }

// Node
func (c *SharedContext) Provider() provider.Provider {
	node, ok := c.Value(Provider)
	if !ok {
		return nil
	}
	return node.(provider.Provider)
}