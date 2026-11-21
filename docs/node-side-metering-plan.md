# 落地反馈回中转面板：按人计量回节点侧 完整方案

> 状态：方案稿，2026-11-21。用户 2026-11-21 17:15 点选“直接写完整版，按人计量回节点侧”。
>
> 背景：按订阅限流 A（1.3.25）与 B（Go relay 进程内计量，方案 v3）都数不到直连流量——直连不经过中转机。本方案把计量点搬到落地机上：落地跑一个 agent，读 Xray 统计、按人推回面板。这是节点侧计量（市面机场面板的正统做法），不是中转侧计量。
>
> 底座：轻量节点后端方案 v1（`node-backend-plan.md`，Phase 0 sidecar）+ 方案 v2（`per-user-traffic-plan.md`，要抄的市面机制）+ B v3（`per-user-traffic-plan-b-v3.md`，术语与 A/B 的局限）+ 两份市面调研报告（2026-11-20）。

## 0. 结论先行（先看完再往下读）

1. **它解决什么：直连流量按人计量 + 限流执法。** 落地机上的 agent 按订阅者数流量、推回面板；超量/到期时 agent 在落地删用户。直连和中转的流量在落地汇合，落地是唯一能看到一个人全部流量的地方。建成后，按人限流不再依赖中转机。
2. **它解决不了什么：** Xboard 落地（§8，范围之外）；sing-box 落地没有按用户统计接口（§9，只能先数总量）；通用订阅仍然不能按人限流（§3.5，所有人共用一条链接，分不清人）；落地侧的 Xboard 类面板自己设的限流/到期，仍然管不了。
3. **最大的破坏性变化（必须先知道）：每个订阅者要在落地有一个独立身份。** 现在落地一个节点只有一个 UUID，所有订阅者共用。改成每人一个 UUID 后，**现有订阅链接全部变掉，客户端必须重新导入一次**。这不是小改动，是换身份体系。迁移只能一个落地一个落地做，每次都要重新发链接。
4. **A 和 B 的命运：** 某个落地装上 agent 后，该落地的 A（每人一条转发）就不再需要——中转回到盲转发，按人计量由落地负责；B 只在没有 agent 的落地上还有意义。不是一下子全拆，是按落地逐个退役。
5. **它改了哪条既定原则：** “落地不用装东西”不再成立。agent 必须装在落地机上（静态 Go 二进制 + systemd），还要改落地 Xray 的配置（开 stats 与 API）。这是用户 2026-11-21 17:02 主动提的方向变化，方案按此写。

**本节未核实项：** 无。本节是结论汇总，细节与依据在后面各节。

## 1. 架构：落地 agent + 面板控制面

### 1.1 一张图

```
  管理面板（最高一级）                远程面板（拥有该落地的面板）
  ┌──────────────────┐              ┌──────────────────┐
  │ 配额/重置/停用决策 │              │ agent 注册/令牌   │
  │ 汇总各落地每人用量 │◀── central ──│ 汇总本落地每人用量 │
  │ 订阅生成（每人UUID）│  user-traffic │ 落地用户表下发    │
  └──────────────────┘              └──────┬───────────┘
                                           │ agent 拉取/推送
                                           │ (HTTPS, 每 60 秒)
                                    ┌──────▼───────┐
                                    │ 落地 agent    │  落地机
                                    │ node-agent    │
                                    │ 静态 Go 二进制 │
                                    └──────┬───────┘
                                           │ gRPC loopback
                                    ┌──────▼───────┐
                                    │ Xray-core     │
                                    │ VLESS+WS+TLS  │
                                    │ (Docker 容器)  │
                                    └──────────────┘
  客户端 ──直连──▶ 落地 Xray ──▶ 互联网
  客户端 ──中转──▶ realm ──▶ 落地 Xray ──▶ 互联网
  （两条路都在落地 Xray 汇合，agent 在汇合点数流量）
```

### 1.2 角色分工

| 一级 | 职责 |
|---|---|
| 管理面板（控制面，最高一级） | 配额配置；汇总各落地每人用量；算重置；做停用/恢复决策；订阅生成（给每人发 UUID）；展示每人用量（含直连） |
| 远程面板（拥有落地的面板，执行面） | agent 注册与令牌；下发用户表与配置；收 agent 的流量推送；向管理面板汇总（复用 A 已有的 `central/user-traffic` 通道） |
| 落地 agent（`node-agent`） | 拉用户表；经 gRPC 下发到 Xray；每 10 秒快照 Xray 统计；落盘账本；每 60 秒推增量给远程面板；本地执法（删用户、重启 Xray 断在线连接） |
| 落地 Xray | 零协议改动，只加统计配置（§2）；按 email 计数；接受 agent 的 gRPC 用户增删 |
| 中转机 realm | 不动，回到盲转发；装上 agent 的落地不再需要每人一条转发（§7.4） |

agent 只跑在落地机上，不跑在中转机上（中转机没有 Xray 可管）；管理面板模式本机没有落地，不跑 agent。**面板绝不 SSH 落地机**——所有下发走 agent 的 HTTPS 通道，这是 v1 的既定原则，本方案沿用。

### 1.3 通道：主方案是节点拉取（agent 主动），不是面板推送

|  | 节点拉取（主方案） | 面板推送（备选） |
|---|---|---|
| 方向 | agent 每 60 秒向远程面板拉用户表/配置；每 60 秒向远程面板推流量 | 面板主动连 agent 下发/查数 |
| 防火墙/NAT | 最好——agent 只需出站 HTTPS，不开入站管理口 | 落地要向面板 IP 开管理口，mTLS CA 全套 |
| 面板挂了 | agent 持上次用户表照常服务，按本地账本继续执法 | agent 持上次配置照常服务 |
| 吊销延迟 | ≤60 秒（拉取间隔），界面写明 | 立即 |
| 复杂度 | 最低，复用面板已有的 central 令牌机制（§10） | 要搭 mTLS CA、发证、轮换、吊销 |

**主方案定为节点拉取，理由三条：**

1. 市面验证过的形态：V2bX/v2node/ppanel-node 全部是节点拉取（UniProxy，`GET config`/`GET user`/`POST push`，ETag + SHA-256 跳过未变内容）；Remnawave/Marzban 的面板推送是另一套，需要 mTLS CA（调研报告 §3）。
2. v1 方案 §4.2 已定“v1 默认节点拉取”，本方案是 v1 的计量分支，不另起一套。
3. 落地机在公网，面板推要开入站口；拉取只出站，防火墙零改动。吊销延迟 ≤60 秒可以接受——A 的停用本来也是分钟级（采样 60 秒 + 汇总 60 秒），没有变差；紧急吊销走长连接流是 Phase 3 的事（§11）。

拉取与推送各走独立接口（与 UniProxy 的拆法一致，ETag 各自独立）：
- `GET /api/v1/node/config?node_id=…`：入站模板、证书引用、配置版本（ETag）。
- `GET /api/v1/node/users?node_id=…`：用户表（§3），带版本号/ETag，未变返回 304。
- `POST /api/v1/node/push`：流量增量 + 在线用户 + 健康（§4.5）。

