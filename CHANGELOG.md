# 更新日志

哲学系节点侧 agent（`node-agent`）的版本更新记录（按版本从旧到新）。

### v0.0.1 建仓（2026-11-21）

- 新仓 `nbbk/zhexuexi-agent` 建仓首版：只推方案与建仓记录，不写应用代码、不打包。
- 首版内容：`README.md`、`DEV-PLAN.md`（节点侧按人计量开发流程，从 `zhexuexi-client` 的 DEV-PLAN §3 A 线 + §5 A 线测试 + §8 A 线进度抽出、单独成文）、`docs/node-side-metering-plan.md`（节点侧按人计量完整方案 v1 复制件，原样，不改内容）、`.gitignore`。
- 建仓原因：节点侧按人计量与客户端分仓做——客户端在 `nbbk/zhexuexi-client`，节点侧 agent 在本仓；现有仓 `relay-node`（main 0566500）、`landing-node`（main 7918512）一个文件都不碰。
- 未做：应用代码、打包、真机验证。

### v0.0.2 节点扩展与兼容设计（2026-11-22）

- DEV-PLAN.md 新增 §10 节点扩展与兼容设计（2026-11-22 用户定）：节点类型可插拔（协议注册表 + 适配器，新类型只加注册项 + 适配器，三件套：入站配置模板、统计来源、停用方式；新增流程加模板 → 单元测试 → 试点落地 → 灰度 → 全量，带回滚）；跟上主流（协议版本跟踪清单、Xray/sing-box 新版 2 周内评估、安全修复 1 周内跟进、旧版保留 2 个小版本，双核心并行、sing-box 先只数总量后续升级路径）；兼容设计（旧订阅链接过渡期新旧 UUID 并存、agent/面板版本协商、配置文件向前兼容、接口 v1 起步）；更新迭代流程（滚动更新一个落地一个落地更、签名校验、失败自动回滚旧版、凌晨低峰窗口）。
- 未做：应用代码、打包、真机验证。

### v0.1.0-stage0 阶段 0：搭架子 + QueryStats 最小验证（2026-11-22）

- Go 工程骨架：`go.mod`（模块 `github.com/nbbk/zhexuexi-agent`，Go 1.22，grpc v1.64.0 / protobuf v1.34.2）、`cmd/node-agent`（`version` / `doctor` / `run` / `join` 四个子命令，`join` Phase 0 再实现）、`internal/xray`（StatsService gRPC 客户端，`QueryStats` / `GetStats`，reset 永远 false）、`internal/ledger`（本地账本：基线规则、待推送暂存、翻周期幂等、原子落盘 0600）、`internal/config`（配置文件，600 权限）、`internal/sync`（推送与用户表数据形状）、`internal/enforce`（只有包说明，执法是 Phase 2 的事）、`systemd/node-agent.service`。
- 最小验证：`QueryStats` 在 mock Xray 服务上能出数（`user>>>` 按用户、`inbound>>>` 总量），二进制级验证：`doctor` 对 mock 通过、没 Xray 时干净报错退出 1；`run` 快照循环 4 次账本数字自洽、收到信号先落盘再退出。
- 接口审计：`docs/interface-audit.md`（面板 1.3.29 代码实测：`rpt_` 认证与 `central/user-traffic` 只读接口可复用；`GET /api/v1/node/users`、`POST /api/v1/node/push`、`GET /api/v1/node/config` 三个新接口与 `node_uuids` / `metering_source` / agent 状态三个字段全缺，panel-adapter 单独转接做不到推送，须拍板 D10）；`docs/protocol-tracker.md`（落地 Xray 没钉死版本，`install.sh` 用 `latest`，与 DEV-PLAN §3.2 要求不符，试点前定）。
- 测试：`go test -race ./...` 18 个全过（含基线倒退不重加、推送失败加回、翻周期幂等、reset 永远 false）；`gofmt` 与 `go vet` 全过；静态二进制 `CGO_ENABLED=0 go build` 能编。
- 未做：真 Xray 实测、真实落地、推送（Phase 0）、入网 join、执法（Phase 2）、真机验证。

### v0.1.1 试点落地纠正：印尼排除、改选智利（2026-11-22）

- 试点落地改选智利落地：用户 2026-11-22 05:00 说智利落地可以用，05:01 明确说印尼落地是客户定制、不要用。`docs/pilot-choice.md` 结论已改（§0.4–§0.5、§3、§4、§6、§7），DEV-PLAN.md 的 D3 行与第 1 周行已同步改。
- 印尼落地全程不碰：不再确认、不再上机操作、不再作为试点或备选；其 Xray 实证等调查事实保留在 `docs/pilot-choice.md` §2 备查，不作选择依据。
- 智利落地开工前置条件：须先真机确认核心是 Xray（容器名 / 注册表）；若确认不是 Xray，停下来汇报，不要擅自换试点。
- 未做：智利核心真机确认、Phase 0 统计配置、推送、真机验证。
