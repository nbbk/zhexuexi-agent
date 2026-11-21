// Package xray 是 agent 读落地 Xray 统计的 gRPC 客户端。
//
// 只实现 StatsService 的两个方法（GetStats / QueryStats），对应节点侧
// 方案 §4.1。AlterInbound（HandlerService，增删用户）是 Phase 1 的
// 事，不在本阶段实现。
//
// 计量纪律（DEV-PLAN §3.7）：读数一律 reset=false + 持久基线，
// 绝不用 reset=true——读完清零，agent 读完还没落盘就崩溃，
// 那一窗口的流量永久丢失。基线规则在 internal/ledger 里实现。
package xray

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/nbbk/zhexuexi-agent/internal/xray/statspb"
)

// Stat 一条 Xray 计数器。名字形如：
//
//	user>>>u-sub-xxx@node>>>traffic>>>uplink
//	user>>>u-sub-xxx@node>>>traffic>>>downlink
//	inbound>>>tag>>>traffic>>>uplink
//
// 其中 user>>> 后面是 Xray 入站 clients 里每条用户的 email，
// agent 按 email 归属到订阅者（节点侧方案 §3.2）。
type Stat struct {
	Name  string
	Value int64
}

// Client 读 Xray 统计的最小接口。测试用 mock 实现替换，不依赖真 Xray。
type Client interface {
	// QueryStats 按模式查全部计数器。pattern 是 Xray 侧的前缀匹配，
	// 常用 "user>>>"（全部用户）与 "inbound>>>"（入站总量）。
	// reset 永远传 false（见包注释的计量纪律）。
	QueryStats(ctx context.Context, pattern string, reset bool) ([]Stat, error)
	// GetStats 精确查一条计数器。
	GetStats(ctx context.Context, name string, reset bool) (Stat, error)
	Close() error
}

// statsServiceClient 是手写的 StatsService gRPC stub，
// 与 protoc-gen-go-grpc 生成的代码行为一致（方法名与路径相同）。
type statsServiceClient struct {
	cc grpc.ClientConnInterface
}

func (c *statsServiceClient) GetStats(ctx context.Context, in *statspb.GetStatsRequest, opts ...grpc.CallOption) (*statspb.GetStatsResponse, error) {
	out := new(statspb.GetStatsResponse)
	err := c.cc.Invoke(ctx, "/xray.app.stats.command.StatsService/GetStats", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *statsServiceClient) QueryStats(ctx context.Context, in *statspb.QueryStatsRequest, opts ...grpc.CallOption) (*statspb.QueryStatsResponse, error) {
	out := new(statspb.QueryStatsResponse)
	err := c.cc.Invoke(ctx, "/xray.app.stats.command.StatsService/QueryStats", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GRPCClient 是基于真实 gRPC 连接的 Client 实现。
type GRPCClient struct {
	conn   *grpc.ClientConn
	client *statsServiceClient
}

// Dial 连上落地 Xray 的 dokodemo-door API 入站（默认 127.0.0.1:10085）。
// 连接是明文的：API 入站只绑 loopback，绝不暴露公网（节点侧方案 §2.2）。
// ctx 超时后返回错误，调用方不要无限重试——doctor 与 run 各自决定重试策略。
func Dial(ctx context.Context, addr string) (*GRPCClient, error) {
	if addr == "" {
		return nil, fmt.Errorf("xray api 地址为空（应为 127.0.0.1:10085）")
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("连 Xray API %s 失败：%w", addr, err)
	}
	return &GRPCClient{conn: conn, client: &statsServiceClient{cc: conn}}, nil
}

// QueryStats 按模式查全部计数器，reset 永远传 false（计量纪律）。
func (c *GRPCClient) QueryStats(ctx context.Context, pattern string, reset bool) ([]Stat, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := c.client.QueryStats(ctx, &statspb.QueryStatsRequest{Pattern: pattern, Reset_: reset})
	if err != nil {
		return nil, fmt.Errorf("QueryStats(%q) 失败：%w", pattern, err)
	}
	stats := make([]Stat, 0, len(resp.Stat))
	for _, s := range resp.Stat {
		stats = append(stats, Stat{Name: s.Name, Value: s.Value})
	}
	return stats, nil
}

// GetStats 精确查一条计数器。计数器不存在时返回空 Stat，不报错——
// 调用方（自检 / 对账）把“空”当作“这条计数器还没产生”，
// 而不是当作接口故障；接口本身的故障（连不上、超时）才返回错误。
func (c *GRPCClient) GetStats(ctx context.Context, name string, reset bool) (Stat, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := c.client.GetStats(ctx, &statspb.GetStatsRequest{Name: name, Reset_: reset})
	if err != nil {
		return Stat{}, fmt.Errorf("GetStats(%q) 失败：%w", name, err)
	}
	if resp.Stat == nil {
		return Stat{}, nil
	}
	return Stat{Name: resp.Stat.Name, Value: resp.Stat.Value}, nil
}

// Close 关闭 gRPC 连接。
func (c *GRPCClient) Close() error {
	return c.conn.Close()
}

// Check 连通性自检：Dial 后调一次 QueryStats("inbound>>>")，
// 能拿到计数器（哪怕是空列表）就算 Xray 统计接口已开。
// doctor 子命令调这个，失败时返回的错误里写清是“连不上”还是“接口没开”。
func Check(ctx context.Context, addr string) ([]Stat, error) {
	cli, err := Dial(ctx, addr)
	if err != nil {
		return nil, err
	}
	defer cli.Close()
	stats, err := cli.QueryStats(ctx, "inbound>>>", false)
	if err != nil {
		return nil, fmt.Errorf("Xray 统计接口没开或不可用（%s）：%w", addr, err)
	}
	return stats, nil
}