agent 与远程面板之间是 HTTPS（面板域名 + 11111 端口，面板须开 HTTPS，见 §10）。

**本节未核实项：** 三个新接口是本方案定的，未实现；agent 向远程面板汇报、远程面板再向管理面板汇总的两级结构沿用 A 的 central 通道（`central/user-traffic`，1.3.25 已有），未改协议。

## 2. Xray 统计怎么开：要加的确切配置块

### 2.1 现状（已核实代码）

落地 tiktok-node 的 `install.sh` 生成的 Xray 配置里**没有** `stats` 段、**没有** `api` 入站、**没有** `policy` 的 `statsUserUplink`/`statsUserDownlink`——grep 零命中（唯一命中的 `nameserver-policy` 是 DNS 策略，与统计无关）。配置只有 `log`、`dns`、`inbounds`（单个 VLESS 入站，`clients` 里一个 UUID）、`outbounds`（freedom）。所以落地 Xray 现在**完全不产生任何流量统计**，agent 无数可读。统计功能要先给落地 Xray 开。

### 2.2 要加的四块（按 Xray 官方机制，调研报告 §6.2 已核实）

**第 1 块——顶层 `stats` 段（开关）：**

```json
"stats": {}
```

空对象就行。没有这一段，统计管理器根本不启动，`policy` 写了也没用。这一段是两道门的第一道。

**第 2 块——`policy`（按用户级别开统计）：**

```json
"policy": {
    "levels": {
        "0": {
            "statsUserUplink": true,
            "statsUserDownlink": true,
            "statsUserOnline": true
        }
    }
}
```

- Xray 按用户级别（`level`）决定开不开统计；客户端不写 `level` 时默认 0 级，所以 0 级开就覆盖全部用户。
- `statsUserUplink` / `statsUserDownlink` 是按人流量的两道门；`statsUserOnline` 是在线 IP 追踪（`user>>>{email}>>>online`，IP 20 秒无流量即过期），给“在线用户”展示用，不开也不影响流量计量。
- 注意：这两个开关只决定“记不记”，不决定“能不能查”——查数是第 3、4 块的事。

**第 3 块——顶层 `api` 段（声明服务）：**

```json
"api": {
    "tag": "api",
    "services": ["HandlerService", "StatsService"]
}
```

- `StatsService` 是查流量用的（`GetStats` / `QueryStats`，见 §4.1）。
- `HandlerService` 是增删用户用的（`AlterInbound`，见 §3.3）。两个都要开，缺一个 agent 就少一半能力。

**第 4 块——`api` 入站 + 路由（把 gRPC 口子只开在 loopback 上）：**

```json
"inbounds": [
    {
        "listen": "127.0.0.1",
        "port": 10085,
        "protocol": "dokodemo-door",
        "settings": { "address": "127.0.0.1" },
        "tag": "api"
    }
],
"outbounds": [
    { "protocol": "freedom", "tag": "direct" },
    { "protocol": "blackhole", "tag": "api" }
],
"routing": {
    "rules": [
        { "inboundTag": ["api"], "outboundTag": "api", "type": "field" }
    ]
}
```

- `dokodemo-door` 入站只绑 `127.0.0.1:10085`，gRPC 只能从本机连。**Xray 的 API 没有内置认证，能连上就能查数、改用户**，所以这一块绝不能绑 `0.0.0.0`、绝不能映射到公网端口（见 §10）。
- Phase 0 的 agent 跑在落地机宿主机上、Xray 跑在 Docker 容器里：容器内 `127.0.0.1:10085` 宿主机够不着，要在 compose 里加一条**只绑宿主机 loopback 的端口映射**（`127.0.0.1:10085:10085`），agent 连宿主机的 `127.0.0.1:10085`。不要写成 `10085:10085`（那会绑公网）。

### 2.3 现有落地怎么生效

1. agent（或安装脚本）重写宿主机上的 `config.json`（多节点是 `config-2.json` 等，容器名 `tiktok-xray` / `tiktok-xray-2`），补上 §2.2 的四块，原有入站、出站、DNS 一字不动。
2. 重启 Xray 容器生效（`docker compose restart tiktok-xray`，或按 `install.sh` 自身的写法 `docker compose up -d --force-recreate`）。只重启 Xray 容器，不动 Caddy——证书与 443 不受影响。
3. 重启后验证：宿主机上 `grpcurl`（或 agent 自检）调 `QueryStats`，应能看到 `inbound>>>` 总量计数器；有用户流量后出现 `user>>>` 计数器（§4.1）。
4. 这是一次性改动：以后加用户、改配额都不用再动配置（走 gRPC，§3.3），只有入站本身变化（改端口、改传输、改证书）才需要再改配置 + 重启。

**版本注意：** 落地 Xray 是 v26.x（调研时钉到 v26.11.9）。stats 与 gRPC API 在 v26 里的行为与上述一致（调研报告核实自 Xray internals 文档）；实施时把落地 Xray 版本钉死，升级前在测试环境跑一遍 §14 的验收。

**本节未核实项：** §2.2 的四块配置按调研报告（Xray internals 文档，2026-11-20 核实）与 Xray 官方 API 形态写成，未在本机真实 Xray 上跑过——实施时第一步就是在测试落地按 §2.3 走一遍，`QueryStats` 能出数才算过；`127.0.0.1:10085:10085` 的 loopback 映射写法须在真实 Docker 上验证（宿主机连通且公网扫不到）。

## 3. 每人一个身份：落地 Xray 里一个订阅者一个用户条目

### 3.1 为什么必须每人一个身份

Xray 按人计数的钥匙是用户条目里的 **`email` 字段**，不是 UUID。分发器只在 `user != nil && len(user.Email) > 0` 时才建按人计数器（Xray internals 文档的 `getLink()` 代码，2026-11-21 刚复核过原文）。`email` 为空的用户没有按人计数器，两个用户共用一个 `email` 时流量合并到一个计数器。

现在落地一个节点只有一个 UUID、一条 `clients` 记录、没有 `email`——所有订阅者共用一个身份，Xray 就只能数出“这个节点总共多少”，分不出人。所以**不改身份体系，节点侧按人计量无从谈起**。这是本方案最大的前提，也是 §0 第 3 条破坏性变化的根。

### 3.2 身份映射（面板 → 落地）

| 面板侧记录 | 落地 Xray 用户条目 | 说明 |
|---|---|---|
| 订阅用户（SubUser）/ 合并订阅（MergedSub），各有一条记录 | 每条记录在每个落地各一个 UUID + 一个 `email` | 一个订阅跨 3 个落地就有 3 个 UUID（每落地一个），`email` 全落地通用 |
| `email` 格式：`u-sub-<id>@node` / `u-merged-<id>@node` | 写进 Xray `clients` 条目的 `email` 字段 | `email` 只作计数钥匙，不作登录名；用订阅记录的 kind + id 编码，不用 UUID 字符串——轮换 UUID 不断用户历史（v1 §5 的规则：流量永远按 `user.id` 记） |
| 通用订阅（全量） | 共用旧 UUID，`email` 记为 `generic@node` | 见 §3.5 |
| 每个 UUID | `crypto/rand` 生成的 UUIDv4，与节点 UUID 同款随机强度 | 面板生成，存进订阅记录的新字段（`node_uuids: map[landing_id]uuid`，见 §7.1）；UUID 是客户端凭证，要存明文（与今天节点 UUID 的存法一致），不是哈希 |

