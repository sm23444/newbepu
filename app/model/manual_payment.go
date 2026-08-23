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

type VerifiedManualPayment struct {
	Network     string
	TxHash      string
	Amount      decimal.Decimal
	FromAddress string
	RecvAddress string
	TradeType   TradeType
	Timestamp   time.Time
	BlockNum    int
}

func (ManualPaymentClaim) TableName() string {
	return "bep_manual_payment_claim"
}

func ManualPaymentTransactionUsed(network, txHash string, caseInsensitive bool) (bool, error) {
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

// CompleteManualPaymentTx claims a verified on-chain transfer and marks the
// matching order successful in the caller's transaction. The order snapshot
// is checked in the update predicate so a concurrent payment-method/state
// change cannot be overwritten by a stale verification.
func CompleteManualPaymentTx(tx *gorm.DB, order *Order, payment VerifiedManualPayment, caseInsensitive bool) error {
	if order == nil || order.ID == 0 || order.TradeId == "" {
		return ErrManualPaymentOrderChanged
	}
	matchAddress := order.MatchAddress
	if matchAddress == "" {
		matchAddress = order.Address
	}
	hashMatches := order.RefHash == payment.TxHash
	addressMatches := matchAddress == payment.RecvAddress
	if caseInsensitive {
		hashMatches = strings.EqualFold(order.RefHash, payment.TxHash)
		addressMatches = strings.EqualFold(matchAddress, payment.RecvAddress)
	}
	if payment.TradeType != order.TradeType || !addressMatches || !order.CreatedAt.Before(payment.Timestamp) || !order.ExpiredAt.After(payment.Timestamp) {
		return ErrManualPaymentOrderChanged
	}
	if !order.AddressLocked {
		expected, err := decimal.NewFromString(order.Amount)
		if err != nil || !payment.Amount.Equal(expected) {
			return ErrManualPaymentOrderChanged
		}
	}
	if order.Status == OrderStatusConfirming && hashMatches {
		updated := tx.Model(&Order{}).
			Where("id = ? AND trade_id = ? AND status = ? AND ref_hash = ?", order.ID, order.TradeId, OrderStatusConfirming, order.RefHash).
			Updates(map[string]any{
				"status":        OrderStatusSuccess,
				"from_address":  payment.FromAddress,
				"confirmed_at":  payment.Timestamp,
				"ref_block_num": payment.BlockNum,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrManualPaymentOrderChanged
		}
		return nil
	}
	var count int64
	claimQuery := tx.Model(&ManualPaymentClaim{}).Where("network = ?", payment.Network)
	orderQuery := tx.Model(&Order{}).
		Where("status in (?)", []int{OrderStatusConfirming, OrderStatusSuccess}).
		Where("trade_type in (?)", GetNetworkTrades(Network(payment.Network)))
	if caseInsensitive {
		claimQuery = claimQuery.Where("LOWER(tx_hash) = ?", strings.ToLower(payment.TxHash))
		orderQuery = orderQuery.Where("LOWER(ref_hash) = ?", strings.ToLower(payment.TxHash))
	} else {
		claimQuery = claimQuery.Where("tx_hash = ?", payment.TxHash)
		orderQuery = orderQuery.Where("ref_hash = ?", payment.TxHash)
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

	if err := tx.Create(&ManualPaymentClaim{Network: payment.Network, TxHash: payment.TxHash, TradeID: order.TradeId}).Error; err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "unique constraint") || strings.Contains(message, "duplicate key") {
			return ErrManualPaymentAlreadyClaimed
		}
		return err
	}

	updates := map[string]any{
		"from_address":  payment.FromAddress,
		"confirmed_at":  payment.Timestamp,
		"ref_hash":      payment.TxHash,
		"ref_block_num": payment.BlockNum,
		"status":        OrderStatusSuccess,
	}
	if order.AddressLocked {
		rate, err := decimal.NewFromString(order.Rate)
		if err != nil {
			return err
		}
		updates["amount"] = payment.Amount.String()
		updates["money"] = rate.Mul(payment.Amount).String()
	}
	query := tx.Model(&Order{}).
		Where("id = ? AND trade_id = ?", order.ID, order.TradeId).
		Where("status IN (?)", []int{OrderStatusWaiting, OrderStatusExpired}).
		Where("ref_hash = ? AND trade_type = ? AND fiat = ? AND crypto = ?", order.RefHash, order.TradeType, order.Fiat, order.Crypto).
		Where("rate = ? AND amount = ? AND money = ? AND address = ? AND match_address = ? AND address_locked = ? AND expired_at = ?", order.Rate, order.Amount, order.Money, order.Address, order.MatchAddress, order.AddressLocked, order.ExpiredAt)
	updated := query.Updates(updates)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrManualPaymentOrderChanged
	}
	return nil
}
