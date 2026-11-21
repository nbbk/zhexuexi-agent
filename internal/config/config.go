// Package config 读 agent 的配置文件。
//
// 配置文件默认 /etc/node-agent/config.json（600 权限，里面有节点令牌）。
// 字段新增一律带默认值，旧配置不改也能直接读（DEV-PLAN §10.3 向前兼容）。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Config agent 的运行配置。PanelURL 是要对接的面板地址
// （如 https://panel.example.com:11111，示例用占位域名）；
// NodeID 是面板给这台落地编的号；NodeToken 是入网后得到的长期
// 节点令牌（join 时写进配置，绝不进仓库、不打日志）；
// XrayAPIAddr 是落地 Xray 的 dokodemo-door API 入站地址。
type Config struct {
	PanelURL         string        `json:"panel_url"`
	NodeID           string        `json:"node_id"`
	NodeToken        string        `json:"node_token,omitempty"`
	XrayAPIAddr      string        `json:"xray_api_addr"`
	ConfigPath       string        `json:"-"`
	LedgerPath       string        `json:"ledger_path"`
	SnapshotInterval time.Duration `json:"snapshot_interval_seconds"`
	PushInterval     time.Duration `json:"push_interval_seconds"`
}

// DefaultConfig 默认值：快照 10 秒、推送 60 秒（DEV-PLAN §3.7 第 5 条），
// Xray API 默认 127.0.0.1:10085（只绑 loopback）。
func DefaultConfig() *Config {
	return &Config{
		XrayAPIAddr:      "127.0.0.1:10085",
		LedgerPath:       "/etc/node-agent/ledger.json",
		SnapshotInterval: 10 * time.Second,
		PushInterval:     60 * time.Second,
	}
}

// Load 读配置文件。文件不存在时返回错误——
// agent 没有配置就不知道往哪个面板报、读哪台 Xray，
// 宁可启动失败报清楚，也不要用默认配置悄悄跑错地方。
// JSON 里的秒数字段用 time.Duration 的纳秒值容易写错，
// 所以 snapshot_interval_seconds / push_interval_seconds
// 在文件里是“秒”这个整数，Load 时再换算。
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读配置文件 %s 失败：%w（先跑 node-agent join 入网，或手动写配置）", path, err)
	}
	var file struct {
		PanelURL         string `json:"panel_url"`
		NodeID           string `json:"node_id"`
		NodeToken        string `json:"node_token"`
		XrayAPIAddr      string `json:"xray_api_addr"`
		LedgerPath       string `json:"ledger_path"`
		SnapshotInterval int64  `json:"snapshot_interval_seconds"`
		PushInterval     int64  `json:"push_interval_seconds"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("配置文件 %s 格式不对：%w", path, err)
	}
	cfg := DefaultConfig()
	cfg.ConfigPath = path
	if file.PanelURL != "" {
		cfg.PanelURL = file.PanelURL
	}
	if file.NodeID != "" {
		cfg.NodeID = file.NodeID
	}
	cfg.NodeToken = file.NodeToken
	if file.XrayAPIAddr != "" {
		cfg.XrayAPIAddr = file.XrayAPIAddr
	}
	if file.LedgerPath != "" {
		cfg.LedgerPath = file.LedgerPath
	}
	if file.SnapshotInterval > 0 {
		cfg.SnapshotInterval = time.Duration(file.SnapshotInterval) * time.Second
	}
	if file.PushInterval > 0 {
		cfg.PushInterval = time.Duration(file.PushInterval) * time.Second
	}
	return cfg, nil
}

// Save 把配置写回文件（0600，含令牌）。join 子命令在 Phase 0 实现后用它。
func (c *Config) Save(path string) error {
	out := map[string]any{
		"panel_url":                 c.PanelURL,
		"node_id":                   c.NodeID,
		"node_token":                c.NodeToken,
		"xray_api_addr":             c.XrayAPIAddr,
		"ledger_path":               c.LedgerPath,
		"snapshot_interval_seconds": int64(c.SnapshotInterval / time.Second),
		"push_interval_seconds":     int64(c.PushInterval / time.Second),
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("配置序列化失败：%w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("写配置文件 %s 失败：%w", path, err)
	}
	return nil
}
