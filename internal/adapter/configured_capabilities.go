package adapter

import (
	"context"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
)

type configuredRuntimeCapabilityProvider interface {
	ConfiguredRuntimeCapabilities(context.Context, config.Runtime) ([]string, error)
}

// ConfiguredCapabilities resolves capabilities that depend on the pinned
// runtime installation while preserving the Provider's static tool and
// environment contract.
func ConfiguredCapabilities(ctx context.Context, p Provider, r config.Runtime) (CapabilitySet, error) {
	caps := p.Capabilities()
	if configured, ok := p.(configuredRuntimeCapabilityProvider); ok {
		runtime, err := configured.ConfiguredRuntimeCapabilities(ctx, r)
		if err != nil {
			return CapabilitySet{}, err
		}
		caps.Runtime = append([]string(nil), runtime...)
	}
	if err := validateCapabilitySet(caps); err != nil {
		return CapabilitySet{}, err
	}
	return caps, nil
}
