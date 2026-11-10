package server

import (
	"testing"
	"time"

	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/registry/memory"
	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/transport"
)

func TestServerStop_ReleasesOwnedDiscoveryButNotInjectedCache(t *testing.T) {
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
			r := router.New(router.WithConfig(router.NewConfig()))
			defer r.Close()
			tr := transport.NewHTTPTransport()
			srv := New(Name("cache-owner"), WithRegistry(source), WithRouter(r), WithTransport(tr), WithBroker(&fakeBroker{}), Address(":0"))
			if err := srv.Start(); err != nil {
				t.Fatal(err)
			}
			defer srv.Stop()
			cache := srv.Config().Registry.(registry.IRegistryCacher)
			if _, err := cache.GetService("sale"); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- srv.Stop() }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("server Stop hung")
			}
			_, err := cache.GetService("sale")
			if injected && err != nil {
				t.Fatalf("borrowed cache stopped: %v", err)
			}
			if !injected && err != registry.ErrWatcherStopped {
				t.Fatalf("owned cache survived server Stop: %v", err)
			}
		})
	}
}

// Options are reusable; ownership must not depend on which server applied one first.
func TestServerRegistryOption_ReuseCreatesIndependentOwnedWrappers(t *testing.T) {
	back := memory.New()
	option := WithRegistry(back)
	first, second := &Config{}, &Config{}
	option(first)
	option(second)
	if first.Registry == second.Registry || len(first.ownedCachers) != 1 || len(second.ownedCachers) != 1 {
		t.Fatal("reused option silently transferred owned cache to another server")
	}
	first.ownedCachers[0].Stop()
	second.ownedCachers[0].Stop()
}
