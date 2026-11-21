# 更新日志

哲学系节点侧 agent（`node-agent`）的版本更新记录（按版本从旧到新）。

### v0.0.1 建仓（2026-11-21）

- 新仓 `nbbk/zhexuexi-agent` 建仓首版：只推方案与建仓记录，不写应用代码、不打包。
- 首版内容：`README.md`、`DEV-PLAN.md`（节点侧按人计量开发流程，从 `zhexuexi-client` 的 DEV-PLAN §3 A 线 + §5 A 线测试 + §8 A 线进度抽出、单独成文）、`docs/node-side-metering-plan.md`（节点侧按人计量完整方案 v1 复制件，原样，不改内容）、`.gitignore`。
- 建仓原因：节点侧按人计量与客户端分仓做——客户端在 `nbbk/zhexuexi-client`，节点侧 agent 在本仓；现有仓 `relay-node`（main 0566500）、`landing-node`（main 7918512）一个文件都不碰。
- 未做：应用代码、打包、真机验证。
