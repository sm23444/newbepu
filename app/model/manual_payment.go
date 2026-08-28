package model

import (
	"errors"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

var (
	ErrManualPaymentAlreadyClaimed = errors.New("transaction hash has already been claimed")
	ErrManualPaymentOrderChanged   = errors.New("order is no longer eligible for manual payment recovery")
)

type ManualPaymentClaim struct {
	Id
	Network string `gorm:"column:network;type:varchar(32);not null;uniqueIndex:idx_manual_payment_network_hash,priority:1" json:"network"`
	TxHash  string `gorm:"column:tx_hash;type:varchar(128);not null;uniqueIndex:idx_manual_payment_network_hash,priority:2" json:"tx_hash"`
	TradeID string `gorm:"column:trade_id;type:varchar(128);not null;uniqueIndex" json:"trade_id"`
	AutoTimeAt
}

func NormalizeManualPaymentReference(txHash string, caseInsensitive bool) string {
	value := strings.TrimSpace(txHash)
	if caseInsensitive {
		return strings.ToLower(value)
	}
	return value
}

func (ManualPaymentClaim) TableName() string {
	return "bep_manual_payment_claim"
}

func ManualPaymentTransactionUsed(network, txHash string, caseInsensitive bool) (bool, error) {
	txHash = NormalizeManualPaymentReference(txHash, caseInsensitive)
	var count int64
	query := Db.Model(&ManualPaymentClaim{}).Where("network = ?", network)
	if caseInsensitive {
		query = query.Where("LOWER(tx_hash) = ?", strings.ToLower(txHash))
	} else {
		query = query.Where("tx_hash = ?", txHash)
	}
	if err := query.Count(&count).Error; err != nil || count > 0 {
		return count > 0, err
	}

	query = Db.Model(&Order{}).
		Where("status in (?)", []int{OrderStatusConfirming, OrderStatusSuccess}).
		Where("trade_type in (?)", GetNetworkTrades(Network(network)))
	if caseInsensitive {
		query = query.Where("LOWER(ref_hash) = ?", strings.ToLower(txHash))
	} else {
		query = query.Where("ref_hash = ?", txHash)
	}
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func ClaimManualPayment(order *Order, network, txHash string, blockNum int, from string, at time.Time, amount decimal.Decimal, caseInsensitive bool) error {
	txHash = NormalizeManualPaymentReference(txHash, caseInsensitive)
	updates := map[string]any{
		"from_address":  from,
		"confirmed_at":  at,
		"ref_hash":      txHash,
		"ref_block_num": blockNum,
		"status":        OrderStatusConfirming,
	}
	if order.AddressLocked {
		rate, err := decimal.NewFromString(order.Rate)
		if err != nil {
			return err
		}
		updates["amount"] = amount.String()
		updates["money"] = rate.Mul(amount).String()
	}

	err := Db.Transaction(func(tx *gorm.DB) error {
		var count int64
		claimQuery := tx.Model(&ManualPaymentClaim{}).Where("network = ?", network)
		orderQuery := tx.Model(&Order{}).
			Where("status in (?)", []int{OrderStatusConfirming, OrderStatusSuccess}).
			Where("trade_type in (?)", GetNetworkTrades(Network(network)))
		if caseInsensitive {
			claimQuery = claimQuery.Where("LOWER(tx_hash) = ?", strings.ToLower(txHash))
			orderQuery = orderQuery.Where("LOWER(ref_hash) = ?", strings.ToLower(txHash))
		} else {
			claimQuery = claimQuery.Where("tx_hash = ?", txHash)
			orderQuery = orderQuery.Where("ref_hash = ?", txHash)
		}
		if err := claimQuery.Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrManualPaymentAlreadyClaimed
		}
		if err := orderQuery.Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrManualPaymentAlreadyClaimed
		}

		if err := ClaimPaymentTransactionTx(tx, order, network, txHash); err != nil {
			if errors.Is(err, ErrPaymentTransactionAlreadyClaimed) {
				return ErrManualPaymentAlreadyClaimed
			}
			return err
		}

		if err := tx.Create(&ManualPaymentClaim{Network: network, TxHash: txHash, TradeID: order.TradeId}).Error; err != nil {
			message := strings.ToLower(err.Error())
			if strings.Contains(message, "unique constraint") || strings.Contains(message, "duplicate key") {
				return ErrManualPaymentAlreadyClaimed
			}
			return err
		}

		updated := tx.Model(&Order{}).
			Where("id = ? and trade_id = ?", order.ID, order.TradeId).
			Where("status in (?)", []int{OrderStatusWaiting, OrderStatusExpired}).
			Where("ref_hash = ?", order.RefHash).
			Where("trade_type = ?", order.TradeType).
			Where("fiat = ?", order.Fiat).
			Where("crypto = ?", order.Crypto).
			Where("rate = ?", order.Rate).
			Where("amount = ?", order.Amount).
			Where("money = ?", order.Money).
			Where("address = ?", order.Address).
			Where("match_address = ?", order.MatchAddress).
			Where("address_locked = ?", order.AddressLocked).
			Where("expired_at = ?", order.ExpiredAt).
			Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrManualPaymentOrderChanged
		}
		return nil
	})
	if err != nil {
		return err
	}

	order.FromAddress = from
	order.ConfirmedAt = &at
	order.RefHash = txHash
	order.RefBlockNum = blockNum
	order.Status = OrderStatusConfirming
	if order.AddressLocked {
		order.Amount = amount.String()
		rate, _ := decimal.NewFromString(order.Rate)
		order.Money = rate.Mul(amount).String()
	}
	return nil
}
