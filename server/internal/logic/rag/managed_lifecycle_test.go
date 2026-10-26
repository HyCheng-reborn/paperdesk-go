package rag

import (
	"context"
	"errors"
	"testing"

	"github.com/gogf/gf/v2/container/gvar"
	"github.com/wangle201210/go-rag/server/core/common"
	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/vector"
)

func TestManagedLifecycleConfigStrict(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     any
		want    bool
		invalid bool
	}{
		{"missing", nil, false, false}, {"false", false, false, false}, {"true", true, true, false},
		{"string", "true", false, true}, {"number", 1, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readManagedLifecycle(context.Background(), &fakeConfigGetter{data: map[string]*gvar.Var{"vector.managedResourceLifecycle": gvar.New(tc.raw)}})
			if (err != nil) != tc.invalid || got != tc.want {
				t.Fatalf("got %v %v", got, err)
			}
		})
	}
	sentinel := errors.New("config unavailable")
	_, err := readManagedLifecycle(context.Background(), &fakeConfigGetter{err: sentinel})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

func TestManagedFactoryDisabledProfileCannotCreateResource(t *testing.T) {
	cfg := validRagConfig()
	cfg.EmbeddingDimensions = 1024
	cfg.VectorCfg.ManagedResourceLifecycle = true
	cfg.Provider = &config.ProviderConfig{Name: "openai", EmbeddingModel: "new-profile", EmbeddingDimensions: cfg.EmbeddingDimensions}
	_, err := (&defaultRagFactory{}).Create(cfg, nil)
	if !errors.Is(err, common.ErrEmbeddingGuardNotReady) {
		t.Fatal(err)
	}
}

func TestManagedFactoryRejectsBeforeAnyResourceIO(t *testing.T) {
	for _, tc := range []struct {
		name    string
		managed bool
		backend string
		want    error
	}{
		{"unconfirmed", false, "es", vector.ErrManagedLifecycleRequired},
		{"qdrant-still-disabled", true, "qdrant", vector.ErrResourceBindingNotReady},
		{"expression", true, "es", vector.ErrPhysicalTargetRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validRagConfig()
			cfg.VectorCfg.Type = tc.backend
			cfg.VectorCfg.ManagedResourceLifecycle = tc.managed
			if tc.name == "expression" {
				cfg.VectorCfg.IndexName = "idx-*"
			}
			// nil store would panic if initialization crossed the policy gate.
			_, err := (&defaultRagFactory{}).Create(cfg, nil)
			if !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
			_, err = (&defaultVectorStoreFactory{}).Create(&cfg.VectorCfg)
			if !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
		})
	}
}
