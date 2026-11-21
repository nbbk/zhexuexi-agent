# 哲学系节点侧 agent（Zhexuexi Agent）

落地机上跑的静态 Go 程序 `node-agent`：读 Xray 统计、按订阅者数流量、每 60 秒推回面板；超量 / 到期时在落地删用户。直连流量不经过中转机，面板的 A 方案数不到直连——这个 agent 就是为了让直连节点也能按人限流。

## 定位

- **本仓只做节点侧。** 客户端（Flutter + sing-box，Win + Android）在另一个仓：[nbbk/zhexuexi-client](https://github.com/nbbk/zhexuexi-client)。
- **一句话架构：** 落地 Xray stats → agent 按人计数（每 10 秒快照、每 60 秒推送）→ 面板汇总 → 超量停用（删用户 + 重启 Xray）。

## 相关仓库

- 客户端：[nbbk/zhexuexi-client](https://github.com/nbbk/zhexuexi-client) —— 哲学系客户端 + 本 agent 的开发流程原来合在一个仓，现已拆开：客户端归它，节点侧归本仓
- 落地节点：[nbbk/landing-node](https://github.com/nbbk/landing-node) —— Xray / sing-box 双核心落地节点，一键安装
- 中转面板：[nbbk/relay-node](https://github.com/nbbk/relay-node) —— 中转 + 管理面板（1.3.29），订阅、分组、按人限流、备份整机迁移
- 本仓：`nbbk/zhexuexi-agent` —— 只做节点侧 agent，不动上面三个仓库

## 文档入口

- [DEV-PLAN.md](DEV-PLAN.md) —— 节点侧按人计量开发流程（先读这份）
- [docs/node-side-metering-plan.md](docs/node-side-metering-plan.md) —— 节点侧按人计量完整方案 v1（17 节，设计依据全文，原样复制）
- 更新日志：[CHANGELOG.md](CHANGELOG.md)

## 状态

阶段 0 已完成（v0.1.0-stage0）：Go 工程骨架 + `QueryStats` 最小验证
（mock Xray 服务上测过）+ 接口审计。`node-agent version` 与
`node-agent doctor` 能跑，`run` 只做快照 + 账本，不推送、不执法。
**未做：** 真 Xray 实测、真实落地、推送（Phase 0）、入网 join、
执法（Phase 2）。开发按 [DEV-PLAN.md](DEV-PLAN.md) 的进度走，A 线约 6–8 周（Phase 0 只数总量 → Phase 1 试点按人 → Phase 2 执法 + 迁移 → Phase 3 sing-box 定夺 + 全落地推广）。

## 用户已拍板（2026-11-21 23:27，流量限制选方案 2）

- 所有节点（中转 + 直连）都要能限流；节点侧计量在前 / 并行，客户端 MVP 在后。
- 每人一个 UUID：现有订阅链接全部变掉，客户端必须重新导入一次。
- 停用 = 删用户 + 重启 Xray，该节点其他用户闪断约 1–2 秒。
- sing-box 落地先只数总量（没有按用户统计接口）。
- 尽量不改现有面板仓库；须改时列清单，等用户拍板。

## 硬性边界

绝不动现有仓库：`landing-node`、`relay-node`、`tiktok-node`、`sing-box-node`、`zhexuexi-client` 一个文件都不要碰。节点侧 agent 的开发全部在本仓里做，现有仓库只作为被对接的上游（读它们的代码 / 文档作依据，改动留在本仓的适配层里；若执行中发现必须改现有仓库才能实现，停下来写进 DEV-PLAN §6“待用户拍板”清单，不要擅自改）。