agent 在本机维护一张映射表（`email` ↔ 面板用户 `(kind, id)` ↔ UUID），从拉取的用户表里来；流量上报按 `(kind, id)` 回面板，面板按此归属到 SubUser/MergedSub。

### 3.3 下发：主方案是 gRPC AlterInbound 免重启

**主方案：`HandlerService.AlterInbound`（`AddUserOperation` / `RemoveUserOperation`），不用重启 Xray。**

- agent 收到用户表后做差分（按 UUID + `email` 比对，V2bX 的 `compareUserList` 同款）：新增的用户调 `AlterInbound` 加进去，删除的调 `AlterInbound` 删掉，改配额/改到期不碰 Xray 用户条目（配额在 agent 的账本里，不在 Xray 配置里）。
- 加用户立即生效（新连接能用新 UUID 建连），删用户立即拒绝新连接（Xray 认证时查不到该用户）。其他用户的在线连接不受影响——这是相对“改配置 + 重启”最大的好处。
- 依据：3x-ui 用同一套 gRPC（`AlterInbound` + `AddUserOperation`/`RemoveUserOperation`）做面板增删用户；Remnawave 的 SDK 同样包了 `addTrojanUser`/`removeUser`（调研报告 §2）。

**备用：重新生成配置 + 重启 Xray 容器。**

- 只在两种情况下用：①入站本身变化（改端口、改传输、改证书、改 `stats`/`api` 配置）；②gRPC 通道不可用（api 入站挂了、agent 连不上 10085）。
- 代价是全节点闪断一下（容器重启约 1–2 秒，客户端自动重连），所以日常增删用户绝不走这条路。

### 3.4 原来共用的节点 UUID 怎么办：留给通用订阅

- 旧的共用 UUID **不删除、不轮换**，继续留在该入站的 `clients` 里，`email` 写为 `generic@node`。
- 它的流量计入 `generic@node` 这个计数器，面板把它算作该落地的“通用订阅总量”，不归属到任何一个订阅者。
- 这样迁移期间旧链接一直能用（用户还没重导之前不断网），通用订阅永久有身份可用。退役旧 UUID 等于废掉通用订阅——除非用户决定不要通用订阅了，否则不退役。

### 3.5 通用订阅仍然不能按人限流

通用订阅的本质是一条链接所有人共用、一个 UUID 所有人共用，落地分不清谁是谁——节点侧也分不清。通用订阅的流量只能按 `generic@node` 计总量，不能按人分、不能按人限。这一点与 A、B 完全相同，界面写明。

想给通用订阅里的人限流，唯一的办法是把他们转成一人一条订阅（SubUser / MergedSub），每人发一个 UUID。

**本节未核实项：** `AlterInbound` 的 `AddUserOperation`/`RemoveUserOperation` 按调研报告（3x-ui 的 gRPC 用法、Remnawave SDK）写成，未在本机真实 Xray 上跑过——实施时须在测试落地验证“加用户后新 UUID 立即可用、删用户后新连接立即被拒、其他用户不断线”三条；`email` 格式 `u-sub-<id>@node` 是本方案定的，未实现。

## 4. 计量：QueryStats 按用户查，高频快照 + 本地落盘

### 4.1 查什么：计数器名字与方向

Xray 的计数器名字是 `user>>>{email}>>>traffic>>>uplink`（客户端 → Xray，上行）与 `user>>>{email}>>>traffic>>>downlink`（Xray → 客户端，下行）。另有 `inbound>>>{tag}>>>traffic>>>uplink/downlink`（每入站总量）与 `outbound>>>` 总量，给 Phase 0 的节点总量用。

agent 用 `StatsService.QueryStats` 按模式查：一次 `QueryStats(pattern="user>>>")` 返回该 Xray 实例全部用户的上下行计数器；或 `GetStats(name, reset=false)` 逐个查。查到的是**累计值**（int64 字节，从 Xray 启动起单调递增，直到显式清零）。

### 4.2 读数纪律：reset:false + agent 侧持久基线（主方案，不用 reset:true）

两种读法，选错会丢数或重数（调研报告 §6.3、3x-ui 的教训）：

|  | `reset:true`（读完清零） | `reset:false` + 基线（主方案） |
|---|---|---|
| 做法 | `GetStats` 读到的就是自上次读以来的增量 | 每次读累计值，增量 = 本次累计 − 上次基线；基线存 agent 本地账本 |
| 优点 | 不用存基线 | agent 崩溃、重启都不丢数 |
| 致命短板 | agent 读完还没落盘就崩溃，那一窗口的流量永久丢失（计数器已被清零） | 要自己管基线 |
| Xray 重启 | 计数器归零，下次读到 0 | 计数器归零，见下条 |

**主方案定为 `reset:false` + 持久基线，理由：** agent 崩溃窗口的丢数是不可接受的（计量是计费依据）；基线管理有现成的正确做法（3x-ui 2026 年修过的 bug 就是这条）。

**基线规则（逐条，照抄 3x-ui 的修法）：**

1. 每个计数器（每用户每方向）存一个基线 `baseline`，增量 = `max(0, 当前累计 − 基线)`。
2. 计数器倒退（本次累计 < 基线）说明 Xray 重启过、计数器清零了：**增量按 0 算，只把基线重定为当前累计，绝不把整个当前累计重加一遍**。3x-ui 出过“重启后流量又弹回来”的 bug，就是倒退时把整个计数器重加了——本方案禁止这种做法。
3. 基线与已确认用量一起落盘（§4.3），agent 重启后从账本恢复基线，不从 0 开始猜。

### 4.3 快照节奏与本地持久账本

- **快照：每 10 秒一次**（参照 x-ui 的 `XrayTrafficJob` 每 10 秒）。每次快照：`QueryStats` 读全部用户计数器 → 按 §4.2 算增量 → 累加进本期用量 → 写账本。
- **账本：`/etc/node-agent/ledger.json`（原子写：写临时文件再 rename）**，每用户一行：`(user_id, period_id, up_period, down_period, up_total, down_total, next_reset_at, last_reset_at)` + 每个计数器的 `baseline`。面板是记账方（billing），agent 是执法方——账本是 agent 执法的依据，不只放内存。
- **为什么 10 秒这么密：** Xray 的计数器在内存里，Xray 重启就清零。快照越密，重启丢的窗口越小（≤10 秒的流量）。10 秒是 x-ui 的实测 cadence，不是拍脑袋。
- **推送：每 60 秒一次**（UniProxy 的 `push_interval` 默认 60 秒），只推有增量的用户；另有立即推：某用户一次快照后跨过 80% 或 100% 配额时不等 60 秒，立即推一次（让面板尽快知道，也让 agent 的本地执法与面板决策对上）。

