package app

import (
	"context"

	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type artifactContracts struct {
	registry   reconcile.ContractsVersionResolver
	configured string
}

// ResolveContractsVersion retains the uniform-mode constraint even when
// collectors are composed independently of startup registry validation.
func (selection artifactContracts) ResolveContractsVersion(
	ctx context.Context,
	principal string,
	current run.Run,
) (string, error) {
	version, err := selection.registry.ResolveContractsVersion(ctx, principal, current)
	if err != nil {
		return "", err
	}
	if selection.configured != RegistryContractsVersion && version != selection.configured {
		return "", run.ErrForbidden
	}

	return version, nil
}
