package task

import (
	"testing"

	"github.com/v03413/tronprotocol/core"
)

func TestTronTransactionSucceededHandlesEmptyResult(t *testing.T) {
	if tronTransactionSucceeded(nil) {
		t.Fatal("nil transaction reported success")
	}
	if tronTransactionSucceeded(&core.Transaction{}) {
		t.Fatal("transaction without result reported success")
	}
}

func TestTronTransactionSucceededAcceptsSuccess(t *testing.T) {
	transaction := &core.Transaction{
		Ret: []*core.Transaction_Result{{ContractRet: core.Transaction_Result_SUCCESS}},
	}
	if !tronTransactionSucceeded(transaction) {
		t.Fatal("successful transaction was rejected")
	}
}