### 4.4 推送失败：先暂存、面板 ACK 后再清、失败加回

快照的增量先进暂存区（staging），agent 把增量 POST 给远程面板，面板 ACK 后才清暂存；失败（超时/非 2xx）把暂存加回计数器，下次再推。绝不“先清零再推”（丢数），绝不“推完不确认就清”（重试 double-count）。这是 NodeRS-AnyTLS 的 `accounting.rs` 机制 + V2bX 的 `ReturnUserTraffic`，调研报告 §1.3/§5.3。

**去重：** 每次推送带 `(user_id, period_id, seq)`（`seq` 每用户每周期递增），面板侧按 `seq` 去重；重推的增量 `seq` 相同则丢弃。uniproxy 文档明确警告“重放报告会重复记账”，所以去重必须做在面板侧，不能只靠 agent 不重推。

### 4.5 面板侧汇总：节点侧为准，同一落地不叠加

这是最容易算错账的地方，规则只有一条：

> **同一个落地、同一个用户，面板只认一个来源的数：如果该落地有 agent 在报数（最近 5 分钟内有推送），就以节点侧的数为准，忽略 A 的中转侧计数；如果该落地没有 agent（或 agent 离线超过 5 分钟），就回落到 A 的中转侧计数（只含中转流量，界面标注“仅中转”）。两个来源永远不相加。**

原因：中转过来的流量最终也进了落地 Xray，节点侧的数已经包含了中转那一份；再把 A 的数加上就是 double-count。

面板按落地记一个 `metering_source` 状态：`agent` / `relay(A)` / `none`，列表里标出来。管理面板汇总时：每人每落地取该落地的权威来源，再跨落地相加，得到每人总用量；`subscription-userinfo` 头的 upload/download/total 就用这个合并后的数（语义与 A 时代一致，数字口径见 §4.6）。

在线用户：agent 每次推送 piggyback 一份在线表（`GetAllOnlineUsers`，每用户在线 IP 列表；Xray 的在线语义是“20 秒内有流量”，不是“有长连接”，调研报告 §6.2）。

### 4.6 计量口径（迁移时必须写明的变化）

- Xray 按**负载字节**计（`SizeStatWriter` 加的是 `MultiBuffer.Len()`，Xray 内部缓冲后的载荷、编码前的字节，不含 TLS/WS 帧头与 TCP/IP 头——Xray internals 文档原话，2026-11-21 刚复核）。
- A 按 **L3 字节**计（iptables 含 TCP/IP 头），比负载字节多几个百分点。
- **装上 agent 的落地，同一用户的用量数字会比 A 时代小几个百分点——这是口径变化，不是丢数。** 不补固定系数（系数因协议而异，WS/TLS 开销不同，补了反而错），界面写明口径（“用量按负载字节计，不含包头”），`subscription-userinfo` 头的数字口径与之一致。客户端显示的用量一般也按负载计，节点侧口径与客户端更接近（A 的 L3 反而比客户端显示的多）。

**本节未核实项：** `QueryStats` 一次返回全部用户计数器的性能（用户数上百时单次返回体大小）未实测——实施时先按一次全量查做，用户数上去后再评估分批；L3 与负载字节的具体差值百分比未实测（“几个百分点”是调研报告的定性说法）；在线 IP 的 20 秒语义按 Xray internals 文档写成，未实测。

## 5. 执法：面板裁决、agent 执行（超量不断整机重启的办法只有一个：重启 Xray）

### 5.1 两处执法（市面同款，调研报告 §7.2）

- **(a) 准入：** 用户不在 Xray 的 `clients` 里，新连接在认证时直接被拒（VLESS UUID 对不上）。不建连接、不耗落地资源。准入的执行者是 Xray 本身，agent 的工作是保持 `clients` 列表与“可用用户”一致。
- **(b) 巡查：** agent 每 10 秒扫一遍账本：本期用量在连接中途跨过上限的，执行 §5.2 的停用。只停该用户，其他用户无感（除了重启 Xray 那一下，见 §5.2）。

### 5.2 停用怎么做：删用户 + 重启 Xray（主方案）——先说清为什么没有更优雅的办法

**原生 Xray 的 `RemoveUser` 不杀已建立的连接。** 这是 Xray 的既定行为（调研报告 §1.4）：删用户只影响新连接，已建立的 TCP/TLS 会话继续传，直到自然结束。V2bX 能关在线连接是因为它改了 Xray 的分发器（`DelUsers` + `LinkManager.CloseAll()`）；我们用的是官方原版 Xray，没有这个能力；为此去 fork 改 Xray 不在本方案范围内。

所以用原版 Xray 停用一个用户只有两档可选：

|  | 自然排空（温和） | 删用户 + 重启 Xray（主方案） |
|---|---|---|
| 做法 | 只删用户，不动在线连接 | 删用户后重启 Xray 容器，该节点全部用户闪断约 1–2 秒、客户端自动重连 |
| 效果 | 已建立的长连接（视频、下载）能一直传到结束，超量用户实际不断 | 正在传的连接立即断，新连接被拒，停用立即生效 |
| 代价 | 限流有名无实——一个一直在传的用户可以一直传下去 | 该节点其他用户闪断一下（约 1–2 秒，客户端自动重连） |

**主方案定为删用户 + 重启 Xray，理由：** 限流必须立即生效，否则 100% 停用就是摆设；闪断一下是 3x-ui 的默认做法（`restartXrayOnClientDisable`，默认开），市面接受这个代价。面板 UI 里要写明：“停用会闪断该节点其他用户一下（约 1–2 秒，客户端自动重连）”，不要让用户以为是无感的。

### 5.3 80% 提醒 / 100% 停用，复用 A 的语义（1.3.25 已实施）

- 80% 时 Telegram 提醒一次（不重复发）；100% 时停用；手动恢复仍超上限则拒绝，编辑调高上限且不再超量自动恢复。
- 决策分两条线，互相兜底：
  - **快线（agent 本地，10 秒）：** agent 巡查发现某用户跨过 100%，立即在本地执行 §5.2 的停用，并把“本地停用 + 原因 + 时间”随下次推送上报面板。
  - **慢线（面板权威，60 秒）：** 面板按汇总用量算出 80%/100%，100% 时把该用户从下次拉取的用户表里拿掉（市面做法：停用靠“拿掉名单”，V2board 的 `getAvailableUsers` 同款）；agent 拉到新表后删用户。面板收到 agent 的本地停用上报后，在自己的状态里把该用户标为已停用（这样下次拉取也不会再把他加回来，避免来回翻动）。
- 恢复：面板把用户加回用户表（调高上限或重置后），agent 拉到后经 `AlterInbound` 加回 Xray；在线连接不自动恢复，客户端重连即可。

**本节未核实项：** “删用户 + 重启 Xray 容器约 1–2 秒”未实测（容器重启时间因机器而异）；`RemoveUser` 不杀已建连接按调研报告（Xray 行为）写成，未在本机实测——实施时须在测试落地验证“删用户后已建立的连接是否还在传”。

## 6. 重置：复用 A 的 next_reset_at 三种模式

