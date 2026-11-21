// Package ledger 是 agent 的本地账本：每用户的本期用量、基线、待推送增量。
//
// 计量纪律（DEV-PLAN §3.7，节点侧方案 §4.2），逐条实现在这里，
// 不要只记在方案里：
//
//  1. 读数用 reset=false + 持久基线。增量 = max(0, 本次累计 − 基线)。
//     计数器倒退（本次累计 < 基线）说明 Xray 重启过：增量按 0 算，
//     只把基线重定为当前累计，绝不把整个当前累计重加一遍。
//  2. 推送先暂存、面板 ACK 后再清、失败加回。快照的增量先进待推送区
//     （Pending），agent 把增量 POST 给面板，面板 ACK 后才清暂存；
//     失败（超时 / 非 2xx）暂存留着，下次再推。绝不“先清零再推”。
//  3. 去重在面板侧做：每次推送带 (user_id, period_id, seq)，
//     seq 每用户每周期递增；重推的增量 seq 相同则面板丢弃。
//  4. 翻周期幂等：last_reset_at compare-and-set，时钟回拨不重复翻。
//
// 账本文件默认 /etc/node-agent/ledger.json，原子写（写临时文件再
// rename），文件权限 0600——里面有每用户的用量，丢了可以从面板
// 对账重建，但不要让别的用户读到。
package ledger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// UserUsage 一行用户用量。Up/Down 是本期已用（上行/下行，负载字节）；
// UpTotal/DownTotal 是该用户历史上累计（面板对账用）；BaselineUp/
// BaselineDown 是 Xray 计数器的基线（上次快照读到的累计值）；
// Seq 是本周期已推送的序号（每次推送成功 +1，面板按它去重）。
type UserUsage struct {
	UserID       string `json:"user_id"`
	PeriodID     string `json:"period_id"`
	Up           int64  `json:"up"`
	Down         int64  `json:"down"`
	UpTotal      int64  `json:"up_total"`
	DownTotal    int64  `json:"down_total"`
	BaselineUp   int64  `json:"baseline_up"`
	BaselineDown int64  `json:"baseline_down"`
	Seq          uint64 `json:"seq"`
	NextResetAt  string `json:"next_reset_at,omitempty"`
	LastResetAt  string `json:"last_reset_at,omitempty"`
}

// PendingDelta 一条待推送的增量：快照算出来、还没被面板 ACK 的。
// 面板 ACK 后 ConfirmPending 清掉；推送失败时留着，下次再推。
type PendingDelta struct {
	UserID   string `json:"user_id"`
	PeriodID string `json:"period_id"`
	Seq      uint64 `json:"seq"`
	Up       int64  `json:"up"`
	Down     int64  `json:"down"`
}

// Ledger 账本：内存里算、落盘里存。所有方法都加锁，
// run 循环的快照 goroutine 与推送 goroutine 可以同时调。
type Ledger struct {
	mu      sync.Mutex
	path    string
	Users   map[string]*UserUsage   `json:"users"`
	Pending map[string]PendingDelta `json:"pending"`
}

// New 建一个空账本（不读盘）。读盘用 Load。
func New(path string) *Ledger {
	return &Ledger{path: path, Users: map[string]*UserUsage{}, Pending: map[string]PendingDelta{}}
}

// Load 从文件读账本。文件不存在时返回空账本（第一次跑），不报错。
func Load(path string) (*Ledger, error) {
	l := New(path)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil
		}
		return nil, fmt.Errorf("读账本 %s 失败：%w", path, err)
	}
	if err := json.Unmarshal(b, l); err != nil {
		return nil, fmt.Errorf("账本 %s 格式不对：%w", path, err)
	}
	if l.Users == nil {
		l.Users = map[string]*UserUsage{}
	}
	if l.Pending == nil {
		l.Pending = map[string]PendingDelta{}
	}
	return l, nil
}

// Save 原子写账本：先写临时文件（同目录，0600），再 rename 覆盖。
// 写一半崩溃也不会留半截账本——rename 是原子的。
func (l *Ledger) Save() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.saveLocked()
}

func (l *Ledger) saveLocked() error {
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("账本序列化失败：%w", err)
	}
	dir := filepath.Dir(l.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("建账本目录 %s 失败：%w", dir, err)
	}
	tmp := filepath.Join(dir, ".ledger.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("写账本临时文件失败：%w", err)
	}
	if err := os.Rename(tmp, l.path); err != nil {
		return fmt.Errorf("账本落盘失败：%w", err)
	}
	return nil
}

