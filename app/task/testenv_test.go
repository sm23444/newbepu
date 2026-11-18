package task

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tidwall/gjson"
	applog "github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
)

// 任务测试的公共环境：临时日志目录、临时 SQLite 和主网交易 fixture

func initTaskTestEnv(t *testing.T, kv map[model.ConfKey]string) {
	t.Helper()

	if err := applog.Init(filepath.Join(t.TempDir(), "logs")); err != nil {
		t.Fatalf("init log: %v", err)
	}
	t.Cleanup(applog.Close)

	if err := model.Init(filepath.Join(t.TempDir(), "task-test.db"), ""); err != nil {
		t.Fatalf("init db: %v", err)
	}
	db := model.Db
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	for k, v := range kv {
		model.Db.Model(&model.Conf{}).Where("k = ?", k).Update("v", v)
	}
	model.RefreshC()
}

func createTestOrder(t *testing.T, o model.Order, created time.Time) model.Order {
	t.Helper()

	at := model.Datetime(created)
	o.CreatedAt = &at
	o.UpdatedAt = &at
	if o.ConfirmedAt == nil {
		o.ConfirmedAt = &created
	}
	if err := model.Db.Create(&o).Error; err != nil {
		t.Fatalf("create order: %v", err)
	}

	return o
}

func getTestOrder(t *testing.T, tradeID string) model.Order {
	t.Helper()

	var o model.Order
	if err := model.Db.Where("trade_id = ?", tradeID).First(&o).Error; err != nil {
		t.Fatalf("get order: %v", err)
	}

	return o
}

// 回调在协程中执行；等待本地 HTTP 测试地址被安全策略拒绝后的尝试完成，避免测试清理数据库时仍有写入。
func waitOrderNotifyAttempt(t *testing.T, tradeID string) {
	t.Helper()

	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		order := getTestOrder(t, tradeID)
		if order.NotifyNum > 0 && order.NotifyClaimToken == "" {
			return
		}
	}
	t.Fatalf("order %s callback attempt not finished", tradeID)
}

// 主网交易，每个文件是只含这一笔交易的 getBlock 响应
func readSolanaFixture(t *testing.T, name string) []byte {
	t.Helper()

	body, err := os.ReadFile(filepath.Join("testdata", "solana", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	return body
}

// 返回 slotParse 写入 transferQueue 的转账
func drainTransferQueue() []transfer {
	var result []transfer
	for {
		select {
		case items := <-transferQueue.Out:
			result = append(result, items...)
		case <-time.After(200 * time.Millisecond):
			return result
		}
	}
}

func solanaFixtureSlot(block []byte) int {
	return int(gjson.GetBytes(block, "result.parentSlot").Int()) + 1
}