三种模式复用 A（1.3.25 已实施）：自然月（每月 1 号）、跟随生成日（建订阅那天，每月同一天）、自定义每月几号；31 号钳制到月末（NX 同款，调研报告 §3.3）；`next_reset_at` 建订阅/续费时由面板算出存下来。

- agent 每 10 秒检查一次：`now >= next_reset_at` 时翻周期（`period_id + 1`，本期用量清零，总量不动，算下一次 `next_reset_at` 并写回账本、上报面板）。翻周期是幂等的（compare-and-set：`last_reset_at` 已是本次的不再翻），时钟回拨不会重复翻。
- **面板是重置的权威：** `next_reset_at` 由面板算，agent 只执行；面板离线时 agent 按本地存的 `next_reset_at` 翻（与 A 的 fail-open 一致）；面板恢复后以面板的 `next_reset_at` 为准（agent 上报的 `period_id`/`last_reset_at` 供面板核对）。
- 面板离线跨过重置点的情况：agent 按本地 `next_reset_at` 翻了周期，推送里带的 `period_id` 已是新周期；面板恢复后按 `period_id` 归属增量，不会把旧周期的迟到增量算进新周期（v2 §1.3 的 3x-ui 教训）。

**本节未核实项：** 无。重置模型沿用 A（已实施）与 Xboard 的 `next_reset_at` 字段（调研报告 §1.2）。

## 7. 订阅生成怎么变 + 迁移步骤

### 7.1 订阅生成

- **直连链接：带每人自己的 UUID。** 订阅生成时按 `(订阅, 落地)` 查 `node_uuids` 表，拼出该用户的直连链接（域名、端口、WS 路径与原来相同，只换 UUID）。
- **中转链接：同样带每人自己的 UUID。** UUID 在 TLS 载荷里面，中转的 realm 是盲转发、只转发字节流，不读 UUID——所以中转链接的 UUID 换成每人的，realm 不用改、不用重启。订阅生成时中转链接与直连链接用同一个 UUID（同一个用户在同一个落地只有一个身份，不分直连/中转）。
- **通用订阅：不变。** 通用订阅继续用旧的共用 UUID（`generic@node`），链接格式不动。
- 面板数据模型新增：订阅记录（SubUser/MergedSub）加 `node_uuids: map[landing_id]uuid`（每落地一个 UUID）；落地记录加 `metering_source`（`agent` / `relay` / `none`，§4.5 的状态）与 agent 状态（版本、最后推送时间）。

### 7.2 迁移步骤（一个落地一个落地做，每次都要重新发链接）

1. **选试点落地：** 先选一个落地（建议用户量最少的），不全切。
2. **Phase 0（§11）：** 该落地先只数总量，验证通道与上报（订阅链接不动，用户无感）。
3. **试点用户验证（Phase 1 验收，§14）：** 建两个测试订阅（A 用户、B 用户），给 A 发每人 UUID、拉 5MB 直连流量，面板显示 A=5MB、B=0，归属正确才算过。
4. **全量迁移（一次重新生成）：** 试点通过后，给该落地全部订阅者生成每人 UUID，订阅一次性重新生成——**直连与中转链接同时换成每人 UUID，同时把该落地的 A 每人转发退役**（中转链接改回共享端口 + 每人 UUID）。用户重新导入一次（直连与中转各一条，都是新链接）。
5. **验证：** 迁移后按 §14 逐项验收（真实流量、超量停用、重启不丢不重）。
6. **逐落地推广：** 一个落地验收通过，再做下一个。不要多个落地同时迁——出问题分不清是哪个落地的。
7. **A 转发的退役：** 该落地的 A 每人转发（30000–39999 端口段里的端口）在迁移时一并删除、端口回收；realm 配置里该落地的每人转发段删掉（共享端口保留）。退役后该落地不再建 A 转发，新订阅者直接走 agent。

**为什么 UUID 迁移与 A 退役放在同一次重新生成里：** 两件事各改一次链接就要用户重导两次；合在一次里只重导一次。代价是如果 agent 有 bug，退役后没有 A 兜底——所以第 3 步的试点验收必须先过，不通过不迁。

### 7.3 回滚（agent 出问题能不能退回 A、怎么退）

- 能退。步骤：停 agent → 面板把该落地的 `metering_source` 改回 `relay` → 给该落地的订阅者重新生成 A 转发（每人端口）与订阅链接（中转链接改回每人端口，直连链接改回共用 UUID 或保留每人 UUID——直连链接用每人 UUID 也能走，因为 Xray 里每人 UUID 还在；最简单的回滚是直连链接不动、只把中转链接改回每人端口）→ 用户重新导入一次。
- 回滚不丢数：用量账本在面板侧（agent 推上来的增量已进面板的 `user_traffic.json`）；agent 本地账本作为对账依据留着，不删除。
- 回滚后该落地回到 1.3.25 的 A 行为（直连不计量，界面标注“仅中转”）。

**本节未核实项：** 迁移与回滚是流程设计，未实施验证（须按 §14 的验收标准实测）；`node_uuids` 字段名与存储格式是本方案定的，未实现。

## 8. Xboard 落地：明确写出范围之外

Xboard 管理的落地（跑 Xboard-Node / V2bX / XrayR 的节点）**不在本方案范围内**，原因三条：

1. 它们已经有自己的计量：V2bX/XrayR 按用户计量、上报给 Xboard 面板（UniProxy 推送，调研报告 §1.3）。我们的面板没有到 Xboard 的通道，看不到这些数。
2. 我们的 agent 不能直接管 Xboard 的 Xray：Xboard-Node 的用户表由 Xboard 下发，agent 再写一份会打架（两个事实来源）。
3. 证书、入站、用户都在 Xboard 那边，agent 改不动。

**理论上的桥（不做，仅记录）：** 面板调 Xboard 的管理 API 读每人流量。Xboard 是否对外暴露可供第三方调用的按人流量接口，本次没有核实——不要假装能管。如果以后用户想把 Xboard 落地也纳入，先查 Xboard 的 API 文档，确认接口存在再单独立项。

Xboard 落地保持现状：中转侧 A 限流照旧（直连不计量，界面标注），不受本方案影响，不产生回归。

**本节未核实项：** Xboard 是否有可供外部调用的按人流量 API 未核实。

## 9. sing-box 落地：按用户计量没有 API，三选项推荐一个

### 9.1 先说清 sing-box 的现状（已核实）

- sing-box-node 的 `install.sh` 里没有 stats、没有 experimental API 配置。
- sing-box 本身**没有增删用户 API**（调研报告 §2，核实自 sing-box 官方文档）：只有 Clash API（全局流量/连接，不是按用户）与 V2Ray API（聚合统计，不是按用户），而且两个 API 都要 `with_clash_api` / `with_v2ray_api` 编译标签——官方发布的二进制默认不带。现在落地跑的 `ghcr.io/sagernet/sing-box:latest` 是官方版本，两个 API 都没有。
- 结论：sing-box 落地**现在没有任何可用的按用户计量接口**，agent 读不到按人流量。

### 9.2 三选项

