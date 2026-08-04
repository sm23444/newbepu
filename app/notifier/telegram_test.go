package notifier

import (
	"strings"
	"testing"
	"time"

	"github.com/v03413/bepusdt/app/model"
	"github.com/v03413/bepusdt/app/utils"
)

func TestNonOrderTransferTitleUsesMatchAddress(t *testing.T) {
	trans := model.TronTransfer{
		RecvAddress: "0xf9dff4e813644edc7d9b1b535100b7e92333f743",
		FromAddress: "0x6cba0ff7e76b225ee3b975c3096d828f012c8c72",
	}
	wallet := model.Wallet{
		Address:   "0xF9DFF4E813644eDc7D9b1b535100b7e92333f743",
		MatchAddr: "0xf9dff4e813644edc7d9b1b535100b7e92333f743",
	}

	if title := nonOrderTransferTitle(trans, wallet); title != "收入" {
		t.Fatalf("title = %q, want %q", title, "收入")
	}
}

func TestNonOrderTransferTitleMarksOutgoingTransfer(t *testing.T) {
	trans := model.TronTransfer{
		RecvAddress: "0xrecipient",
		FromAddress: "0xf9dff4e813644edc7d9b1b535100b7e92333f743",
	}
	wallet := model.Wallet{
		Address:   "0xF9DFF4E813644eDc7D9b1b535100b7e92333f743",
		MatchAddr: "0xf9dff4e813644edc7d9b1b535100b7e92333f743",
	}

	if title := nonOrderTransferTitle(trans, wallet); title != "支出" {
		t.Fatalf("title = %q, want %q", title, "支出")
	}
}

func TestNotifyFailTextUsesClaimedAttemptNumber(t *testing.T) {
	confirmedAt := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	order := model.Order{
		OrderId:     "merchant-order-1",
		TradeType:   model.UsdtTrc20,
		Fiat:        model.CNY,
		Amount:      "1.25",
		Money:       "8.75",
		Rate:        "7",
		NotifyNum:   2,
		ConfirmedAt: &confirmedAt,
	}

	text, err := notifyFailText(order, "merchant unavailable")
	if err != nil {
		t.Fatalf("build notify failure text: %v", err)
	}
	want := utils.CalcNextNotifyTime(confirmedAt, order.NotifyNum).Format(time.DateTime)
	wrong := utils.CalcNextNotifyTime(confirmedAt, order.NotifyNum+1).Format(time.DateTime)
	if !strings.Contains(text, want) {
		t.Fatalf("notification text does not contain next retry time %q", want)
	}
	if strings.Contains(text, wrong) {
		t.Fatalf("notification text still uses incremented retry time %q", wrong)
	}
}

func TestOrderTransactionReplyMarkupOmitsExchangeButton(t *testing.T) {
	for _, tradeType := range []model.TradeType{model.UsdtOKX, model.UsdtBinance, model.UsdcOKX, model.UsdcBinance} {
		order := model.Order{TradeType: tradeType, RefHash: "exchange-transaction-id"}
		if markup := orderTransactionReplyMarkup(order, "details"); markup != nil {
			t.Fatalf("trade type %s unexpectedly produced reply markup: %+v", tradeType, markup)
		}
		if params := orderSendMessageParams("payment received", order, "details"); params.ReplyMarkup != nil {
			t.Fatalf("trade type %s unexpectedly set reply markup interface: %#v", tradeType, params.ReplyMarkup)
		}
	}
}

func TestOrderTransactionReplyMarkupUsesExplorerURL(t *testing.T) {
	order := model.Order{TradeType: model.UsdtPolygon, RefHash: "0xabc123"}
	markup := orderTransactionReplyMarkup(order, "details")
	if markup == nil || len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 1 {
		t.Fatalf("unexpected reply markup: %+v", markup)
	}
	button := markup.InlineKeyboard[0][0]
	if got, want := button.URL, "https://polygonscan.com/tx/0xabc123"; got != want {
		t.Fatalf("button URL = %q, want %q", got, want)
	}
	if button.CallbackData != "" {
		t.Fatalf("button callback data = %q, want empty", button.CallbackData)
	}
}
