// node-agent 是落地机上跑的静态 Go 程序：读 Xray 统计、按订阅者数流量、
// 推回面板；超量 / 到期时在落地删用户。
//
// 子命令（DEV-PLAN §3.2）：
//
//	version  打印版本号（与 CHANGELOG 一致）。
//	doctor   自检：配置文件能不能读、Xray 统计接口通不通。
//	run      跑起来：每 10 秒快照一次 QueryStats，算增量进账本。
//	join     入网：Phase 0 实现（需要面板的一次性入网令牌接口，
//	         见 docs/interface-audit.md 的缺失清单），阶段 0 先不实现。
//
// 阶段 0 的 run 只做快照 + 账本，不推送、不执法：
// 推送（Phase 0）要等接口审计的结论（panel-adapter 还是面板新接口），
// 执法（Phase 2）要等用户拍板 D2。不要在阶段 0 先把没定的部分写出来。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nbbk/zhexuexi-agent/internal/config"
	"github.com/nbbk/zhexuexi-agent/internal/ledger"
	"github.com/nbbk/zhexuexi-agent/internal/xray"
)

// Version 由 CHANGELOG 里的版本号改，发布时一起改。
// 不要写死在别的地方——面板按 agent_version 标红过旧的 agent，
// 读的就是这里输出的版本。
var Version = "v0.1.0-stage0"

const defaultConfigPath = "/etc/node-agent/config.json"

func main() {
	log.SetFlags(log.LstdFlags)
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version":
		fmt.Printf("node-agent %s\n", Version)
	case "doctor":
		if err := runDoctor(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "自检失败：%v\n", err)
			os.Exit(1)
		}
		fmt.Println("自检通过")
	case "run":
		if err := runAgent(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "agent 退出：%v\n", err)
			os.Exit(1)
		}
	case "join":
		fmt.Fprintln(os.Stderr, "join 未实现：入网要面板的一次性入网令牌接口（Phase 0 实现），先手动把配置写到 /etc/node-agent/config.json（600 权限），再跑 node-agent run。")
		os.Exit(2)
	default:
		fmt.Fprintf(os.Stderr, "不知道的子命令：%s\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "用法：node-agent <version|doctor|run|join> [选项]")
	fmt.Fprintln(os.Stderr, "  version              打印版本号")
	fmt.Fprintln(os.Stderr, "  doctor [--config 路径]  自检：配置文件 + Xray 统计接口")
	fmt.Fprintln(os.Stderr, "  run [--config 路径]     跑起来：每 10 秒快照一次 QueryStats，算增量进账本")
	fmt.Fprintln(os.Stderr, "  join                 入网（Phase 0 实现，阶段 0 未实现）")
}

// runDoctor 自检两件事：
//  1. 配置文件能不能读（读不到就报清楚，不要悄悄用默认配置跑错地方）；
//  2. Xray 统计接口通不通（QueryStats("inbound>>>") 能拿到计数器就算开）。
func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath, "配置文件路径")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	fmt.Printf("配置文件：%s（面板 %s，节点 %s）\n", *configPath, cfg.PanelURL, cfg.NodeID)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	stats, err := xray.Check(ctx, cfg.XrayAPIAddr)
	if err != nil {
		return err
	}
	fmt.Printf("Xray 统计接口已开（%s），inbound>>> 计数器 %d 条\n", cfg.XrayAPIAddr, len(stats))
	for _, s := range stats {
		fmt.Printf("  %s = %d\n", s.Name, s.Value)
	}
	return nil
}

// runAgent 快照循环：每 snapshot_interval 秒调一次 QueryStats，
// user>>> 的计数器按 email 归属到订阅者，inbound>>> 的记到总量行。
// 增量算进账本并落盘。收到 SIGINT/SIGTERM 时先落一次盘再退出，
// 不丢最后 10 秒的增量。
func runAgent(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath, "配置文件路径")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	led, err := ledger.Load(cfg.LedgerPath)
	if err != nil {
		return err
	}
	log.Printf("node-agent %s 启动：面板 %s，节点 %s，Xray API %s，快照每 %v，推送每 %v（阶段 0：推送未实现，只快照 + 账本）",
		Version, cfg.PanelURL, cfg.NodeID, cfg.XrayAPIAddr, cfg.SnapshotInterval, cfg.PushInterval)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ticker := time.NewTicker(cfg.SnapshotInterval)
	defer ticker.Stop()
	if err := snapshotOnce(ctx, cfg, led); err != nil {
		log.Printf("第一次快照失败：%v（继续跑，下次再试）", err)
	}
	for {
		select {
		case <-ctx.Done():
			log.Println("收到退出信号，先落一次盘再退出")
			if err := led.Save(); err != nil {
				return fmt.Errorf("退出时账本落盘失败：%w", err)
			}
			return nil
		case <-ticker.C:
			if err := snapshotOnce(ctx, cfg, led); err != nil {
				log.Printf("快照失败：%v（增量下次再算，基线不动，不丢数）", err)
			}
		}
	}
}

// snapshotOnce 一次快照：user>>> 按用户算增量，inbound>>> 记总量。
// 失败时返回错误、基线不动——下次快照用旧基线重算，增量不会丢，
// 只是晚 10 秒（DEV-PLAN §3.7：快照失败不推进基线）。
func snapshotOnce(ctx context.Context, cfg *config.Config, led *ledger.Ledger) error {
	cli, err := xray.Dial(ctx, cfg.XrayAPIAddr)
	if err != nil {
		return err
	}
	defer cli.Close()

	stats, err := cli.QueryStats(ctx, "user>>>", false)
	if err != nil {
		return err
	}
	// 计数器名：user>>><email>>>traffic>>>uplink|downlink。
	// email 是 Xray 里的计数钥匙（u-sub-<id>@node），阶段 0 还没有
	// 用户表（Phase 1 才拉），先按 email 原样记到账本里；
	// Phase 1 再按用户表把 email 映射到订阅标识。
	// periodID 暂用自然月占位，Phase 1 用户表下发后按面板的 period_id 为准。
	byEmail := map[string][2]int64{}
	for _, s := range stats {
		parts := strings.Split(s.Name, ">>>")
		if len(parts) != 4 || parts[0] != "user" || parts[2] != "traffic" {
			continue
		}
		cur := byEmail[parts[1]]
		switch parts[3] {
		case "uplink":
			cur[0] = s.Value
		case "downlink":
			cur[1] = s.Value
		}
		byEmail[parts[1]] = cur
	}
	periodID := time.Now().Format("2006-01")
	for email, v := range byEmail {
		up, down := led.ApplySnapshot(email, periodID, v[0], v[1])
		if up != 0 || down != 0 {
			log.Printf("快照：%s 本次 +%d/+%d 字节", email, up, down)
		}
	}

	// 入站总量：记到 generic@node 行，面板上先看“直连流量有多少”。
	total, err := cli.QueryStats(ctx, "inbound>>>", false)
	if err != nil {
		return err
	}
	var totalUp, totalDown int64
	for _, s := range total {
		if strings.HasSuffix(s.Name, ">>>uplink") {
			totalUp += s.Value
		} else if strings.HasSuffix(s.Name, ">>>downlink") {
			totalDown += s.Value
		}
	}
	led.ApplySnapshot("generic@node", periodID, totalUp, totalDown)
	return led.Save()
}