|  | 做法 | 能数到什么 | 代价 |
|---|---|---|---|
| **(a) 只数总量（主方案，近期）** | agent 读落地宿主机的 iptables/nftables 计数器（按落地入站端口计总量，A 在中转机上用的同一套做法），推回面板 | 该落地总共多少流量（含直连），不分人 | 总量口径是 L3（与 Xray 的负载字节差几个百分点，界面写明）；按人限流仍然做不到 |
| **(b) 换 Xray 内核** | 该落地重装成 tiktok-node（Xray），走本方案 §2–§7 的全套 | 按人计量 + 执法，与 Xray 落地完全一致 | 破坏性最大：重装落地、新 UUID、新证书、新链接，全部用户重新导入；等于换节点 |
| **(c) 这些落地只走中转限流** | sing-box 落地的限流订阅强制只走中转（A 的每人转发），直连链接不发给限流订阅 | 限流管用（只走中转就能数），但直连这条路对限流订阅关闭 | 直连的低延迟没了；用户问“为什么这个节点没有直连”时要解释 |

**主方案定为 (a)，理由：**

1. (b) 是重装节点，不是加计量——只有当某个 sing-box 落地确实需要按人限流时才值得做，按落地在 Phase 3 定，不在现在一刀切。
2. (c) 牺牲了直连，而直连恰恰是用户要保的体验；(a) 不动任何链接、不动任何用户，先拿到“直连流量有多少”这个最缺的可见性。
3. (a) 的 agent 代码与 Xray 落地的 agent 是同一个二进制（总量模式），以后某个落地换了 Xray 内核，agent 切到按人模式即可，不用换 agent。

Phase 3 再按落地定：哪个 sing-box 落地换 Xray 内核（b），哪个继续只数总量（a），哪个走中转限流（c）。三个选项可以按落地混用，不用全 fleet 一个答案。

**本节未核实项：** 官方 sing-box 镜像不带 Clash/V2Ray API 按调研报告（官方文档）写成，未拉取镜像实测验证；宿主机 iptables 读数在 sing-box 落地上的做法沿用 A 的采样纪律，未实施验证。

## 10. 安全：agent 与面板的认证（主方案复用 rpt_ 令牌机制）

### 10.1 主方案：每节点一个令牌，复用 central 的 rpt_ 机制

- 每个落地一个节点令牌：格式与集中管理的远程令牌相同（`rpt_` + 64 位随机十六进制——或新前缀 `nag_`，实施时定；格式不重要，机制重要）。
- 存储：面板只存令牌的 SHA-256 哈希，明文只在入网时显示一次；比对用 ConstantTimeCompare；同 IP 连续 5 次失败锁 5 分钟。——这就是 1.2.0 已实施并审计过的 central 令牌机制，不新发明一套。
- 入网：一次性入网令牌（面板生成、管理员在落地机上跑 `node-agent join <令牌>`，用完即弃）；入网后 agent 得到长期节点令牌（节点 id + 令牌），写进 `/etc/node-agent/config.json`（600 权限）。
- 呈现：agent 每次拉取/推送都在 HTTPS 头里带 `Authorization: Bearer <节点令牌>` + `X-Node-Id`；面板验哈希、验节点 id 与令牌是否匹配。

**为什么不用 mTLS（面板当 CA）做主方案：** mTLS 是更干净的长期形态（Remnawave 3.x 同款，面板当 CA、发证轮换吊销），v1 §6 也写了“最好是 mTLS”。但面板现在没有任何 CA 基础设施（签发、轮换、吊销、证书存储全要新写），而 rpt_ 机制已经实现并跑了一年。先复用令牌把计量跑通，mTLS 列为 Phase 3 升级（§11），不要为认证阻塞计量。

### 10.2 其他安全规则

- **Xray API 只绑 loopback（§2.2 第 4 块），绝不暴露公网。** agent 是唯一的调用方；面板、管理员、中转机都不直接连 10085。
- **agent → 面板必须走 HTTPS。** 面板须开 HTTPS（1.3.14 起面板支持真证书 + 自动续签）；如果面板还是明文 HTTP，节点令牌在传输中可被嗅探。入网的前提条件就是面板已开 HTTPS，未开不许入网。
- **agent 以 root 运行**（要读 iptables、要 `docker restart` Xray 容器、要写 `/etc/node-agent/`）。接受这一点，代价写明。
- **agent 被攻破的影响面：只限它自己那台落地。** 能做的事：增删该落地 Xray 的用户、读该落地的流量、重启该落地的 Xray。不能做的事：碰面板的管理账号、碰其他落地、碰 DNS 令牌、碰面板的 ACME 凭证（这些东西永远不发到落地，v1 §5 的分离规则）。
- **吊销：** 面板删节点令牌 → agent 拉取/推送立即 401；落地继续按上次用户表服务（fail-open），直到管理员上机处理。令牌轮换 = 重新入网（发新入网令牌、agent 重新 join）。
- **agent 不存面板的任何凭证：** 只有它自己的节点令牌；日志里不打令牌、不打 UUID（UUID 是用户凭证，日志只记 `email` 与用量）。

**本节未核实项：** 节点令牌前缀（沿用 `rpt_` 还是新开 `nag_`）未定，实施时定；mTLS 的 CA 基础设施是 Phase 3 的工作，本方案不展开。

## 11. 分阶段：每阶段可独立验证、可回滚

| 阶段 | 做什么 | 不做什么 | 验收（过哪条才算完） | 回滚 |
|---|---|---|---|---|
| **Phase 0——只读 sidecar，先数节点总量** | 落地装 agent（systemd）；改 Xray 配置加 §2.2 四块 + 容器重启一次；agent 每 10 秒读 `inbound>>>` 总量 + `generic@node`，每 60 秒推给远程面板；面板显示该落地总量（含直连） | 不建每人 UUID；不改订阅链接；不执法 | 面板上的落地总量开始涨（含直连流量）；agent 在线、心跳正常；Xray 重启后总量不断（基线规则生效） | 停 agent、删 Xray 配置里的四块、重启容器，回到现状 |
| **Phase 1——每人建用户 + 计量（一个落地试点）** | 面板给试点落地的测试订阅生成每人 UUID；agent 经 `AlterInbound` 下发；`QueryStats` 按人计量并推送；面板显示每人用量 | 不执法（超量也不停用，只显示、只提醒）；不迁真实用户 | §14 第 1、4、5、8 条通过（A 拉 5MB/B 拉 0MB 归属正确；重启不丢不重；面板离线 5 分钟补推对上；多用户不串户） | 删测试用户的 UUID，回到 Phase 0 |
| **Phase 2——执法** | agent 巡查 + 删用户 + 重启 Xray（§5.2）；面板 80%/100% 决策进拉取的用户表；真实用户迁移（§7.2，一次重新生成） | 不动其他落地 | §14 全部通过；真实用户迁移后直连流量进每人用量 | 回滚见 §7.3 |
| **Phase 3——sing-box 定夺 + 加固** | 按落地定 sing-box 路线（§9.2）；mTLS 升级（§10.1）；长连接流做紧急吊销（拉取留作 fallback）；推广到全部落地 | — | 全部落地 `metering_source=agent` 或明确标注原因；紧急吊销 <10 秒（若做了流） | 按落地回滚到 Phase 2 状态 |

