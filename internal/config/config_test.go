package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"panel_url":"https://panel.example.com:11111","node_id":"node-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load 失败：%v", err)
	}
	if cfg.PanelURL != "https://panel.example.com:11111" || cfg.NodeID != "node-1" {
		t.Fatalf("基本字段不对：%+v", cfg)
	}
	// 没填的字段走默认值，不报错。
	if cfg.XrayAPIAddr != "127.0.0.1:10085" {
		t.Fatalf("Xray 地址默认值应为 127.0.0.1:10085，实际 %s", cfg.XrayAPIAddr)
	}
	if cfg.SnapshotInterval != 10*time.Second || cfg.PushInterval != 60*time.Second {
		t.Fatalf("快照/推送间隔默认值应为 10 秒/60 秒，实际 %v/%v", cfg.SnapshotInterval, cfg.PushInterval)
	}
	if cfg.LedgerPath != "/etc/node-agent/ledger.json" {
		t.Fatalf("账本路径默认值不对：%s", cfg.LedgerPath)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "no-such.json")); err == nil {
		t.Fatal("配置文件不存在时 Load 必须报错（不要悄悄用默认配置跑错地方）")
	}
}

func TestLoadBadJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{不是 json`), 0o600)
	if _, err := Load(path); err == nil {
		t.Fatal("配置文件格式不对时 Load 必须报错")
	}
}

func TestSaveRoundtripKeeps0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultConfig()
	cfg.PanelURL = "https://panel.example.com:11111"
	cfg.NodeID = "node-1"
	cfg.NodeToken = "rpt_exampletoken"
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save 失败：%v", err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("配置文件权限必须是 0600（含令牌），实际 %o", info.Mode().Perm())
	}
	back, err := Load(path)
	if err != nil {
		t.Fatalf("Save 后 Load 失败：%v", err)
	}
	if back.NodeToken != "rpt_exampletoken" {
		t.Fatal("Save 再 Load 后令牌丢了")
	}
}
