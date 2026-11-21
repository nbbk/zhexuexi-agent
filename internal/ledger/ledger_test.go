package ledger

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBaselineRegressionNotDoubleCounted(t *testing.T) {
	// 节点侧方案 §4.2 的核心规则：Xray 重启后计数器归零，
	// 本次累计 < 基线时增量按 0 算，绝不把整个当前累计重加一遍。
	l := New(filepath.Join(t.TempDir(), "ledger.json"))
	up, down := l.ApplySnapshot("subuser:su-1", "2026-11", 1000, 2000)
	if up != 1000 || down != 2000 {
		t.Fatalf("第一次快照应全算增量，实际 up=%d down=%d", up, down)
	}
	// 正常涨 500。
	up, _ = l.ApplySnapshot("subuser:su-1", "2026-11", 1500, 2000)
	if up != 500 {
		t.Fatalf("正常增量应为 500，实际 %d", up)
	}
	// Xray 重启：计数器归零后又涨到 300（< 基线 1500）。
	up, down = l.ApplySnapshot("subuser:su-1", "2026-11", 300, 400)
	if up != 0 || down != 0 {
		t.Fatalf("计数器倒退时增量必须按 0 算，实际 up=%d down=%d", up, down)
	}
	u := l.Get("subuser:su-1")
	if u.Up != 1500 || u.Down != 2000 {
		t.Fatalf("倒退后本期用量必须不变（1500/2000），实际 %+v", u)
	}
	// 重启后继续涨：300 → 800，增量是 500，不是从 0 重加。
	up, _ = l.ApplySnapshot("subuser:su-1", "2026-11", 800, 400)
	if up != 500 {
		t.Fatalf("重启后继续涨的增量应为 500，实际 %d", up)
	}
	if l.Get("subuser:su-1").Up != 2000 {
		t.Fatalf("本期用量应为 2000，实际 %d", l.Get("subuser:su-1").Up)
	}
}

func TestPendingAckAndFailure(t *testing.T) {
	// 推送先暂存、ACK 后再清、失败加回（DEV-PLAN §3.7 第 2 条）。
	l := New(filepath.Join(t.TempDir(), "ledger.json"))
	l.ApplySnapshot("subuser:su-1", "2026-11", 1000, 500)

	pending := l.TakePending()
	if len(pending) != 1 || pending[0].Up != 1000 || pending[0].Down != 500 {
		t.Fatalf("TakePending 应取出 1000/500，实际 %v", pending)
	}
	if pending[0].Seq != 1 {
		t.Fatalf("第一次推送的 seq 应为 1，实际 %d", pending[0].Seq)
	}
	// 推送失败：不调 ConfirmPending，暂存还在。
	if len(l.TakePending()) != 1 {
		t.Fatal("推送失败时暂存必须还在，下次再推")
	}
	// 期间又涨 200：暂存累加到 1200。
	l.ApplySnapshot("subuser:su-1", "2026-11", 1200, 500)
	pending = l.TakePending()
	if len(pending) != 1 || pending[0].Up != 1200 {
		t.Fatalf("失败后暂存应累加到 1200，实际 %v", pending)
	}
	// 面板 ACK：清暂存，seq 记到 1。
	l.ConfirmPending(pending)
	if len(l.TakePending()) != 0 {
		t.Fatal("ACK 后暂存必须清空")
	}
	if l.Get("subuser:su-1").Seq != 1 {
		t.Fatalf("ACK 后 Seq 应记为 1，实际 %d", l.Get("subuser:su-1").Seq)
	}
	// 再涨 100：下一次推送的 seq 是 2。
	l.ApplySnapshot("subuser:su-1", "2026-11", 1300, 500)
	if l.TakePending()[0].Seq != 2 {
		t.Fatal("第二次推送的 seq 应为 2")
	}
}

func TestMaybeResetIdempotent(t *testing.T) {
	// 翻周期幂等：同一 nextResetAt 只翻一次，时钟回拨也不重复翻。
	l := New(filepath.Join(t.TempDir(), "ledger.json"))
	l.ApplySnapshot("subuser:su-1", "2026-11", 1000, 500)
	resetAt := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

	if l.MaybeReset("subuser:su-1", time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC), resetAt) {
		t.Fatal("没到重置时间不能翻")
	}
	if !l.MaybeReset("subuser:su-1", resetAt, resetAt) {
		t.Fatal("到了重置时间必须翻")
	}
	u := l.Get("subuser:su-1")
	if u.Up != 0 || u.Down != 0 || u.Seq != 0 {
		t.Fatalf("翻周期后本期用量与 Seq 必须清零，实际 %+v", u)
	}
	// 同一个 nextResetAt 再调一次：不重复翻。
	if l.MaybeReset("subuser:su-1", resetAt.Add(time.Hour), resetAt) {
		t.Fatal("同一重置点重复调必须返回 false，不重复翻")
	}
}

func TestPeriodChangeResetsBaseline(t *testing.T) {
	l := New(filepath.Join(t.TempDir(), "ledger.json"))
	l.ApplySnapshot("subuser:su-1", "2026-11", 1000, 500)
	// 面板下发了新周期：本期清零，基线重定为当前累计，不算增量。
	up, down := l.ApplySnapshot("subuser:su-1", "2026-12", 1000, 500)
	if up != 0 || down != 0 {
		t.Fatalf("换周期时增量必须为 0，实际 %d/%d", up, down)
	}
	if l.Get("subuser:su-1").Up != 0 {
		t.Fatal("换周期后本期用量必须清零")
	}
	up, _ = l.ApplySnapshot("subuser:su-1", "2026-12", 1500, 500)
	if up != 500 {
		t.Fatalf("换周期后增量应从新基线算（500），实际 %d", up)
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	l := New(path)
	l.ApplySnapshot("subuser:su-1", "2026-11", 1000, 500)
	l.ApplySnapshot("subuser:su-2", "2026-11", 300, 700)
	if err := l.Save(); err != nil {
		t.Fatalf("Save 失败：%v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("账本文件不存在：%v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("账本文件权限必须是 0600，实际 %o", info.Mode().Perm())
	}
	l2, err := Load(path)
	if err != nil {
		t.Fatalf("Load 失败：%v", err)
	}
	if l2.Get("subuser:su-1").Up != 1000 || l2.Get("subuser:su-2").Down != 700 {
		t.Fatalf("落盘再读回的用量不对：%+v %+v", l2.Get("subuser:su-1"), l2.Get("subuser:su-2"))
	}
	if len(l2.TakePending()) != 2 {
		t.Fatal("落盘再读回的待推送增量应还在（没 ACK 之前不丢）")
	}
}

func TestLoadMissingFile(t *testing.T) {
	l, err := Load(filepath.Join(t.TempDir(), "no-such.json"))
	if err != nil {
		t.Fatalf("账本文件不存在时 Load 应返回空账本、不报错：%v", err)
	}
	if len(l.Users) != 0 {
		t.Fatal("空账本不应有用户")
	}
}
