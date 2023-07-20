package memory

import (
	"context"

	"github.com/volts-dev/volts/registry"
)

// Services is an option that preloads service data.
func Services(s map[string][]*registry.Service) registry.Option {
	return func(o *registry.Config) {
		if o.Context == nil {
			o.Context = context.Background()
		}
		o.Context = context.WithValue(o.Context, servicesKey{}, s)
	}
}
