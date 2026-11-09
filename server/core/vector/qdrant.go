package vector

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"
	"github.com/wangle201210/go-rag/server/core/config"
	coretypes "github.com/wangle201210/go-rag/server/core/types"
	gormmodel "github.com/wangle201210/go-rag/server/internal/model/gorm"
	"gorm.io/gorm"
)

// QdrantVectorStore Qdrant 向量存储实现
type QdrantVectorStore struct {
	dimensions int
	client     *qdrant.Client
	db         *gorm.DB // SQL database for fingerprint storage (Qdrant has no collection-level metadata)
}

// NewQdrantVectorStore 创建 Qdrant 向量存储实例
func NewQdrantVectorStore(cfg *QdrantConfig, dimensions ...int) (*QdrantVectorStore, error) {
	d, err := storeDimensions(dimensions)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, fmt.Errorf("nil vector configuration")
	}
	client, err := qdrant.NewClient(&qdrant.Config{
		Host:   cfg.Address,
		Port:   cfg.Port,
		APIKey: cfg.APIKey,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create qdrant client: %w", err)
	}

	return &QdrantVectorStore{
		client:     client,
		dimensions: d,
	}, nil
}

// SetDB injects the *gorm.DB for fingerprint storage.
// Must be called after NewQdrantVectorStore when using Qdrant (which has no native SQL metadata).
func (s *QdrantVectorStore) SetDB(db *gorm.DB) {
	s.db = db
}

// GetClient 获取 Qdrant 客户端（用于兼容现有代码）
func (s *QdrantVectorStore) GetClient() *qdrant.Client {
	return s.client
}

// CreateIndex 创建集合。
// fp != nil 时原子写入 fingerprint + 生成 UUID + schema 验证（C4）。
// fp == nil 时仅创建 schema（向后兼容）。
// R7: 不在失败时自动删除集合。
func (s *QdrantVectorStore) CreateIndex(ctx context.Context, indexName string, fp *Fingerprint) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	dims := s.dimensions
	if fp != nil && fp.Dimensions > 0 {
		dims = fp.Dimensions
	}

	// 创建集合，使用命名向量配置以支持多向量
	req, err := config.QdrantCollection(indexName, dims)
	if err != nil {
		return err
	}
	// D1: fp != nil 时 db 必须非 nil（身份存储就绪后才创建资源）
	if fp != nil && s.db == nil {
		return fmt.Errorf("Qdrant CreateIndex with fingerprint requires SQL database (db is nil)")
	}

	err = s.client.CreateCollection(ctx, req)
	if err != nil {
		// D1: 归一化 AlreadyExists 错误为 ErrIndexAlreadyExists
		if isQdrantAlreadyExists(err) {
			return ErrIndexAlreadyExists
		}
		return fmt.Errorf("failed to create collection: %w", err)
	}

	// fp != nil: 原子身份绑定流程
	if fp != nil {
		resourceID := uuid.New().String()

		// 插入 SQL 记录（Ready=false）
		if s.db != nil {
			fpJSON, marshalErr := fp.MarshalJSON()
			if marshalErr != nil {
				return fmt.Errorf("marshal fingerprint for SQL: %w", marshalErr)
			}
			record := gormmodel.IndexFingerprint{
				VectorStoreType:   "qdrant",
				IndexName:         indexName,
				ResourceID:        resourceID,
				FingerprintJSON:   string(fpJSON),
				FingerprintDigest: fp.Digest(),
				EmbeddingModelID:  fp.EmbeddingModelID,
				Dimensions:        fp.Dimensions,
				ProviderName:      fp.ProviderName,
				Similarity:        fp.Similarity,
				Ready:             false,
			}
			if result := s.db.WithContext(ctx).Create(&record); result.Error != nil {
				return fmt.Errorf("write fingerprint to SQL: %w", result.Error)
			}
		}

		// 创建 payload 索引以支持过滤
		_, fieldErr := s.client.CreateFieldIndex(ctx, &qdrant.CreateFieldIndexCollection{
			CollectionName: indexName,
			FieldName:      coretypes.KnowledgeName,
			FieldType:      qdrant.FieldType_FieldTypeKeyword.Enum(),
		})
		if fieldErr != nil {
			// R7: 不自动删除集合
			return fmt.Errorf("failed to create field index: %w", fieldErr)
		}

		// D3: 严格 schema 验证（回读 collection info 确认实际维度/distance）
		info, infoErr := s.client.GetCollectionInfo(ctx, indexName)
		if infoErr != nil {
			return fmt.Errorf("get collection info for schema verification: %w", infoErr)
		}
		fields, extractErr := extractQdrantFieldInfo(info)
		if extractErr != nil {
			return fmt.Errorf("extract schema info for verification: %w", extractErr)
		}
		if schemaErr := ValidateSchemaStrict(fp, fields, "qdrant"); schemaErr != nil {
			return schemaErr
		}

		// 更新 Ready=true
		if s.db != nil {
			if result := s.db.WithContext(ctx).Model(&gormmodel.IndexFingerprint{}).
				Where("vector_store_type = ? AND index_name = ?", "qdrant", indexName).
				Update("ready", true); result.Error != nil {
				return fmt.Errorf("set fingerprint Ready=true for index %q: %w", indexName, result.Error)
			} else if result.RowsAffected == 0 {
				return fmt.Errorf("set fingerprint Ready=true for index %q: record not found or unchanged", indexName)
			}
		}
		return nil
	}

	// fp == nil: 向后兼容路径（无 fingerprint）
	_, err = s.client.CreateFieldIndex(ctx, &qdrant.CreateFieldIndexCollection{
		CollectionName: indexName,
		FieldName:      coretypes.KnowledgeName,
		FieldType:      qdrant.FieldType_FieldTypeKeyword.Enum(),
	})
	if err != nil {
		// R7: Do NOT auto-delete collection on failure.
		return fmt.Errorf("failed to create field index: %w", err)
	}

	return nil
}

