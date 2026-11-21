package xray

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/nbbk/zhexuexi-agent/internal/xray/statspb"
)

// mockStatsServer 是内存里的假 Xray StatsService：
// 预置几条计数器，QueryStats 按前缀匹配返回，GetStats 精确查一条。
// 用途：验证 agent 的 gRPC 客户端 wiring（dial、调用、解析）——
// 不依赖真 Xray、不依赖 docker。真 Xray 的实测在测试落地做（见
// docs/interface-audit.md 的未核实项），不在这里假装测过。
type mockStatsServer struct {
	statspb.UnimplementedStatsServiceServer
	counters map[string]int64
	// lastReset 记录最近一次 QueryStats/GetStats 收到的 reset 值，
	// 测“客户端永远传 reset=false”这条计量纪律。
	lastReset *bool
}

func (m *mockStatsServer) GetStats(ctx context.Context, req *statspb.GetStatsRequest) (*statspb.GetStatsResponse, error) {
	r := req.GetReset_()
	m.lastReset = &r
	v, ok := m.counters[req.Name]
	if !ok {
		return &statspb.GetStatsResponse{}, nil
	}
	return &statspb.GetStatsResponse{Stat: &statspb.Stat{Name: req.Name, Value: v}}, nil
}

func (m *mockStatsServer) QueryStats(ctx context.Context, req *statspb.QueryStatsRequest) (*statspb.QueryStatsResponse, error) {
	r := req.GetReset_()
	m.lastReset = &r
	resp := &statspb.QueryStatsResponse{}
	for name, v := range m.counters {
		if strings.HasPrefix(name, req.Pattern) {
			resp.Stat = append(resp.Stat, &statspb.Stat{Name: name, Value: v})
		}
	}
	return resp, nil
}

// startMockServer 在 bufconn 上起一个假 StatsService，返回 dial 用的客户端。
func startMockServer(t *testing.T, counters map[string]int64) (*mockStatsServer, *GRPCClient) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	mock := &mockStatsServer{counters: counters}
	statspb.RegisterStatsServiceServer(srv, mock)
	go func() {
		if err := srv.Serve(lis); err != nil {
			t.Logf("mock server 停了：%v", err)
		}
	}()
	t.Cleanup(func() { srv.Stop() })

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("连 mock server 失败：%v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return mock, &GRPCClient{conn: conn, client: &statsServiceClient{cc: conn}}
}

func TestQueryStatsUserPattern(t *testing.T) {
	counters := map[string]int64{
		"user>>>u-sub-1@node>>>traffic>>>uplink":   5000,
		"user>>>u-sub-1@node>>>traffic>>>downlink": 12000,
		"user>>>u-sub-2@node>>>traffic>>>uplink":   0,
		"inbound>>>tag1>>>traffic>>>uplink":        5000,
		"inbound>>>tag1>>>traffic>>>downlink":      12000,
	}
	_, cli := startMockServer(t, counters)

	stats, err := cli.QueryStats(context.Background(), "user>>>", false)
	if err != nil {
		t.Fatalf("QueryStats 失败：%v", err)
	}
	if len(stats) != 3 {
		t.Fatalf("user>>> 应返回 3 条，实际 %d 条：%v", len(stats), stats)
	}
	got := map[string]int64{}
	for _, s := range stats {
		got[s.Name] = s.Value
	}
	if got["user>>>u-sub-1@node>>>traffic>>>downlink"] != 12000 {
		t.Fatalf("下行计数器不对：%v", got)
	}

	// 入站总量模式只返回 inbound>>> 的两条。
	stats, err = cli.QueryStats(context.Background(), "inbound>>>", false)
	if err != nil {
		t.Fatalf("QueryStats(inbound) 失败：%v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("inbound>>> 应返回 2 条，实际 %d 条", len(stats))
	}
}

func TestQueryStatsResetAlwaysFalse(t *testing.T) {
	mock, cli := startMockServer(t, map[string]int64{
		"inbound>>>tag1>>>traffic>>>uplink": 100,
	})
	if _, err := cli.QueryStats(context.Background(), "inbound>>>", false); err != nil {
		t.Fatalf("QueryStats 失败：%v", err)
	}
	if mock.lastReset == nil || *mock.lastReset {
		t.Fatalf("客户端传给 Xray 的 reset 必须是 false，实际 %v", mock.lastReset)
	}
	if _, err := cli.GetStats(context.Background(), "inbound>>>tag1>>>traffic>>>uplink", false); err != nil {
		t.Fatalf("GetStats 失败：%v", err)
	}
	if mock.lastReset == nil || *mock.lastReset {
		t.Fatalf("GetStats 传给 Xray 的 reset 必须是 false，实际 %v", mock.lastReset)
	}
}

func TestGetStatsMissingCounter(t *testing.T) {
	_, cli := startMockServer(t, map[string]int64{})
	s, err := cli.GetStats(context.Background(), "user>>>nobody@node>>>traffic>>>uplink", false)
	if err != nil {
		t.Fatalf("查不到的计数器不应报错，只返回空：%v", err)
	}
	if s.Name != "" || s.Value != 0 {
		t.Fatalf("查不到的计数器应返回空 Stat，实际 %+v", s)
	}
}

func TestDialEmptyAddr(t *testing.T) {
	if _, err := Dial(context.Background(), ""); err == nil {
		t.Fatal("地址为空时 Dial 必须报错")
	}
}

func TestDialUnreachable(t *testing.T) {
	// 连一个没人在听的 loopback 端口：Dial 本身不建连（grpc.NewClient
	// 是懒连接），QueryStats 必须在超时内失败，而不是一直挂住。
	cli, err := Dial(context.Background(), "127.0.0.1:1")
	if err != nil {
		t.Fatalf("Dial 不应立刻报错（懒连接）：%v", err)
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = cli.QueryStats(ctx, "inbound>>>", false)
	if err == nil {
		t.Fatal("连不上 Xray 时 QueryStats 必须报错")
	}
	t.Logf("连不上时的错误（符合预期）：%v", err)
}

func TestCheckAgainstMock(t *testing.T) {
	// Check 是 doctor 走的路径：这里用真 TCP 回环起 mock，
	// 驗证“能拿到 inbound>>> 计数器就算接口已开”这条规则。
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起监听失败：%v", err)
	}
	srv := grpc.NewServer()
	statspb.RegisterStatsServiceServer(srv, &mockStatsServer{counters: map[string]int64{
		"inbound>>>tag1>>>traffic>>>uplink": 42,
	}})
	go srv.Serve(lis)
	defer srv.Stop()

	stats, err := Check(context.Background(), lis.Addr().String())
	if err != nil {
		t.Fatalf("Check 对 mock 失败：%v", err)
	}
	if len(stats) != 1 || stats[0].Value != 42 {
		t.Fatalf("Check 应返回 1 条计数器 42，实际 %v", stats)
	}
}
