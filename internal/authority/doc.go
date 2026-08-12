// Package authority freezes the v1alpha1 dual key spaces and side-effect
// authority records of the Marshal Control Plane (ADR 0018 §10 and ADR 0019
// §§123–125). AuthorityNamespaceId owns every Control Plane authority object
// and is writable only by Core. SecurityDomainId identifies provider actors
// and appears in authority records only as provenance. The two key spaces are
// distinct Go types that must never impersonate each other; every violation
// fails closed. SideEffectIntent, SideEffectReceipt and ReconcileRecord are
// Core-internal authority records, not provider wire schemas.
package authority