**阶段之间不跳步：** Phase 0 的总量对不上，就不要进 Phase 1；Phase 1 的归属测不对，就不要迁真实用户。每个阶段的验收都是真实流量，不许只用种子模拟（1.3.25 的教训）。

**本节未核实项：** 各阶段的工作量未估（须实施排期时拆任务再估）；Phase 3 的长连接流是 v1 已有的方向，未细化。

## 12. 性能与资源（相对现在落地的代价）

|  | 现在（无 agent） | 装 agent 后（估计，未实测） |
|---|---|---|
| agent 内存 | — | 约 10–30MB RSS（Go 运行时 + 用户表 + 账本；wrapper 本体几到几十 MB，调研报告 §5） |
| agent CPU | — | 每 10 秒一次 `QueryStats`（一次 gRPC 返回全部用户计数器）+ 每 60 秒一次推送；几十个用户下可忽略 |
| Xray 内存 | 基准 | 每用户每方向一个计数器（int64）+ OnlineMap 条目（20 秒过期）；几十个用户下增加不到 1MB |
| Xray CPU | 基准 | 每 `MultiBuffer` 一次原子加法（lock-free，Xray internals 文档原话：“no mutex locking… extremely fast”）；几十个用户、百兆级带宽下可忽略 |
| 磁盘 | — | 账本 `/etc/node-agent/ledger.json` 每次快照原子写一次；几十个用户约几十 KB |
| VPS 预算 | — | agent + Xray 在 512MB–1GB 的 VPS 上宽裕（XrayR 文档 512MB 最低要求；3x-ui 整机实测 68–74MiB 待机，调研报告 §5，厂商测量） |

**须实测的指标清单（实施时跑）：** agent 常驻内存（RSS）与 CPU 占用；`QueryStats` 全量返回的耗时（10/100/500 用户三档）；Xray 开 stats 前后的 CPU 对比（同带宽、同用户数）；容器重启耗时（§5.2 的闪断时长）；推送失败重推时面板的去重表现。

**本节未核实项：** 本节全部数字是估计，未实测；3x-ui 的 68–74MiB 是第三方测量（Mistgate），厂商数据；XrayR 的 512MB 是文档最低要求，不是实测。

## 13. 故障模式（逐条：现象、应对）

| # | 故障 | 现象 | 应对 |
|---|---|---|---|
| 1 | 面板离线（远程面板或管理面板） | agent 拉不到新用户表、推不上去 | agent 按本地账本 + 最后同步的配额继续执法（fail-open）；增量排队在本地账本（spool），恢复后重推；面板恢复后以面板的配额与 `next_reset_at` 为准 |
| 2 | agent 重启（崩溃/升级/机器重启） | 内存里的基线与未推增量丢失 | 账本已落盘每次快照；启动时读账本恢复基线与已确认用量，只推重启后增量；倒退的计数器按 0 算、绝不重加（§4.2） |
| 3 | Xray 重启（容器重启/机器重启） | Xray 内存计数器清零 | 同 §4.2 第 2 条：计数器倒退按 0 算、重定基线；已推的增量在面板侧不受影响 |
| 4 | 推送失败 / 重试 double-count | 面板收到重复增量，用量虚增 | staging + ACK 后才清暂存，失败加回（§4.4）；面板侧按 `(user_id, period_id, seq)` 去重 |
| 5 | 时钟偏差（agent 与面板时钟不同） | 重置点判断错位 | 重置点由面板算（`next_reset_at`），agent 只执行；翻周期幂等（`last_reset_at` compare-and-set）；VPS 时钟用 NTP 同步，偏差秒级不影响按月边界 |
| 6 | 面板删了用户，落地还在服务 | 用户已被停用，但落地 Xray 里还有他 | 下次拉取（≤60 秒）用户表里没他，agent 经 `AlterInbound` 删掉；巡查每 10 秒也会发现他不在表里并删掉；已建立的连接按 §5.2 处理（重启 Xray 才断） |
| 7 | agent 崩溃在快照与落盘之间 | ≤10 秒的增量没了 | 窗口上限写明（≤10 秒），可接受；优雅停机时先刷一次快照 + 落盘，把窗口清掉 |
| 8 | Xray API 挂了（10085 连不上） | agent 读不到数、下发不了用户 | agent 报警（推送里带健康状态 `xray_api_down`），面板把该落地标红；用户表与流量都停在最后一次成功的状态（fail-open：不断网）；管理员上机查容器 |
| 9 | 节点令牌泄露/被吊销 | agent 拉取/推送 401 | 面板吊销令牌 → agent 停在最后用户表服务；重新入网发新令牌；泄露的令牌只能动这一个落地（§10.2） |
| 10 | 两个 agent 抢一个落地 | 两个 agent 各下一份用户表，互相覆盖 | 禁止：一个落地只允许一个 agent 入网；面板收到第二个入网请求时拒绝，除非先吊销旧令牌 |

**本节未核实项：** 无。本节是设计应对（市面机制同款），待实施验证。

## 14. 测试验收标准（必须含真实流量端到端验证）

1.3.25 的教训：e2e 只用种子模拟超量，真机实测才暴露计量链路断了。本方案的验收不许只用种子模拟，必须含真实流量：

1. **归属正确（核心项）：** A 用户拉 5MB 直连流量、B 用户拉 0MB，面板显示 A=5MB、B=0MB（误差 <1%，口径按 §4.6 的负载字节算）；再让 A 走中转拉 5MB，面板显示 A=10MB（直连 + 中转合并，不 double-count）。
2. **节点总量：** Phase 0 验收：落地总量在面板上可见且随真实流量涨，与中转侧总量 + 直连流量之和对得上（误差 <2%，口径差除外）。
3. **超量停用：** 建 1MB 订阅拉 2MB，超量后该用户新连接被拒、正在传的连接被断（重启 Xray 后），管理面板标红已停用；同落地其他用户（5GB 订阅）只闪断一下、不断线。
4. **重启不丢、不重：** agent 重启后用量不变（账本恢复），再拉 1MB 用量涨 1MB；Xray 容器重启后用量不变，再拉 1MB 涨 1MB，不 double-count、不丢数。
5. **面板离线：** 管理面板离线 5 分钟（agent 与远程面板继续跑），期间用量继续涨；恢复后增量补推上去，用量对得上。
6. **80% 提醒：** 用到 80% 时 Telegram 提醒一次，不重复发。
7. **重置钳制：** 自定义几号 = 31 号的订阅，用测试时间（或改 `next_reset_at`）模拟 2 月，确认重置落在 2 月最后一天、下月回到 31 号。
8. **多用户并发不串户：** 10 个用户同时拉流量，各自用量独立；A 用户的流量一分钱不算到 B 头上。
9. **通用订阅总量：** 通用订阅拉 5MB，`generic@node` 总量涨 5MB，不归属到任何订阅者。
10. **回滚：** 切回 A 后用量连续（账本同一份），再拉 1MB 用量涨 1MB；直连链接回滚后仍可用。
11. **回归：** 真 mihomo `-t`（须带 `-d`）与真 sing-box `check` 通过；go test 全套 + jsdom + e2e 真二进制通过；订阅链接在 FlClash/小火箭/v2rayNG 三端导入正常。

