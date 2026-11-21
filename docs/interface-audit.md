# 接口审计：面板 1.3.29 里 agent 能用什么、缺什么

> 审计对象：`relay-node` 面板 1.3.29 的实际代码（本地副本
> `relay-node-kit-160/panel/`，kit-160 = 1.3.29 基线），只读，
> 一个文件都没改。审计日期 2026-11-22（阶段 0）。
> 结论先行：**拉取与推送的三个新接口一个都没有；认证机制可以直接复用；
> panel-adapter 单独转接做不到推送，必须给面板加新接口（D10），
> 或走一条不推荐的本地文件桥（见 §4）。**

## 1. 有什么：agent 能直接用的

| 接口 / 机制 | 位置（面板代码） | agent 怎么用 |
|---|---|---|
| 认证：`rpt_` 远程管理令牌 | `central.go:41` `generateRemoteToken`（`rpt_` + 32 字节随机数）；`requireCentralToken`（`central.go:206`）：`Authorization: Bearer <令牌>`，与面板存的 SHA-256 哈希比对，同 IP 5 次失败锁 5 分钟，失败记审计（不记令牌本身） | agent 入网后拿到的长期节点令牌就用这套：`node-agent join` 存令牌，之后每次拉取/推送都带 `Bearer`。令牌生成只在面板设置页点一次，明文只返回一次。**不用新发明一套认证。** |
| 读用量：`GET /api/v1/central/user-traffic?key=subuser:su-xxx` | `handlers.go:134`，`central_user.go:136` `handleCentralUserTraffic`（`requireCentralToken`） | agent 可以读回面板汇总的用量（`up_total/down_total/up_period/down_period`、`limit_bytes/limit_mode`、`disabled`、`forwards`、`traffic_error`），用来对账：agent 账本 vs 面板数字。注意：**这是读接口，返回的是面板自己的 `user_traffic.json`（A 方案的中转侧计数），不能用来推数。** |
| 读落地清单与状态：`GET /api/v1/central/status`、`/central/links`、`/central/traffic`、`/central/certs` | `handlers.go:130-132,146-147` | agent 启动时核对自己这台落地还在不在面板上、面板版本够不够（版本门 `min_panel_version = 1.3.29`）。 |
| 订阅生成：`GET /api/v1/central/subscription?landings=1,2&for_user=subuser:su-xxx` | `handlers.go:132`（`subscription.go handleCentralSubscription`） | 按订阅改写中转节点链接端口。agent 不直接调，但用户表里的订阅生成要与它一致（同一套 `for_user` 改写逻辑）。 |
| 下发转发：`POST /api/v1/central/user-forwards`（全量替换）、`POST /api/v1/central/user-forwards/disable`、`.../resume` | `handlers.go:133,135-136`，`central_user.go:60` | **这是中转侧 A 的转发下发，不是节点用户表。** 语义不同，不能复用：它改的是 realm 转发与端口，agent 要的是 Xray 里的每人 UUID。列在这里是防止以后有人误以为“已经有下发接口了”。 |
| 订阅记录字段（用户表的内容来源） | `config.go:252` `SubUser`、`config.go:199` `MergedSub`：`traffic_limit_bytes/mode`、`reset_mode/reset_day`、`next_reset_at/last_reset_at`、`traffic_warned_80/100`、`disabled/disabled_reason`、`expires_at`、`group_id` | 用户表下发时要的字段面板里都有，**缺的只是“下发接口”本身**，不是数据。`normalizeUserLimit`（`user_traffic.go`）的校验规则（limit<=0=不限流；mode 只认 total/monthly；reset 只认 monthly/anniversary/custom；custom 的 ResetDay 1–31）agent 下发前也要照抄。 |
| 用量存储：`user_traffic.json` + `userTrafficStore` | `user_traffic.go:186`（内存 map + 文件，`totals(key)`、`addDelta(key, counters)`），`config.go:545,589` | 面板侧的用量事实来源。agent 推上来的增量最终要进这里（或与它并列的新表，见 §3）。 |
| 采样口径（A 的数怎么来的） | `background.go:40` `sampleUserTraffic()`：iptables/nftables 按端口计数，每分钟采样；`enforceUserTrafficLimits()` 每分钟裁决 | agent 的数是 Xray 负载字节，A 的数是 L3 字节，同一用户装上 agent 后数字会小几个百分点——这是口径变化，不是丢数（DEV-PLAN §3.7 第 4 条）。 |

## 2. 缺什么：三个新接口 + 三个字段（D10 清单）

