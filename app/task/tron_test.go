package task

import (
	"testing"
	"time"

	"github.com/v03413/tronprotocol/api"
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

func TestTronBlockTransactionSucceededChecksExecutionResult(t *testing.T) {
	tests := []struct {
		name       string
		apiSuccess bool
		contract   core.Transaction_ResultContractResult
		want       bool
	}{
		{name: "successful execution", apiSuccess: true, contract: core.Transaction_Result_SUCCESS, want: true},
		{name: "failed execution", apiSuccess: true, contract: core.Transaction_Result_REVERT, want: false},
		{name: "rpc result failed", apiSuccess: false, contract: core.Transaction_Result_SUCCESS, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trans := &api.TransactionExtention{
				Result: &api.Return{Result: tt.apiSuccess},
				Transaction: &core.Transaction{
					Ret: []*core.Transaction_Result{{ContractRet: tt.contract}},
				},
			}
			if got := tronBlockTransactionSucceeded(trans); got != tt.want {
				t.Fatalf("tronBlockTransactionSucceeded() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTronRetryContinuesAfterFiveAttempts(t *testing.T) {
	setEVMTestTaskLogger(t)
	tr := newTron()
	tr.retryAttempts[123] = 5
	tr.scheduleBlockRetry(tronBlock{Number: 123}, time.Hour)

	if got := tr.retryAttempts[123]; got != 6 {
		t.Fatalf("retry attempt = %d, want 6", got)
	}
	timer, ok := tr.retryScheduled[123]
	if !ok {
		t.Fatal("retry timer was not scheduled after five attempts")
	}
	timer.Stop()
}