**本节未核实项：** 无。本节是验收标准，待实施时执行。

## 15. 待用户拍板的问题清单（不要替用户定）

1. **下发方式确认：** 主方案 gRPC `AlterInbound` 免重启（§3.3）是否接受？还是要求全部走配置重载（更简单、但每次增删用户都闪断）？
2. **停用闪断确认：** 停用一个用户要重启 Xray、该节点其他用户闪断约 1–2 秒（§5.2），是否接受？还是宁可自然排空（限流不立即生效）？
3. **试点落地选哪个：** 第一个装 agent 的落地是哪一个？
4. **推送节奏：** 快照 10 秒、推送 60 秒、立即推（跨 80%/100% 时）是否接受？还是放宽到 60 秒/60 秒（与 A 相同）？
5. **sing-box 路线：** 先只数总量（§9.2 主方案），还是直接定某几个落地换 Xray 内核？
6. **Xboard 范围：** Xboard 落地保持现状（§8），是否接受？还是要去查 Xboard 的 API 看能不能桥接？
7. **实施版本：** 做到哪个面板版本（1.3.29 起，还是更后）？先做单面板还是直接做集中管理（agent 向远程面板汇报、远程面板向管理面板汇总是两级，集中管理是默认形态）？
8. **A 退役节奏：** 装上 agent 的落地，A 每人转发是迁移时一次退役（§7.2），还是先留着过渡一段时间（过渡期直连与中转各一份计量，面板按 §4.5 去重）？
9. **通用订阅：** 旧共用 UUID 永久保留（§3.4），是否接受？还是以后要废掉通用订阅、全部转一人一条？

**本节未核实项：** 无。本节是待定问题清单，问题本身就是未定项。

## 16. 来源

- 轻量节点后端方案 v1：`~/workspace/goals/relay-node-one-click-installer/files/node-backend-plan.md`（Phase 0 sidecar、统一模型、三信任域、分阶段）
- 方案 v2：`~/workspace/goals/relay-node-one-click-installer/files/per-user-traffic-plan.md`（市面机制：增量推送、先暂存确认后再清、`next_reset_at`、DelUsers+CloseAll）
- 方案 B v3：`~/workspace/goals/relay-node-one-click-installer/files/per-user-traffic-plan-b-v3.md`（A/B 的局限、与节点后端的关系、B 的价值窗口）
- 市面调研报告（按人流量）：`~/workspace/research_notes/per-user-traffic-panel-research-20261120-0228/report.md`（V2board/Xboard/SSPanel/V2bX/NodeRS-AnyTLS/Xray stats，2026-11-20）
  - Xray stats：https://xray-internals.hidandelion.com/advanced/stats.html（计数器命名、两道门、`email` 规则、`SizeStatWriter` 口径、`reset:true` 的 delta 纪律、3x-ui 基线 fix——**2026-11-21 刚复核过原文**）
  - V2bX 流量上报：https://deepwiki.com/wyx2685/V2bX/6.3-traffic-reporting（增量推送、`ReturnUserTraffic`）
  - V2bX 节点生命周期（`DelUsers` + `CloseAll()`）：https://deepwiki.com/wyx2685/V2bX/4.1-node-lifecycle-management
  - NodeRS-AnyTLS：https://deepwiki.com/moeclubm/noders-anytls/1.2-key-features（`AtomicU64` 按用户、约 10KB 阈值、推送失败加回、删用户取消会话）
  - 3x-ui 流量统计（10 秒轮询）：https://deepwiki.com/saintmaur/x-ui/4.4-traffic-accounting
  - Xboard 重置字段与每分钟任务：https://github.com/guoweidong1987/xboard/blob/HEAD/CLAUDE.md
  - NX 面板 31 号钳制：https://docs.nx-panel.org/use/users
  - uniproxy 增量定义（重放会重复记账）：https://pkg.go.dev/github.com/GoAsyncFunc/uniproxy/pkg
- 节点后端调研报告：`~/workspace/research_notes/lightweight-node-backend-research-20261120-0257/report.md`（V2bX/v2node/ppanel-node/Remnawave/Marzban 形态、Xray gRPC、证书，2026-11-20）
  - Remnawave 节点架构：https://deepwiki.com/remnawave/panel/2.2-node-architecture（面板推全量 Xray JSON + 证书、mTLS、面板当 CA）
  - sing-box experimental API：https://github.com/MetaCubeX/sing-box/blob/HEAD/docs/configuration/experimental/index.md（Clash API / V2Ray API，均需编译标签）
  - ppanel-node：https://deepwiki.com/wyx2685/ppanel-node（build tag 瘦身、Lego）
- 生产实测（2026-11-21）：A 在 1.3.25 上线（用户真机：三条 1GB 订阅用量 0 B / 1.69 KB / 1.68 KB）；1.3.27 三处 bug 与修法（采样在清链后、首见定基线丢数、空壳记录遮挡聚合缓存）；1.3.27 e2e 真实流量验证（经用户端口拉 5MB，用量涨到 5MB、超量后远程转发被撤）；直连不计量（`rewriteSubLinksForUser` 只改“-中转”结尾的链接，直连不经过中转机，A 的设计限制）
- 代码核实（2026-11-21，grep 实测）：落地 `tiktok-node-kit/install.sh` 无 `stats`、无 `api` 入站、无 `policy` 统计开关（唯一命中是 DNS 的 `nameserver-policy`）；`"clients": [ { "id": "$NODE_UUID" } ]`（第 407 行，一个节点一个 UUID）；`sing-box-node-kit/install.sh` 无 stats/experimental 配置；Xray 配置挂载为只读（`./config.json:/usr/local/etc/xray/config.json:ro`），多节点为 `config-2.json` / `tiktok-xray-2`。

> 未核实项汇总（全文档）：§2.2 四块配置未在本机真实 Xray 上跑过；`127.0.0.1:10085:10085` 的 loopback 映射写法未在真实 Docker 上验证；`AlterInbound` 增删用户未在本机实测；`RemoveUser` 不杀已建连接未在本机实测；停用重启 Xray 的闪断时长（约 1–2 秒）未实测；`QueryStats` 全量返回在用户数上百时的性能未实测；L3 与负载字节的具体差值百分比未实测；Xboard 是否有可供外部调用的按人流量 API 未核实；官方 sing-box 镜像不带 Clash/V2Ray API 未拉取镜像实测验证；宿主机 iptables 读数在 sing-box 落地上的做法未实施验证；各阶段工作量未估；`node_uuids` 字段名、节点令牌前缀、agent 二进制名 `node-agent` 与配置路径（本方案定的，未实现）；mTLS 的 CA 基础设施是 Phase 3 工作，未细化。

**本节未核实项：** 见本节末尾的未核实项汇总。
