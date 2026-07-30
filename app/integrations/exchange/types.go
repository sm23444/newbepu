package exchange

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

type Transaction struct {
	Provider      string
	TransactionID string
	Asset         string
	Amount        decimal.Decimal
	ReceiverUID   string
	OccurredAt    time.Time
}

type Client interface {
	Provider() string
	ListIncoming(ctx context.Context, asset string, start, end time.Time) ([]Transaction, error)
}
