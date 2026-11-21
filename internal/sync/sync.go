// Package sync 是 agent 与面板之间的数据同步：
// 拉用户表（Phase 1）与推送增量（Phase 0）。
//
// 本阶段（阶段 0）只定数据形状，不实现 HTTP 调用——
// 原因见 docs/interface-audit.md：面板 1.3.29 里没有 agent 能用的
// 用户表下发接口，也没有流量接收接口（缺失清单 D10），
// 先用 panel-adapter 转接的方案还没定下来之前，
// 不要先写一套对着不存在的接口的调用代码。
// Phase 0 实现推送时，先对着本包的 PushRequest 形状写，
// 再由 panel-adapter 或面板新接口接住。
package sync

import "fmt"

// UserRow 面板下发的一行用户（Phase 1 拉取时用）。
// Key 是订阅标识（"subuser:su-xxx" 或 "merged:ms-xxx"，与面板
// 的 userSubKey 同格式）；NodeUUID 是这台落地上的每人 UUID；
// Email 是 Xray 里的计数钥匙（u-sub-<id>@node）；
// LimitBytes <= 0 表示不限流；ExpiresAt 空表示永不过期；
// Disabled 为 true 时 agent 要把该用户从 Xray 里删掉。
type UserRow struct {
	Key         string `json:"key"`
	NodeUUID    string `json:"node_uuid"`
	Email       string `json:"email"`
	LimitBytes  int64  `json:"limit_bytes"`
	LimitMode   string `json:"limit_mode"`
	ResetMode   string `json:"reset_mode"`
	ResetDay    int    `json:"reset_day"`
	NextResetAt string `json:"next_reset_at"`
	ExpiresAt   string `json:"expires_at"`
	Disabled    bool   `json:"disabled"`
}

// PushRecord 一条推送的增量（Phase 0 推送时用）。
// 面板按 (UserID, PeriodID, Seq) 去重：重推的增量 Seq 相同则丢弃。
type PushRecord struct {
	UserID   string `json:"user_id"`
	PeriodID string `json:"period_id"`
	Seq      uint64 `json:"seq"`
	Up       int64  `json:"up"`
	Down     int64  `json:"down"`
}

// PushRequest 一次推送的包。AgentVersion 让面板按版本决定
// 下发字段（DEV-PLAN §10.3 版本协商）；NodeID 标识哪台落地。
type PushRequest struct {
	NodeID       string       `json:"node_id"`
	AgentVersion string       `json:"agent_version"`
	Records      []PushRecord `json:"records"`
}

// DedupKey 去重键：面板侧按它判重（DEV-PLAN §3.7 第 3 条）。
func (r PushRecord) DedupKey() string {
	return fmt.Sprintf("%s/%s/%d", r.UserID, r.PeriodID, r.Seq)
}

// Validate 校验一行用户表：Key 与 Email 缺一不可，
// NodeUUID 缺了就没法在 Xray 里建用户，LimitBytes 不能为负。
func (u UserRow) Validate() error {
	if u.Key == "" {
		return fmt.Errorf("用户表一行缺 key")
	}
	if u.Email == "" {
		return fmt.Errorf("用户 %s 缺 email（Xray 计数钥匙）", u.Key)
	}
	if u.NodeUUID == "" {
		return fmt.Errorf("用户 %s 缺 node_uuid（没法在 Xray 里建用户）", u.Key)
	}
	if u.LimitBytes < 0 {
		return fmt.Errorf("用户 %s 的流量上限不能为负", u.Key)
	}
	return nil
}
