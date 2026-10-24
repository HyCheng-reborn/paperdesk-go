package gorm

import (
	"time"
)

// IndexFingerprint GORM model for storing vector index fingerprints.
// Used by Qdrant (which has no collection-level metadata) to persist fingerprint data.
// ES uses _meta field in index mapping instead.
type IndexFingerprint struct {
	ID                int64     `gorm:"primaryKey;autoIncrement"`
	VectorStoreType   string    `gorm:"type:varchar(16);not null;uniqueIndex:uk_store_index"`
	IndexName         string    `gorm:"type:varchar(255);not null;uniqueIndex:uk_store_index"`
	FingerprintJSON   string    `gorm:"type:text;not null;column:fingerprint_json"`
	FingerprintDigest string    `gorm:"type:varchar(64);not null;column:fingerprint_digest"`
	EmbeddingModelID  string    `gorm:"type:varchar(255);not null"`
	Dimensions        int       `gorm:"type:int;not null"`
	ProviderName      string    `gorm:"type:varchar(64);not null"`
	Similarity        string    `gorm:"type:varchar(16);not null"`
	CreatedAt         time.Time `gorm:"autoCreateTime"`
	UpdatedAt         time.Time `gorm:"autoUpdateTime"`
}

// TableName sets the table name for IndexFingerprint.
func (IndexFingerprint) TableName() string {
	return "index_fingerprints"
}
