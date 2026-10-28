package vector

import (
	"context"
	"errors"
	"fmt"
	"regexp"
)

var (
	ErrPhysicalTargetRequired   = errors.New("vector: a single concrete physical target is required")
	ErrResourceBindingNotReady  = errors.New("vector: physical resource binding is not ready")
	ErrManagedLifecycleRequired = errors.New("vector: managed resource lifecycle must be explicitly confirmed")
)

var physicalName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,254}$`)

func CheckManagedLifecycle(cfg *Config) error {
	if cfg == nil {
		return ErrManagedLifecycleRequired
	}
	if err := ValidatePhysicalName(cfg.IndexName); err != nil {
		return err
	}
	if cfg.Type == "qdrant" {
		return fmt.Errorf("%w: Qdrant incarnation/namespace is unresolved", ErrResourceBindingNotReady)
	}
	if !cfg.ManagedResourceLifecycle {
		return ErrManagedLifecycleRequired
	}
	return nil
}

// ValidatePhysicalName excludes aliases' resolution expressions, wildcards,
// date math, remote cluster selectors and multiple targets. Backend responses
// must additionally prove that the name is concrete, rather than an alias.
func ValidatePhysicalName(name string) error {
	if !physicalName.MatchString(name) {
		return fmt.Errorf("%w: %q", ErrPhysicalTargetRequired, name)
	}
	return nil
}

// BindPhysicalTarget returns a guard capturing an immutable name and identity.
// It does not provide a backend CAS: deployment must prohibit name reuse,
// resource replacement, backend switching and automatic index creation.
func BindPhysicalTarget(ctx context.Context, vs VectorStore, name string, expected *Fingerprint) (func(context.Context) error, error) {
	if vs == nil || expected == nil {
		return nil, ErrResourceBindingNotReady
	}
	if err := ValidatePhysicalName(name); err != nil {
		return nil, err
	}
	if _, qdrant := vs.(*QdrantVectorStore); qdrant {
		return nil, fmt.Errorf("%w: Qdrant incarnation/namespace is unresolved", ErrResourceBindingNotReady)
	}
	stored, id, err := vs.ReadFingerprint(ctx, name)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, fmt.Errorf("%w: empty resource ID", ErrFingerprintCorrupted)
	}
	if err := VerifyFingerprint(stored, expected); err != nil {
		return nil, err
	}
	// Capture a value, not a caller-owned mutable fingerprint pointer.
	want := *expected
	guard := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		fp, currentID, err := vs.ReadFingerprint(ctx, name)
		if err != nil {
			return err
		}
		if currentID != id {
			return fmt.Errorf("%w: target %q", ErrResourceReplaced, name)
		}
		return VerifyFingerprint(fp, &want)
	}
	return guard, nil
}
