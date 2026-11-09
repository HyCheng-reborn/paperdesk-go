package rag

import (
	"fmt"
	"os"

	"github.com/gogf/gf/v2/encoding/gtoml"
	"github.com/gogf/gf/v2/encoding/gyaml"
	"github.com/gogf/gf/v2/os/gcfg"
	"github.com/gogf/gf/v2/os/gres"
	"github.com/wangle201210/go-rag/server/core/config"
)

// verifyFileDimensionSource validates before YAML -> JSON normalization can erase
// float/exponent types. It uses the same public source precedence as AdapterFile:
// SetContent, resolved embedded resource, then disk. No private SDK state is read.
// Non-file adapters supply already typed data and keep the shared strict parser.
func verifyFileDimensionSource(adapter gcfg.Adapter, effective [2]int) error {
	a, ok := adapter.(*gcfg.AdapterFile)
	if !ok || effective == [2]int{} {
		return nil
	}
	name := a.GetFileName()
	content := a.GetContent(name)
	if content == "" {
		path, err := a.GetFilePath(name)
		if err != nil || path == "" {
			return fmt.Errorf("%w: cannot locate original dimension configuration", config.ErrEmbeddingDimensions)
		}
		if file := gres.Get(path); file != nil {
			content = string(file.Content())
		} else {
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("%w: cannot read original dimension configuration", config.ErrEmbeddingDimensions)
			}
			content = string(data)
		}
	}
	// gyaml.Decode uses the pinned YAML decoder without a JSON marshal round-trip,
	// preserving float64 even for 768.0/1e3. Aliases/merge keys use that decoder's
	// normal semantics. JSON is a YAML subset; TOML file/content input is retained.
	document, err := gyaml.Decode([]byte(content))
	if err != nil {
		decoded, tomlErr := gtoml.Decode([]byte(content))
		if tomlErr != nil {
			return fmt.Errorf("%w: cannot decode original dimension configuration", config.ErrEmbeddingDimensions)
		}
		var ok bool
		document, ok = decoded.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: dimension source must be a mapping", config.ErrEmbeddingDimensions)
		}
	}
	paths := [2][2]string{{"embedding", "dimensions"}, {"provider", "embedding_dimensions"}}
	for i, path := range paths {
		var scalar any
		if section := document[path[0]]; section != nil {
			fields, ok := section.(map[string]any)
			if !ok {
				return fmt.Errorf("%w: %s must be a mapping", config.ErrEmbeddingDimensions, path[0])
			}
			scalar = fields[path[1]]
		}
		value, err := config.ParseEmbeddingDimensions(scalar)
		if err != nil {
			return fmt.Errorf("%s.%s source: %w", path[0], path[1], err)
		}
		if value != effective[i] {
			return fmt.Errorf("%w: %s.%s source differs from effective configuration", config.ErrEmbeddingDimensionConflict, path[0], path[1])
		}
	}
	return nil
}
