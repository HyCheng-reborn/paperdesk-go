package rag

import (
	"context"
	"fmt"
)

// Strict bool, default false. This attests deployment controls, not merely an
// intention to enable the feature. See docs/stage2c2-e3-design.md.
func readManagedLifecycle(ctx context.Context, getter configGetter) (bool, error) {
	v, err := getter.Get(ctx, "vector.managedResourceLifecycle")
	if err != nil {
		return false, fmt.Errorf("read managed lifecycle: %w", err)
	}
	if v == nil || v.IsNil() {
		return false, nil
	}
	b, ok := v.Interface().(bool)
	if !ok {
		return false, fmt.Errorf("vector.managedResourceLifecycle must be a boolean")
	}
	return b, nil
}
