// Package autocert is the ACME provider from golang.org/x/crypto/acme/autocert
// This provider does not take any config.
package autocert

import (
	"crypto/tls"
	"net"
	"os"

	"github.com/volts-dev/volts/internal/acme"
	"github.com/volts-dev/volts/logger"
	"golang.org/x/crypto/acme/autocert"
)

var log = logger.New("acme")

// autoCertACME is the ACME provider from golang.org/x/crypto/acme/autocert.
type autocertProvider struct {
	logger logger.ILogger
}

// Listen implements acme.Provider.
func (a *autocertProvider) Listen(hosts ...string) (net.Listener, error) {
	return autocert.NewListener(hosts...), nil
}

// TLSConfig returns a new tls config.
func (a *autocertProvider) TLSConfig(hosts ...string) (*tls.Config, error) {
	// create a new manager
	m := &autocert.Manager{
		Prompt: autocert.AcceptTOS,
	}
	if len(hosts) > 0 {
		m.HostPolicy = autocert.HostWhitelist(hosts...)
	}
	dir := cacheDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		log.Warnf("autocert not using a cache: %v", err)
	} else {
		m.Cache = autocert.DirCache(dir)
	}
	return m.TLSConfig(), nil
}

// New returns an autocert acme.Provider.
func NewProvider() acme.Provider {
	return &autocertProvider{}
}