// IndexExists 检查集合是否存在
func (s *QdrantVectorStore) IndexExists(ctx context.Context, indexName string) (bool, error) {
	exists, err := s.client.CollectionExists(ctx, indexName)
	if err != nil {
		return false, fmt.Errorf("failed to check collection exists: %w", err)
	}
	return exists, nil
}

// DeleteDocument 删除文档
func (s *QdrantVectorStore) DeleteDocument(ctx context.Context, indexName, documentID string) error {
	_, err := s.client.Delete(ctx, &qdrant.DeletePoints{
		CollectionName: indexName,
		Points: &qdrant.PointsSelector{
			PointsSelectorOneOf: &qdrant.PointsSelector_Points{
				Points: &qdrant.PointsIdsList{
					Ids: []*qdrant.PointId{
						{PointIdOptions: &qdrant.PointId_Uuid{Uuid: documentID}},
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to delete document: %w", err)
	}
	return nil
}

// GetKnowledgeBaseList 获取知识库列表
func (s *QdrantVectorStore) GetKnowledgeBaseList(ctx context.Context, indexName string) ([]string, error) {
	// Qdrant 不直接支持聚合，需要通过滚动查询获取所有唯一的知识库名称
	// 这里使用一个简化的实现：查询所有点并去重
	knowledgeMap := make(map[string]bool)

	offset := (*qdrant.PointId)(nil)
	limit := uint32(100)

	for {
		scrollResp, err := s.client.Scroll(ctx, &qdrant.ScrollPoints{
			CollectionName: indexName,
			Limit:          &limit,
			Offset:         offset,
			WithPayload:    &qdrant.WithPayloadSelector{SelectorOptions: &qdrant.WithPayloadSelector_Enable{Enable: true}},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to scroll points: %w", err)
		}

		if len(scrollResp) == 0 {
			break
		}

		for _, point := range scrollResp {
			if payload := point.GetPayload(); payload != nil {
				if knowledgeName, ok := payload[coretypes.KnowledgeName]; ok {
					if name, ok := knowledgeName.GetKind().(*qdrant.Value_StringValue); ok {
						knowledgeMap[name.StringValue] = true
					}
				}
			}
		}

		if len(scrollResp) < int(limit) {
			break
		}
		offset = scrollResp[len(scrollResp)-1].Id
	}

	var list []string
	for name := range knowledgeMap {
		list = append(list, name)
	}

	return list, nil
}

// SearchDocuments 搜索文档
func (s *QdrantVectorStore) SearchDocuments(ctx context.Context, req *SearchRequest) (*SearchResponse, error) {
	// 构建过滤条件
	var filter *qdrant.Filter
	if req.KnowledgeName != "" {
		filter = &qdrant.Filter{
			Must: []*qdrant.Condition{
				{
					ConditionOneOf: &qdrant.Condition_Field{
						Field: &qdrant.FieldCondition{
							Key: coretypes.KnowledgeName,
							Match: &qdrant.Match{
								MatchValue: &qdrant.Match_Keyword{
									Keyword: req.KnowledgeName,
								},
							},
						},
					},
				},
			},
		}
	}

	if len(req.DocIDs) > 0 {
		ids := make([]*qdrant.PointId, len(req.DocIDs))
		for i, id := range req.DocIDs {
			ids[i] = &qdrant.PointId{PointIdOptions: &qdrant.PointId_Uuid{Uuid: id}}
		}

		hasFilter := &qdrant.Condition{
			ConditionOneOf: &qdrant.Condition_HasId{
				HasId: &qdrant.HasIdCondition{
					HasId: ids,
				},
			},
		}

		if filter == nil {
			filter = &qdrant.Filter{Must: []*qdrant.Condition{hasFilter}}
		} else {
			filter.Must = append(filter.Must, hasFilter)
		}
	}

	// 如果有向量查询，使用 Query API
	if qdrantReq, ok := req.Query.(*QdrantSearchQuery); ok && qdrantReq != nil {
		searchResp, err := s.client.Query(ctx, &qdrant.QueryPoints{
			CollectionName: qdrantReq.CollectionName,
			Query:          qdrantReq.Query,
			Filter:         filter,
			Limit:          uint64ptr(uint64(req.Size)),
			WithPayload:    &qdrant.WithPayloadSelector{SelectorOptions: &qdrant.WithPayloadSelector_Enable{Enable: true}},
			WithVectors:    &qdrant.WithVectorsSelector{SelectorOptions: &qdrant.WithVectorsSelector_Enable{Enable: true}},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to search: %w", err)
		}

		var docs []*schema.Document
		for _, point := range searchResp {
			doc, err := s.qdrantPoint2Document(ctx, point)
			if err != nil {
				g.Log().Errorf(ctx, "qdrantPoint2Document failed, err=%v", err)
				continue
			}
			docs = append(docs, doc)
		}

		return &SearchResponse{
			Documents: docs,
			Total:     int64(len(docs)),
		}, nil
	}

	// 否则使用 Scroll API（用于按 ID 和过滤条件获取文档）
	scrollResp, err := s.client.Scroll(ctx, &qdrant.ScrollPoints{
		CollectionName: req.IndexName,
		Filter:         filter,
		Limit:          uint32ptr(uint32(req.Size)),
		WithPayload:    &qdrant.WithPayloadSelector{SelectorOptions: &qdrant.WithPayloadSelector_Enable{Enable: true}},
		WithVectors:    &qdrant.WithVectorsSelector{SelectorOptions: &qdrant.WithVectorsSelector_Enable{Enable: true}},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to scroll: %w", err)
	}

	var docs []*schema.Document
	for _, point := range scrollResp {
		doc, err := s.qdrantScrollPoint2Document(ctx, point)
		if err != nil {
			g.Log().Errorf(ctx, "qdrantScrollPoint2Document failed, err=%v", err)
			continue
		}
		docs = append(docs, doc)
	}

	return &SearchResponse{
		Documents: docs,
		Total:     int64(len(docs)),
	}, nil
}

// qdrantPoint2Document 将 Qdrant ScoredPoint 转换为 Document
func (s *QdrantVectorStore) qdrantPoint2Document(_ context.Context, point *qdrant.ScoredPoint) (*schema.Document, error) {
	var docID string
	switch id := point.Id.GetPointIdOptions().(type) {
	case *qdrant.PointId_Uuid:
		docID = id.Uuid
	case *qdrant.PointId_Num:
		docID = fmt.Sprintf("%d", id.Num)
	default:
		return nil, fmt.Errorf("unsupported point id type")
	}

	doc := &schema.Document{
		ID:       docID,
		MetaData: map[string]any{},
	}

	payload := point.GetPayload()
	if payload != nil {
		// 提取 content
		if content, ok := payload[coretypes.FieldContent]; ok {
			if val, ok := content.GetKind().(*qdrant.Value_StringValue); ok {
				doc.Content = val.StringValue
			}
		}

		// 提取 extra
		if extra, ok := payload[coretypes.FieldExtra]; ok {
			if val, ok := extra.GetKind().(*qdrant.Value_StringValue); ok {
				doc.MetaData[coretypes.FieldExtra] = val.StringValue
			}
		}

		// 提取 knowledge_name
		if knowledgeName, ok := payload[coretypes.KnowledgeName]; ok {
			if val, ok := knowledgeName.GetKind().(*qdrant.Value_StringValue); ok {
				doc.MetaData[coretypes.KnowledgeName] = val.StringValue
			}
		}
	}

	// 提取向量
	if vectors := point.GetVectors(); vectors != nil {
		switch v := vectors.GetVectorsOptions().(type) {
		case *qdrant.VectorsOutput_Vector:
			// 转换 float32 到 float64
			data := v.Vector.GetData()
			vec64 := make([]float64, len(data))
			for i, val := range data {
				vec64[i] = float64(val)
			}
			doc.WithDenseVector(vec64)
		}
	}

	// 提取分数
	doc.WithScore(float64(point.Score))

	return doc, nil
}

// qdrantScrollPoint2Document 将 Qdrant RetrievedPoint 转换为 Document
func (s *QdrantVectorStore) qdrantScrollPoint2Document(_ context.Context, point *qdrant.RetrievedPoint) (*schema.Document, error) {
	var docID string
	switch id := point.Id.GetPointIdOptions().(type) {
	case *qdrant.PointId_Uuid:
		docID = id.Uuid
	case *qdrant.PointId_Num:
		docID = fmt.Sprintf("%d", id.Num)
	default:
		return nil, fmt.Errorf("unsupported point id type")
	}

	doc := &schema.Document{
		ID:       docID,
		MetaData: map[string]any{},
	}

	payload := point.GetPayload()
	if payload != nil {
		// 提取 content
		if content, ok := payload[coretypes.FieldContent]; ok {
			if val, ok := content.GetKind().(*qdrant.Value_StringValue); ok {
				doc.Content = val.StringValue
			}
		}

		// 提取 extra
		if extra, ok := payload[coretypes.FieldExtra]; ok {
			if val, ok := extra.GetKind().(*qdrant.Value_StringValue); ok {
				doc.MetaData[coretypes.FieldExtra] = val.StringValue
			}
		}

		// 提取 knowledge_name
		if knowledgeName, ok := payload[coretypes.KnowledgeName]; ok {
			if val, ok := knowledgeName.GetKind().(*qdrant.Value_StringValue); ok {
				doc.MetaData[coretypes.KnowledgeName] = val.StringValue
			}
		}
	}

	// 提取向量
	if vectors := point.GetVectors(); vectors != nil {
		switch v := vectors.GetVectorsOptions().(type) {
		case *qdrant.VectorsOutput_Vector:
			// 转换 float32 到 float64
			data := v.Vector.GetData()
			vec64 := make([]float64, len(data))
			for i, val := range data {
				vec64[i] = float64(val)
			}
			doc.WithDenseVector(vec64)
		}
	}

	return doc, nil
}

// WriteFingerprint writes the fingerprint to SQL table for Qdrant.
// F3: Conditional write — refuses to overwrite if record already exists.
// F2: Accepts resourceID for stable identity binding.
func (s *QdrantVectorStore) WriteFingerprint(ctx context.Context, indexName string, fp *Fingerprint, resourceID string) error {
	if fp == nil {
		return fmt.Errorf("nil fingerprint")
	}
	if s.db == nil {
		return fmt.Errorf("Qdrant fingerprint storage requires SQL database (db is nil)")
	}

	// F3: Check if record already exists (conditional write)
	var existing gormmodel.IndexFingerprint
	result := s.db.WithContext(ctx).
		Where("vector_store_type = ? AND index_name = ?", "qdrant", indexName).
		First(&existing)
	if result.Error == nil {
		// Record already exists — refuse to overwrite
		return fmt.Errorf("index %q: %w", indexName, ErrFingerprintExists)
	}
	if result.Error != nil && result.Error != gorm.ErrRecordNotFound {
		return fmt.Errorf("check existing fingerprint: %w", result.Error)
	}

	// Generate resourceID if not provided
	if resourceID == "" {
		resourceID = uuid.New().String()
	}

	// Serialize fingerprint to JSON
	fpJSON, err := fp.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshal fingerprint: %w", err)
	}
	// Create record with Ready=false first
	record := gormmodel.IndexFingerprint{
		VectorStoreType:   "qdrant",
		IndexName:         indexName,
		ResourceID:        resourceID,
		FingerprintJSON:   string(fpJSON),
		FingerprintDigest: fp.Digest(),
		EmbeddingModelID:  fp.EmbeddingModelID,
		Dimensions:        fp.Dimensions,
		ProviderName:      fp.ProviderName,
		Similarity:        fp.Similarity,
		Ready:             false,
	}
	// Insert (NOT upsert — F3 conditional write)
	if result := s.db.WithContext(ctx).Create(&record); result.Error != nil {
		return fmt.Errorf("write fingerprint: %w", result.Error)
	}
	// R2: Update Ready=true after successful insert — return error on failure (not swallowed).
	if result := s.db.WithContext(ctx).Model(&record).Update("ready", true); result.Error != nil {
		return fmt.Errorf("set fingerprint Ready=true for index %q: %w", indexName, result.Error)
	} else if result.RowsAffected == 0 {
		return fmt.Errorf("set fingerprint Ready=true for index %q: record not found or unchanged", indexName)
	}
	return nil
}

// ReadFingerprint reads the fingerprint from SQL table for Qdrant.
// F2: Returns resourceID alongside fingerprint.
// F7: Verifies digest integrity after reading.
// R2: Verifies collection actually exists in Qdrant when client is available.
// C4: Schema validation — verifies actual vector dimensions match fingerprint.
func (s *QdrantVectorStore) ReadFingerprint(ctx context.Context, indexName string) (*Fingerprint, string, error) {
	if s.db == nil {
		return nil, "", fmt.Errorf("Qdrant fingerprint storage requires SQL database (db is nil)")
	}
	var record gormmodel.IndexFingerprint
	result := s.db.WithContext(ctx).
		Where("vector_store_type = ? AND index_name = ?", "qdrant", indexName).
		First(&record)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, "", ErrFingerprintMissing
		}
		return nil, "", fmt.Errorf("read fingerprint: %w", result.Error)
	}
	// F2: Check Ready flag — incomplete creation must fail closed
	if !record.Ready {
		return nil, "", fmt.Errorf("index %q fingerprint creation incomplete (Ready=false): %w",
			indexName, ErrFingerprintCorrupted)
	}
	// Parse the stored JSON (R5: ParseFingerprint now verifies digest internally)
	fp, err := ParseFingerprint([]byte(record.FingerprintJSON))
	if err != nil {
		return nil, "", err
	}
	// R2: Verify collection actually exists in Qdrant (not just in SQL)
	if s.client != nil {
		info, checkErr := s.client.GetCollectionInfo(ctx, indexName)
		if checkErr != nil {
			return nil, "", fmt.Errorf("verify collection exists: %w", checkErr)
		}
		if info == nil {
			return nil, "", fmt.Errorf("%w: SQL record exists but Qdrant collection %q missing",
				ErrFingerprintCorrupted, indexName)
		}
		// D3: 严格 schema 验证
		fields, extractErr := extractQdrantFieldInfo(info)
		if extractErr != nil {
			return nil, "", fmt.Errorf("extract schema info: %w", extractErr)
		}
		if schemaErr := ValidateSchemaStrict(fp, fields, "qdrant"); schemaErr != nil {
			return nil, "", schemaErr
		}
	}
	return fp, record.ResourceID, nil
}

// Close 关闭连接
func (s *QdrantVectorStore) Close() error {
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}

// uint32ptr 辅助函数
func uint32ptr(v uint32) *uint32 {
	return &v
}

// uint64ptr 辅助函数
func uint64ptr(v uint64) *uint64 {
	return &v
}

// isQdrantAlreadyExists checks if a Qdrant error indicates the collection already exists.
// D1: Normalizes to ErrIndexAlreadyExists for consistent conflict handling.
func isQdrantAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "collection already exists")
}

// mapQdrantDistance maps Qdrant distance enum values to their string representation.
// All known enums are mapped; unknown values return an error (fail closed).
func mapQdrantDistance(d qdrant.Distance) (string, error) {
	switch d {
	case qdrant.Distance_Cosine:
		return "Cosine", nil
	case qdrant.Distance_Dot:
		return "Dot", nil
	case qdrant.Distance_Euclid:
		return "Euclid", nil
	case qdrant.Distance_Manhattan:
		return "Manhattan", nil
	default:
		return "", fmt.Errorf("unknown Qdrant distance enum: %d", d)
	}
}

// extractQdrantFieldInfo extracts FieldInfo map from Qdrant collection info for ValidateSchemaStrict.
// D3: Returns error if required config/named vectors are missing (fail closed).
// E1: All distance enums mapped; unknown distance returns error.
func extractQdrantFieldInfo(info *qdrant.CollectionInfo) (map[string]FieldInfo, error) {
	if info == nil || info.GetConfig() == nil || info.GetConfig().GetParams() == nil {
		return nil, fmt.Errorf("collection has no config params")
	}
	vc := info.GetConfig().GetParams().GetVectorsConfig()
	if vc == nil {
		return nil, fmt.Errorf("collection has no vectors config")
	}
	namedVectors := vc.GetParamsMap()
	if namedVectors == nil || namedVectors.GetMap() == nil {
		return nil, fmt.Errorf("collection has no named vectors map")
	}
	fields := make(map[string]FieldInfo)
	for name, params := range namedVectors.GetMap() {
		if params == nil {
			continue
		}
		fi := FieldInfo{
			Dims: int64(params.GetSize()),
		}
		// Map Qdrant distance to string for ValidateSchemaStrict
		distStr, mapErr := mapQdrantDistance(params.GetDistance())
		if mapErr != nil {
			return nil, mapErr
		}
		fi.Distance = distStr
		fields[name] = fi
	}
	return fields, nil
}

// QdrantSearchQuery Qdrant 搜索查询
type QdrantSearchQuery struct {
	CollectionName string
	Query          *qdrant.Query
}
