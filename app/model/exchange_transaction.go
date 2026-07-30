package model

import (
	"errors"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ExchangeTransactionPending   uint8 = 0
	ExchangeTransactionProcessed uint8 = 1
)

var (
	ErrExchangeTransactionNotPending = errors.New("exchange transaction is no longer pending")
	ErrExchangeOrderNotReceivable    = errors.New("exchange order is no longer receivable")
)

type ExchangeTransaction struct {
	Id
	Provider      string    `gorm:"column:provider;type:varchar(16);not null;uniqueIndex:idx_exchange_transaction,priority:1" json:"provider"`
	TransactionID string    `gorm:"column:transaction_id;type:varchar(128);not null;uniqueIndex:idx_exchange_transaction,priority:2" json:"transaction_id"`
	TradeType     TradeType `gorm:"column:trade_type;type:varchar(20);not null;index" json:"trade_type"`
	Asset         string    `gorm:"column:asset;type:varchar(16);not null" json:"asset"`
	Amount        string    `gorm:"column:amount;type:varchar(32);not null" json:"amount"`
	ReceiverUID   string    `gorm:"column:receiver_uid;type:varchar(128);not null;index" json:"receiver_uid"`
	OccurredAt    time.Time `gorm:"column:occurred_at;not null;index" json:"occurred_at"`
	Status        uint8     `gorm:"column:status;not null;default:0;index" json:"status"`
	OrderID       int64     `gorm:"column:order_id;not null;default:0;index" json:"order_id"`
	AutoTimeAt
}

func (ExchangeTransaction) TableName() string {
	return "bep_exchange_transaction"
}

func StoreExchangeTransactions(rows []ExchangeTransaction) error {
	if len(rows) == 0 {
		return nil
	}

	return Db.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}

func PendingExchangeTransactions(provider string, since time.Time, limit int) ([]ExchangeTransaction, error) {
	var rows []ExchangeTransaction
	if limit <= 0 {
		limit = 500
	}

	err := Db.Where("provider = ? and status = ? and occurred_at >= ?", provider, ExchangeTransactionPending, since).
		Order("occurred_at desc, id desc").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}

	if len(rows) == 0 {
		return []ExchangeTransaction{}, nil
	}

	transactionIDs := make([]string, 0, len(rows))
	tradeTypes := make([]TradeType, 0, len(rows))
	seenTradeTypes := make(map[TradeType]struct{})
	for _, row := range rows {
		transactionIDs = append(transactionIDs, row.TransactionID)
		if _, seen := seenTradeTypes[row.TradeType]; !seen {
			seenTradeTypes[row.TradeType] = struct{}{}
			tradeTypes = append(tradeTypes, row.TradeType)
		}
	}

	var linkedOrders []Order
	if err := Db.Select("id, ref_hash, trade_type").
		Where("ref_hash in (?) and trade_type in (?) and status in (?)", transactionIDs, tradeTypes, []int{OrderStatusConfirming, OrderStatusSuccess}).
		Order("id desc").
		Find(&linkedOrders).Error; err != nil {
		return nil, err
	}

	type transferKey struct {
		transactionID string
		tradeType     TradeType
	}
	orderByTransfer := make(map[transferKey]int64, len(linkedOrders))
	for _, order := range linkedOrders {
		key := transferKey{transactionID: order.RefHash, tradeType: order.TradeType}
		if _, exists := orderByTransfer[key]; !exists {
			orderByTransfer[key] = order.ID
		}
	}

	pending := make([]ExchangeTransaction, 0, len(rows))
	processed := make(map[int64]int64)
	for _, row := range rows {
		orderID, found := orderByTransfer[transferKey{transactionID: row.TransactionID, tradeType: row.TradeType}]
		if !found {
			pending = append(pending, row)
			continue
		}
		processed[row.ID] = orderID
	}
	if err := markExchangeTransactionsProcessedBatch(processed); err != nil {
		return nil, err
	}

	return pending, nil
}

func markExchangeTransactionsProcessedBatch(assignments map[int64]int64) error {
	if len(assignments) == 0 {
		return nil
	}

	ids := make([]int64, 0, len(assignments))
	caseParts := make([]string, 0, len(assignments))
	caseArgs := make([]any, 0, len(assignments)*2)
	for rowID, orderID := range assignments {
		ids = append(ids, rowID)
		caseParts = append(caseParts, "WHEN ? THEN ?")
		caseArgs = append(caseArgs, rowID, orderID)
	}
	orderIDExpr := gorm.Expr("CASE id "+strings.Join(caseParts, " ")+" ELSE order_id END", caseArgs...)

	return Db.Model(&ExchangeTransaction{}).
		Where("id in (?) and status = ?", ids, ExchangeTransactionPending).
		Updates(map[string]any{
			"status":   ExchangeTransactionProcessed,
			"order_id": orderIDExpr,
		}).Error
}

// CompleteExchangeTransaction commits the transaction claim and final order
// state together, so a crash cannot leave only one side persisted.
func CompleteExchangeTransaction(order *Order, provider, transactionID string, blockNum int, from string, at time.Time, amount decimal.Decimal) error {
	if order.Status == OrderStatusConfirming && order.RefHash != transactionID {
		return ErrExchangeOrderNotReceivable
	}

	updates := map[string]any{
		"from_address":  from,
		"confirmed_at":  at,
		"ref_hash":      transactionID,
		"ref_block_num": blockNum,
		"status":        OrderStatusSuccess,
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
		claimed := tx.Model(&ExchangeTransaction{}).
			Where("provider = ? and transaction_id = ? and status = ?", provider, transactionID, ExchangeTransactionPending).
			Updates(map[string]any{
				"status":   ExchangeTransactionProcessed,
				"order_id": order.ID,
			})
		if claimed.Error != nil {
			return claimed.Error
		}
		if claimed.RowsAffected != 1 {
			return ErrExchangeTransactionNotPending
		}

		query := tx.Model(&Order{}).
			Where("id = ? and status in (?)", order.ID, []int{OrderStatusWaiting, OrderStatusExpired, OrderStatusConfirming}).
			Where("trade_type = ? and address = ? and match_address = ? and address_locked = ?", order.TradeType, order.Address, order.MatchAddress, order.AddressLocked).
			Where("ref_hash = ?", order.RefHash)
		if !order.AddressLocked {
			query = query.Where("amount = ?", order.Amount)
		}

		updated := query.Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrExchangeOrderNotReceivable
		}

		return nil
	})
	if err != nil {
		return err
	}

	order.FromAddress = from
	order.ConfirmedAt = &at
	order.RefHash = transactionID
	order.RefBlockNum = blockNum
	order.Status = OrderStatusSuccess
	if order.AddressLocked {
		order.Amount = amount.String()
		rate, _ := decimal.NewFromString(order.Rate)
		order.Money = rate.Mul(amount).String()
	}

	return nil
}

func MarkExchangeTransactionProcessed(provider, transactionID string, orderID int64) error {
	return Db.Model(&ExchangeTransaction{}).
		Where("provider = ? and transaction_id = ?", provider, transactionID).
		Updates(map[string]any{
			"status":   ExchangeTransactionProcessed,
			"order_id": orderID,
		}).Error
}

func DeleteExchangeTransactionsBefore(cutoff time.Time) error {
	return Db.Where("occurred_at < ?", cutoff).Delete(&ExchangeTransaction{}).Error
}
