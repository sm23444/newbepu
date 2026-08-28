package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/v03413/bepusdt/app/conf"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrPaymentTransactionAlreadyClaimed = errors.New("payment transaction has already been claimed")
	ErrPaymentTransactionInvalid        = errors.New("payment transaction reference is invalid")
)

// PaymentTransactionClaim is the durable, cross-worker claim for an on-chain
// transaction. The unique network/hash key is deliberately separate from the
// order row so existing order history can be upgraded without rewriting it.
type PaymentTransactionClaim struct {
	Id
	Network   string    `gorm:"column:network;type:varchar(32);not null;uniqueIndex:idx_payment_transaction_network_hash,priority:1" json:"network"`
	TxHash    string    `gorm:"column:tx_hash;type:varchar(256);not null;uniqueIndex:idx_payment_transaction_network_hash,priority:2" json:"tx_hash"`
	TradeType TradeType `gorm:"column:trade_type;type:varchar(20);not null;index" json:"trade_type"`
	OrderID   int64     `gorm:"column:order_id;not null;index" json:"order_id"`
	AutoTimeAt
}

func (PaymentTransactionClaim) TableName() string {
	return "bep_payment_transaction_claim"
}

// NormalizePaymentTransactionReference applies the case rules used by the
// underlying network. Hex transaction IDs are case-insensitive; Solana's
// base58 signatures are case-sensitive.
func NormalizePaymentTransactionReference(network, txHash string) string {
	value := strings.TrimSpace(txHash)
	if !strings.EqualFold(strings.TrimSpace(network), conf.Solana) {
		return strings.ToLower(value)
	}
	return value
}

func paymentNetworkForTradeType(tradeType TradeType) (string, bool) {
	trade, ok := GetTradeConfig(tradeType)
	if !ok || strings.TrimSpace(string(trade.Network)) == "" {
		return "", false
	}
	return strings.ToLower(strings.TrimSpace(string(trade.Network))), true
}

// ClaimPaymentTransactionTx reserves a transaction reference in the caller's
// transaction. The caller must update the order in that same transaction so a
// failed order update rolls the claim back as well.
func ClaimPaymentTransactionTx(tx *gorm.DB, order *Order, network, txHash string) error {
	if tx == nil || order == nil || order.ID == 0 {
		return ErrPaymentTransactionInvalid
	}

	network = strings.ToLower(strings.TrimSpace(network))
	txHash = NormalizePaymentTransactionReference(network, txHash)
	if network == "" || txHash == "" {
		return ErrPaymentTransactionInvalid
	}

	var existing PaymentTransactionClaim
	lookup := tx.Where("network = ? AND tx_hash = ?", network, txHash).Limit(1).Find(&existing)
	switch {
	case lookup.Error != nil:
		return lookup.Error
	case lookup.RowsAffected > 0:
		if existing.OrderID == order.ID {
			return nil
		}
		return ErrPaymentTransactionAlreadyClaimed
	}

	// Orders created before this claim table was introduced may already contain
	// a real transaction reference. Waiting orders use their trade ID as a
	// placeholder, so only terminal or confirming payment states participate in
	// this legacy check.
	orderQuery := tx.Model(&Order{}).
		Where("id <> ? AND status IN (?) AND ref_hash <> ''", order.ID, []int{OrderStatusConfirming, OrderStatusSuccess, OrderStatusFailed})
	if tradeTypes := GetNetworkTrades(Network(network)); len(tradeTypes) > 0 {
		orderQuery = orderQuery.Where("trade_type IN (?)", tradeTypes)
	}
	if strings.EqualFold(network, conf.Solana) {
		orderQuery = orderQuery.Where("ref_hash = ?", txHash)
	} else {
		orderQuery = orderQuery.Where("LOWER(ref_hash) = ?", strings.ToLower(txHash))
	}

	var count int64
	if err := orderQuery.Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrPaymentTransactionAlreadyClaimed
	}

	claim := PaymentTransactionClaim{
		Network:   network,
		TxHash:    txHash,
		TradeType: order.TradeType,
		OrderID:   order.ID,
	}
	created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&claim)
	if created.Error != nil {
		return created.Error
	}
	if created.RowsAffected == 1 {
		return nil
	}

	// Another worker won the unique claim while this transaction was waiting.
	// Read it back to distinguish an idempotent retry for this order from a
	// reference that belongs to a different order.
	if err := tx.Where("network = ? AND tx_hash = ?", network, txHash).First(&existing).Error; err != nil {
		return fmt.Errorf("load payment transaction claim: %w", err)
	}
	if existing.OrderID == order.ID {
		return nil
	}
	return ErrPaymentTransactionAlreadyClaimed
}
