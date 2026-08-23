package model

import "time"

const (
	PaymentReviewPending  = "pending"
	PaymentReviewApproved = "approved"
	PaymentReviewRejected = "rejected"
)

// PaymentReview stores a payer-submitted recovery request. Evidence files are
// kept outside the public static tree; this table stores only their metadata.
type PaymentReview struct {
	Id
	TradeID         string     `gorm:"column:trade_id;type:varchar(128);not null;index:idx_payment_review_trade_id" json:"trade_id"`
	Status          string     `gorm:"column:status;type:varchar(16);not null;index" json:"status"`
	TransactionHash string     `gorm:"column:transaction_hash;type:varchar(256);not null;default:''" json:"transaction_hash"`
	Description     string     `gorm:"column:description;type:varchar(1000);not null" json:"description"`
	EvidencePath    string     `gorm:"column:evidence_path;type:varchar(512);not null" json:"-"`
	EvidenceType    string     `gorm:"column:evidence_type;type:varchar(64);not null" json:"evidence_type"`
	EvidenceSize    int64      `gorm:"column:evidence_size;not null" json:"evidence_size"`
	EvidenceSHA256  string     `gorm:"column:evidence_sha256;type:char(64);not null" json:"evidence_sha256"`
	ResolutionNote  string     `gorm:"column:resolution_note;type:varchar(1000);not null;default:''" json:"resolution_note"`
	ReviewedBy      string     `gorm:"column:reviewed_by;type:varchar(128);not null;default:''" json:"reviewed_by"`
	ReviewedAt      *time.Time `gorm:"column:reviewed_at" json:"reviewed_at"`
	AutoTimeAt
}

func (PaymentReview) TableName() string {
	return "bep_payment_review"
}