| 缺的东西 | 为什么必须有 | 没有的后果 |
|---|---|---|
| `GET /api/v1/node/users?node_id=…`（带 ETag，未变 304） | agent 每 60 秒拉用户表：每人 UUID、email、限流、到期、停用状态。没有它，agent 不知道 Xray 里该有哪些用户。 | Phase 1 做不了：agent 只能数总量，数不到人。 |
| `POST /api/v1/node/push`（带 `(user_id, period_id, seq)`，面板按 seq 去重，ACK 后 agent 才清暂存） | agent 每 60 秒推增量。没有它，agent 的数到不了面板。 | Phase 0 做不了：账本只在落地本地，面板看不见直连流量。 |
| `GET /api/v1/node/config?node_id=…` | 下发节点配置：推送间隔、最小 agent 版本、是否执法。没有它，agent 的行为只能写死在本地配置里，面板改不了。 | 面板管不住 agent 的行为；版本协商做不了。 |
| 订阅记录新字段 `node_uuids: map[landing_id]uuid` | 每落地一个 UUID，订阅生成时按 `(订阅, 落地)` 查表拼直连 / 中转链接。 | 没有它，每人 UUID 没地方存，订阅链接拼不出来。 |
| 落地记录新字段 `metering_source`（`agent` / `relay` / `none`） | 同一落地、同一用户只认一个来源的数（DEV-PLAN §3.6 汇总规则）。 | 没有它，面板不知道该信 agent 的数还是 A 的数，会 double-count。 |
| 落地记录 agent 状态（版本、最后推送时间） | 面板按版本标红过旧的 agent；超过 5 分钟没推送标离线、回落到 A。 | 没有它，agent 掉了面板不知道，还在显示旧数。 |

## 3. 转接方案：panel-adapter 能做到哪一步

`services/panel-adapter/`（本仓）的定位是“翻译”，不是“变出接口”：

- **能做的：** 三个新接口一旦在面板里有了，adapter 把 agent 的 push 翻译成面板能接的形状（字段名、去重键、汇总口径），面板改动只改 adapter；版本协商（agent 上报版本号，面板按版本决定下发字段）也可以先放 adapter 里。
- **做不到的：** 没有新接口时，adapter 没有东西可翻译。面板现有接口里没有一个是“写用量”的：`central/user-traffic` 是只读的，`central/user-forwards` 写的是转发不是用量，`central/subscription` 是生成订阅链接的。**所以 adapter 不能代替 D10，只能在 D10 之后减少面板改动的量。**
- **结论：** Phase 0 的推送要落地，必须先拍板 D10（给面板加三个接口 + 三个字段，发哪个面板版本），或接受下面 §4 的临时桥。不要在 adapter 里假装“已经接上了”。

## 4. 临时桥（不推荐，写下来是为了一旦用户问“能不能先不等面板改动”时有据可查）

adapter 跑在面板机上，直接读写面板的 `user_traffic.json` 文件，把 agent 的增量写进去。**不推荐，原因三条：**

1. `userTrafficStore` 是内存 map + 文件：面板启动时 `load()` 一次读进内存，之后只在自己改动时 `saveLocked()` 写回。adapter 直接改文件会被面板下一次保存覆盖掉，增量丢了还不报错。
2. 去重、周期、80%/100% 裁决都在面板进程里（`enforceUserTrafficLimits`），文件桥绕过了它们，面板的裁决读到的是“突然多出来”的数字，停用/提醒会乱。
3. 备份口径：`user_traffic.json` 进整机迁移备份（`backup_sched.go:514`），文件桥写进去的数字格式一旦与面板的期望不一致，恢复时会整个备份失败。

所以：临时桥只作为“面板离线演示”用，绝不上生产。生产路径就是 D10。

## 5. 其他核实到的事实（顺手记下，免得以后再查）

- 面板版本：kit-160 的 `VERSION` 文件是 1.3.29（`main.go:26` 从版本文件读），与 DEV-PLAN 的基线一致。
- 落地 Xray 版本：`landing-node` 的 `install.sh` 用的是 `ghcr.io/xtls/xray-core:latest`（第 535 行），**没有钉死版本**。DEV-PLAN §3.2 要求“Xray 版本钉死 v26.x”，现状是不符的——Phase 1 试点前要先定：是把落地改成钉死版本，还是接受 latest 并在升级前跑验收。已记进 `docs/protocol-tracker.md`。
- 落地 Xray 现在完全不产生统计：`install.sh` 生成的配置里没有 `stats` 段、没有 `api` 入站、没有 `policy` 的统计开关（节点侧方案 §2.1 已 grep 核实）。agent 装上之前，要先给落地 Xray 加四块配置（顶层 `stats`、`policy` 0 级三个开关、`api` 段、`dokodemo-door` 入站只绑 `127.0.0.1:10085`），这是 Phase 0 步骤 2 的事，不是阶段 0 的事。
- 集中管理：管理面板通过 `central/*` 接口汇总远程面板的用量（`central_user.go:635` `enforceCentralUserTraffic` 每分钟），agent 的 push 先到“落地所在的面板”（单面板形态，D7 选项 A），集中管理汇总是后补的，不要在阶段 0 先按两级设计。
