package cloudflare

import (
	"errors"
	"fmt"
	"reflect"
)

// Production composition root of the Cloudflare Bridge provider. This file
// freezes the fail-closed construction path a real (non-test) application
// must use: the durable file-backed state store, the durable effect
// authority sink and the Core authority resolver are all mandatory and
// mechanically verified at construction, and the Core authority context is
// resolved and cross-checked before any remote side effect — never an
// in-memory fallback.

// ErrProductionConfigInvalid rejects a production composition that is
// missing or carries a non-durable/non-Core-backed piece.
var ErrProductionConfigInvalid = errors.New("cloudflare production provider: invalid configuration")

// ProductionProviderConfig carries the construction inputs of the
// production composition root. StateStore must be a durable file-backed
// store (NewFileStateStore); the ephemeral in-memory store is refused.
// AuthoritySink must be a durable file-backed effect authority sink and
// AuthorityResolver must be the Core-backed authority context resolver.
type ProductionProviderConfig struct {
	ProviderConfig

	AuthoritySink     EffectAuthoritySink
	AuthorityResolver CoreAuthorityResolver
}

// NewProductionProvider constructs the production Bridge provider and fails
// closed unless every durable piece is present and mechanically verifiable:
// a file-backed state store (never the ephemeral in-memory store), a
// non-nil durable file-backed effect authority sink (never an in-memory or
// typed-nil sink), and a non-nil Core-backed authority resolver whose
// context resolves and matches the Core runtime issuer. Resolution happens
// here, before any Bridge call, so a resolver failure can never surface
// after a remote Provision side effect. It delegates the Bridge/client
// validation to NewProvider and then binds the resolved effect reconcile
// seams.
func NewProductionProvider(config ProductionProviderConfig) (*Provider, error) {
	if config.StateStore == nil {
		return nil, fmt.Errorf("%w: the production provider requires a file-backed state store", ErrProductionConfigInvalid)
	}
	if !config.StateStore.isFileBacked() {
		return nil, fmt.Errorf("%w: the production provider refuses an in-memory state store; construct the store with NewFileStateStore", ErrProductionConfigInvalid)
	}
	sink, ok := config.AuthoritySink.(*FileEffectAuthoritySink)
	if !ok || sink == nil {
		return nil, fmt.Errorf("%w: the production provider requires a non-nil durable file-backed effect authority sink", ErrProductionConfigInvalid)
	}
	coreResolver, ok := config.AuthorityResolver.(CoreBackedAuthorityResolver)
	if !ok || isNilInterface(coreResolver) {
		return nil, fmt.Errorf("%w: the production provider requires a non-nil Core-backed authority resolver", ErrProductionConfigInvalid)
	}
	ctx, err := coreResolver.ResolveAuthorityContext()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAuthorityContextUnresolved, err)
	}
	if err := ctx.Validate(); err != nil {
		return nil, fmt.Errorf("%w: the resolved Core authority context is invalid: %v", ErrProductionConfigInvalid, err)
	}
	if !ctx.Namespace.Equal(coreResolver.CoreIssuer()) {
		return nil, fmt.Errorf("%w: the resolved Core namespace does not match the Core authority runtime issuer", ErrProductionConfigInvalid)
	}
	provider, err := NewProvider(config.ProviderConfig)
	if err != nil {
		return nil, err
	}
	provider.effectSink = sink
	provider.authorityContext = ctx
	return provider, nil
}

// isNilInterface reports whether value is a nil interface or holds a typed
// nil (a nil pointer, interface, map, slice, func or channel). A production
// seam must reject a typed nil fail closed rather than dereference it.
func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return reflected.IsNil()
	default:
		return false
	}
}
