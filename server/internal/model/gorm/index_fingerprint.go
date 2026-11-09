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
	ResourceID        string    `gorm:"type:varchar(36);not null"`  // F2: stable resource identity
	FingerprintJSON   string    `gorm:"type:text;not null;column:fingerprint_json"`
	FingerprintDigest string    `gorm:"type:varchar(128);not null;column:fingerprint_digest"` // F6: varchar(64) → varchar(128)
	EmbeddingModelID  string    `gorm:"type:varchar(255);not null"`
	Dimensions        int       `gorm:"type:int;not null"`
	ProviderName      string    `gorm:"type:varchar(64);not null"`
	Similarity        string    `gorm:"type:varchar(16);not null"`
	Ready             bool      `gorm:"type:tinyint;not null;default:false"` // F2: creation completion flag
	CreatedAt         time.Time `gorm:"autoCreateTime"`
	UpdatedAt         time.Time `gorm:"autoUpdateTime"`
}

// TableName sets the table name for IndexFingerprint.
func (IndexFingerprint) TableName() string {
	return "index_fingerprints"
}
