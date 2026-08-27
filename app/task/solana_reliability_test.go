package task

import (
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/tidwall/gjson"
	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/model"
)

func TestSolanaAdvanceSlotCursorPersistsLargeJump(t *testing.T) {
	s := newSolana()

	if _, _, ok := s.advanceSlotCursor(100, 10); ok {
		t.Fatal("large initial slot jump unexpectedly produced a scan range")
	}
	if s.lastSlotNum != 100 {
		t.Fatalf("last slot = %d, want 100 after skipping a large jump", s.lastSlotNum)
	}

	start, end, ok := s.advanceSlotCursor(103, 10)
	if !ok || start != 101 || end != 103 {
		t.Fatalf("next scan range = %d..%d, %t; want 101..103, true", start, end, ok)
	}
}

func TestSolanaTransactionSucceededRejectsMetaError(t *testing.T) {
	if solanaTransactionSucceeded(gjson.Parse(`{"meta":{"err":{"InstructionError":[1,"Custom"]}}}`)) {
		t.Fatal("failed Solana transaction reported success")
	}
	if !solanaTransactionSucceeded(gjson.Parse(`{"meta":{"err":null}}`)) {
		t.Fatal("successful Solana transaction was rejected")
	}
	if solanaTransactionSucceeded(gjson.Parse(`{"meta":{}}`)) {
		t.Fatal("transaction without an explicit meta.err result reported success")
	}
	s := newSolana()
	if transfers := s.parseTransaction(
		gjson.Parse(`{"meta":{"err":{"InstructionError":[1,"Custom"]}}}`),
		123,
		time.Unix(1, 0),
	); len(transfers) != 0 {
		t.Fatalf("failed transaction produced %d transfers", len(transfers))
	}
}

func TestSolanaVersionedTransactionUsesWritableBeforeReadonlyALTKeys(t *testing.T) {
	data := make([]byte, 9)
	data[0] = 3
	binary.LittleEndian.PutUint64(data[1:], 1_000_000)

	transaction := gjson.Parse(fmt.Sprintf(`{
		"transaction":{
			"signatures":["signature-1"],
			"message":{
				"accountKeys":["static-signer"],
				"instructions":[{
					"programIdIndex":3,
					"accounts":[1,2,0],
					"data":%q
				}]
			}
		},
		"meta":{
			"err":null,
			"loadedAddresses":{
				"writable":["source-token-account","destination-token-account"],
				"readonly":[%q]
			},
			"postTokenBalances":[
				{"accountIndex":1,"mint":%q,"programId":%q,"owner":"source-owner"},
				{"accountIndex":2,"mint":%q,"programId":%q,"owner":"destination-owner"}
			]
		}
	}`, base58.Encode(data), conf.SolSplToken, conf.UsdtSolana, conf.SolSplToken, conf.UsdtSolana, conf.SolSplToken))

	keys := solanaAccountKeys(transaction)
	wantKeys := []string{"static-signer", "source-token-account", "destination-token-account", conf.SolSplToken}
	if fmt.Sprint(keys) != fmt.Sprint(wantKeys) {
		t.Fatalf("account key order = %v, want %v", keys, wantKeys)
	}

	s := newSolana()
	transfers := s.parseTransaction(transaction, 123, time.Unix(456, 0))
	if len(transfers) != 1 {
		t.Fatalf("parsed transfers = %d, want 1", len(transfers))
	}
	got := transfers[0]
	if got.FromAddress != "source-owner" || got.RecvAddress != "destination-owner" || got.Amount.String() != "1" {
		t.Fatalf("parsed transfer = from:%q to:%q amount:%s", got.FromAddress, got.RecvAddress, got.Amount)
	}
	if got.TradeType != model.UsdtSolana || got.TxHash != "signature-1" || got.BlockNum != 123 {
		t.Fatalf("parsed transfer metadata = trade:%s hash:%q block:%d", got.TradeType, got.TxHash, got.BlockNum)
	}
}

func TestSolanaSignatureStatusRequiresSuccessAndFinality(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		finalized bool
		failed    bool
	}{
		{name: "missing", status: `null`},
		{name: "missing error field", status: `{"confirmationStatus":"finalized"}`},
		{name: "confirmed", status: `{"err":null,"confirmationStatus":"confirmed"}`},
		{name: "finalized", status: `{"err":null,"confirmationStatus":"finalized"}`, finalized: true},
		{name: "failed finalized", status: `{"err":{"InstructionError":[1,"Custom"]},"confirmationStatus":"finalized"}`, failed: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			finalized, failed := solanaSignatureStatus(gjson.Parse(test.status))
			if finalized != test.finalized || failed != test.failed {
				t.Fatalf("status outcome = finalized:%t failed:%t, want finalized:%t failed:%t", finalized, failed, test.finalized, test.failed)
			}
		})
	}
}
