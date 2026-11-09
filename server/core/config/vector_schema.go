package config

import (
	"github.com/elastic/go-elasticsearch/v8/typedapi/types"
	"github.com/qdrant/go-client/qdrant"
	coretypes "github.com/wangle201210/go-rag/server/core/types"
)

// ESMapping requires a resolved dimension; schema builders do not choose defaults.
func ESMapping(dimensions int) (*types.TypeMapping, error) {
	if dimensions <= 0 {
		return nil, ErrEmbeddingDimensions
	}
	return &types.TypeMapping{
		Properties: map[string]types.Property{
			coretypes.FieldContent:  types.NewTextProperty(),
			coretypes.FieldExtra:    types.NewTextProperty(),
			coretypes.KnowledgeName: types.NewKeywordProperty(),
			coretypes.FieldContentVector: &types.DenseVectorProperty{
				Dims:       Of(dimensions),
				Index:      Of(true),
				Similarity: Of("cosine"),
			},
			coretypes.FieldQAContentVector: &types.DenseVectorProperty{
				Dims:       Of(dimensions),
				Index:      Of(true),
				Similarity: Of("cosine"),
			},
		},
	}, nil
}
func QdrantCollection(name string, dimensions int) (*qdrant.CreateCollection, error) {
	if dimensions <= 0 {
		return nil, ErrEmbeddingDimensions
	}
	vectorsMap := map[string]*qdrant.VectorParams{
		coretypes.FieldContentVector: {
			Size:     uint64(dimensions),
			Distance: qdrant.Distance_Cosine,
		},
		coretypes.FieldQAContentVector: {
			Size:     uint64(dimensions),
			Distance: qdrant.Distance_Cosine,
		},
	}
	return &qdrant.CreateCollection{CollectionName: name, VectorsConfig: qdrant.NewVectorsConfigMap(vectorsMap)}, nil
}
