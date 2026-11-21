# 协议版本跟踪清单

> DEV-PLAN §10.2：每次发版前核对。核对时记日期与结论，
> 不要只改版本号不记结论。

## 当前钉死的版本（阶段 0，2026-11-22 核实）

| 组件 | 当前版本 | 依据 | 下次核对 |
|---|---|---|---|
| Xray-core（落地） | v26.x（方案调研时钉到 v26.11.9） | 节点侧方案 §2.2 | Phase 1 试点前：在测试落地核实当前实际版本 |
| sing-box（落地） | v1.14（legacy DNS 格式，1.14 移除前须迁移一次） | 面板 1.3.23 交付记录 | Phase 3 定夺时 |
| Go（agent 开发） | 1.22.2（本机 `~/workspace/toolchains/go/bin`） | DEV-PLAN §2.4 | agent 每次发版 |
| gRPC（agent 依赖） | v1.64.0 | 本仓 `go.mod` | agent 每次发版 |
| protobuf（agent 依赖） | v1.34.2 | 本仓 `go.mod` | agent 每次发版 |
| protoc（生成 stats.pb.go 用） | 25.1 | 本机 `/tmp/protoc`（下载的官方包，未装进系统） | 生成代码变动时 |

## 待确认的版本问题（阶段 0 发现，须在 Phase 1 试点前定）

1. **落地 Xray 没钉死版本。** `landing-node` 的 `install.sh` 用的是
   `ghcr.io/xtls/xray-core:latest`（第 535 行），不是 v26.x。
   DEV-PLAN §3.2 要求钉死 v26.x，现状不符。试点前定一个：
   改落地安装脚本钉死版本，或接受 latest 并在每次升级前跑验收。
2. **Xray stats / gRPC API 的行为以实测为准。** 本阶段的
   `QueryStats` 客户端只在 mock server 上测过（见
   `internal/xray/client_test.go`），没在真 Xray 上跑过。
   试点落地第一件事就是 `grpcurl` 调 `QueryStats` 看真计数器。

## 跟进节奏（DEV-PLAN §10.2 定稿）

- Xray / sing-box 发新版后 2 周内评估（看更新日志：安全修复 / 新协议 / 破坏性变更）。
- 安全修复 1 周内跟进；功能更新按月批量跟进，不追每一个小版本。
- 旧版保留 2 个小版本；破坏性变更提前 1 个版本写进更新日志。

## 核对记录

| 日期 | 核对人 | 结论 |
|---|---|---|
| 2026-11-22 | 助手（阶段 0） | 首版清单：Xray 未钉死版本的问题已记入上节；其余按方案 v1 的版本。 |
