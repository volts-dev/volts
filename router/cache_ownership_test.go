package router

import (
	"testing"

	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/registry/memory"
	"github.com/volts-dev/volts/selector"
)

func TestRouterClose_ReleasesOwnedDiscoveryButNotInjectedCache(t *testing.T) {
	for _, injected := range []bool{false, true} {
		t.Run(map[bool]string{false: "owned", true: "borrowed"}[injected], func(t *testing.T) {
			back := memory.New()
			if err := back.Register(&registry.Service{Name: "sale", Nodes: []*registry.Node{{Id: "sale-1"}}}); err != nil {
				t.Fatal(err)
			}
			var source registry.IRegistry = back
			if injected {
				source = registry.NewCacher(back)
				defer source.(registry.IRegistryCacher).Stop()
			}
			cfg := NewConfig()
			r := New(WithConfig(cfg), WithRegistry(source))
			defer r.Close()
			cache := r.Config().RegistryCacher
			if _, err := cache.GetService("sale"); err != nil {
				t.Fatal(err)
			}
			r.Close()
			r.Close()
			_, err := cache.GetService("sale")
			if injected && err != nil {
				t.Fatalf("borrowed cache stopped: %v", err)
			}
			if !injected && err != registry.ErrWatcherStopped {
				t.Fatalf("owned cache survived Close: %v", err)
			}
			if _, err := cfg.Selector.Select("sale"); err != registry.ErrWatcherStopped {
				t.Fatalf("owned selector survived Close: %v", err)
			}
		})
	}
}

func TestRouterClose_DoesNotCloseInjectedSelector(t *testing.T) {
	back := memory.New()
	_ = back.Register(&registry.Service{Name: "sale", Nodes: []*registry.Node{{Id: "sale-1"}}})
	sc := selector.NewConfig()
	sc.Registry = back
	sel := selector.New(selector.WithConfig(sc))
	defer sel.Close()
	cfg := NewConfig()
	cfg.Selector = sel
	r := New(WithConfig(cfg), WithRegistry(back))
	r.Close()
	if _, err := sel.Select("sale"); err != nil {
		t.Fatalf("borrowed selector stopped: %v", err)
	}
}