// ApplySnapshot 把一次 QueryStats 快照算进账本，返回本次增量。
// userID 是订阅标识（如 "subuser:su-xxx"）；up/down 是 Xray 计数器
// 的本次累计值（uplink/downlink）。基线规则见包注释第 1 条。
// 增量同时累加进本期用量、历史累计与待推送区。
func (l *Ledger) ApplySnapshot(userID, periodID string, up, down int64) (upDelta, downDelta int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	u, ok := l.Users[userID]
	if !ok {
		u = &UserUsage{UserID: userID, PeriodID: periodID}
		l.Users[userID] = u
	}
	// 周期变了（面板下发了新 period_id）：本期用量清零，基线重定，
	// 待推送区里旧周期的增量丢弃（旧周期已结算，不再推）。
	if u.PeriodID != periodID {
		u.PeriodID = periodID
		u.Up, u.Down = 0, 0
		u.BaselineUp, u.BaselineDown = up, down
		u.Seq = 0
		delete(l.Pending, userID)
		return 0, 0
	}
	upDelta = delta(u.BaselineUp, up)
	downDelta = delta(u.BaselineDown, down)
	u.BaselineUp, u.BaselineDown = up, down
	if upDelta == 0 && downDelta == 0 {
		return 0, 0
	}
	u.Up += upDelta
	u.Down += downDelta
	u.UpTotal += upDelta
	u.DownTotal += downDelta
	p := l.Pending[userID]
	p.UserID = userID
	p.PeriodID = periodID
	p.Up += upDelta
	p.Down += downDelta
	l.Pending[userID] = p
	return upDelta, downDelta
}

// delta 基线规则：本次累计 >= 基线时增量是差值；
// 本次累计 < 基线时 Xray 重启过，增量按 0 算（调用方把基线重定为当前累计）。
func delta(baseline, current int64) int64 {
	if current < baseline {
		return 0
	}
	return current - baseline
}

// TakePending 取出全部待推送增量（推送时调）。取出的每条带上
// 下一个 seq（Seq+1），面板按 (user_id, period_id, seq) 去重。
// 取出后暂存不删除，等 ConfirmPending 才清——推送失败时还在。
func (l *Ledger) TakePending() []PendingDelta {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]PendingDelta, 0, len(l.Pending))
	for _, p := range l.Pending {
		if p.Up == 0 && p.Down == 0 {
			continue
		}
		u := l.Users[p.UserID]
		seq := uint64(1)
		if u != nil {
			seq = u.Seq + 1
		}
		p.Seq = seq
		out = append(out, p)
	}
	return out
}

// ConfirmPending 面板 ACK 后清暂存（推送成功时调）。
// 只清 seq 对得上的那条：期间又有新快照进来时，Pending 里是新
// 的增量（seq 还是旧值），不能误删。
func (l *Ledger) ConfirmPending(confirmed []PendingDelta) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range confirmed {
		p, ok := l.Pending[c.UserID]
		if !ok || p.PeriodID != c.PeriodID {
			continue
		}
		// 这次确认的增量已经发出去了：从待推送里减掉它。
		// 减完为 0 就删掉这条；期间新来的增量留着。
		p.Up -= c.Up
		p.Down -= c.Down
		if p.Up <= 0 && p.Down <= 0 {
			delete(l.Pending, c.UserID)
		} else {
			l.Pending[c.UserID] = p
		}
		if u := l.Users[c.UserID]; u != nil && c.Seq > u.Seq {
			u.Seq = c.Seq
		}
	}
}

// MaybeReset 翻周期：now >= nextResetAt 且 lastResetAt 不是本次
// nextResetAt 时，把本期用量清零、Seq 清零，记 lastResetAt。
// 返回 true 表示翻了，false 表示没到时间或已经翻过（幂等）。
// nextResetAt 由面板算好下发，agent 只执行，不自己算重置日。
func (l *Ledger) MaybeReset(userID string, now time.Time, nextResetAt time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	u, ok := l.Users[userID]
	if !ok {
		return false
	}
	if now.Before(nextResetAt) {
		return false
	}
	stamp := nextResetAt.UTC().Format(time.RFC3339)
	if u.LastResetAt == stamp {
		return false // 已经翻过，时钟回拨也不重复翻
	}
	u.Up, u.Down = 0, 0
	u.Seq = 0
	u.LastResetAt = stamp
	u.NextResetAt = ""
	delete(l.Pending, userID)
	return true
}

// Get 读一行用量（测试与 run 循环的日志用），不存在返回 nil。
func (l *Ledger) Get(userID string) *UserUsage {
	l.mu.Lock()
	defer l.mu.Unlock()
	u := l.Users[userID]
	if u == nil {
		return nil
	}
	cp := *u
	return &cp
}
