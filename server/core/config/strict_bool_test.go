package config

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gogf/gf/v2/container/gvar"
)

// fakeGetter implements the configGetter interface for ValidateStrictBoolField tests.
type fakeGetter struct {
	data map[string]*gvar.Var
	err  error
}

func (f *fakeGetter) Get(_ context.Context, key string) (*gvar.Var, error) {
	if f.err != nil {
		return nil, f.err
	}
	if v, ok := f.data[key]; ok {
		return v, nil
	}
	return gvar.New(nil), nil
}

// ---------------------------------------------------------------------------
// R3: ValidateStrictBoolField tests
// ---------------------------------------------------------------------------

// TestStrictBool_MissingField_NoError verifies that a missing field returns no error
// (defaults to false).
func TestStrictBool_MissingField_NoError(t *testing.T) {
	ctx := context.Background()
	getter := &fakeGetter{data: map[string]*gvar.Var{}}

	err := ValidateStrictBoolField(ctx, getter, "provider.supports_dimensions_request")
	if err != nil {
		t.Fatalf("expected no error for missing field, got: %v", err)
	}
}

// TestStrictBool_True_NoError verifies that a real bool true passes validation.
func TestStrictBool_True_NoError(t *testing.T) {
	ctx := context.Background()
	getter := &fakeGetter{data: map[string]*gvar.Var{
		"field": gvar.New(true),
	}}

	err := ValidateStrictBoolField(ctx, getter, "field")
	if err != nil {
		t.Fatalf("expected no error for bool true, got: %v", err)
	}
}

// TestStrictBool_False_NoError verifies that a real bool false passes validation.
func TestStrictBool_False_NoError(t *testing.T) {
	ctx := context.Background()
	getter := &fakeGetter{data: map[string]*gvar.Var{
		"field": gvar.New(false),
	}}

	err := ValidateStrictBoolField(ctx, getter, "field")
	if err != nil {
		t.Fatalf("expected no error for bool false, got: %v", err)
	}
}

// TestStrictBool_StringTrue_Rejected verifies that string "true" is rejected.
func TestStrictBool_StringTrue_Rejected(t *testing.T) {
	ctx := context.Background()
	getter := &fakeGetter{data: map[string]*gvar.Var{
		"field": gvar.New("true"),
	}}

	err := ValidateStrictBoolField(ctx, getter, "field")
	if err == nil {
		t.Fatal("expected error for string 'true'")
	}
	if got := err.Error(); !strings.Contains(got, "must be a boolean") {
		t.Errorf("expected 'must be a boolean' error, got: %v", err)
	}
}

// TestStrictBool_Number_Rejected verifies that number 1 is rejected.
func TestStrictBool_Number_Rejected(t *testing.T) {
	ctx := context.Background()
	getter := &fakeGetter{data: map[string]*gvar.Var{
		"field": gvar.New(1),
	}}

	err := ValidateStrictBoolField(ctx, getter, "field")
	if err == nil {
		t.Fatal("expected error for number 1")
	}
	if got := err.Error(); !strings.Contains(got, "must be a boolean") {
		t.Errorf("expected 'must be a boolean' error, got: %v", err)
	}
}

// TestStrictBool_Nonsense_Rejected verifies that string "nonsense" is rejected.
func TestStrictBool_Nonsense_Rejected(t *testing.T) {
	ctx := context.Background()
	getter := &fakeGetter{data: map[string]*gvar.Var{
		"field": gvar.New("nonsense"),
	}}

	err := ValidateStrictBoolField(ctx, getter, "field")
	if err == nil {
		t.Fatal("expected error for string 'nonsense'")
	}
	if got := err.Error(); !strings.Contains(got, "must be a boolean") {
		t.Errorf("expected 'must be a boolean' error, got: %v", err)
	}
}

// TestStrictBool_ExplicitNull_Rejected verifies P2-3: an explicit YAML null is
// distinguishable from a missing key and must be rejected, not treated as a
// default false.
func TestStrictBool_ExplicitNull_Rejected(t *testing.T) {
	ctx := context.Background()
	getter := &fakeGetter{data: map[string]*gvar.Var{
		"provider": gvar.New(map[string]interface{}{"supports_dimensions_request": nil}),
		// Full-path Get for an explicit null yields a nil value, matching GoFrame.
	}}

	err := ValidateStrictBoolField(ctx, getter, "provider.supports_dimensions_request")
	if err == nil {
		t.Fatal("expected error for explicit null")
	}
	if got := err.Error(); !strings.Contains(got, "must be a boolean") {
		t.Errorf("expected 'must be a boolean' error, got: %v", err)
	}
}

// TestStrictBool_MissingKeyInPresentParent_NoError verifies that a present parent
// mapping without the key is still treated as missing.
func TestStrictBool_MissingKeyInPresentParent_NoError(t *testing.T) {
	ctx := context.Background()
	getter := &fakeGetter{data: map[string]*gvar.Var{
		"provider": gvar.New(map[string]interface{}{"name": "openai"}),
	}}

	err := ValidateStrictBoolField(ctx, getter, "provider.supports_dimensions_request")
	if err != nil {
		t.Fatalf("expected no error for missing key, got: %v", err)
	}
}

// TestStrictBool_ReadError_Propagated verifies that getter errors propagate.
func TestStrictBool_ReadError_Propagated(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("config read failed")
	getter := &fakeGetter{err: sentinel}

	err := ValidateStrictBoolField(ctx, getter, "field")
	if err == nil {
		t.Fatal("expected error from getter")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got: %v", err)
	}
}
