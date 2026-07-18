# 演进记录（进展摘要）

非正式 CHANGELOG，供 Agent 与开发者理解“为什么现在是这样”。

## 起源

- 需求：将 Hureru/octopus 中**站点签到**能力独立为小工具，去掉同步/调度/DB 等。
- 实现：Go CLI，`internal/checkin` 参考 sitesync 的 checkin HTTP 约定。

## 配置与站点

- 使用 `config.yaml` 描述多站点；示例见 `config.example.yaml`。
- 多数 NewAPI 站强制 `New-Api-User`，因此配置强调 `user_id`。
- 曾手工从 AionUi/Octopus `accounts-backup-*.json` 提取未禁用账号写入配置。

## 导入功能

- 增加 `import` 子命令与 `ImportOctopus*` API，避免每次手工转 YAML。
- 默认跳过 disabled、无凭证、不支持平台；可选 include-disabled / require-auto-checkin。
- `Save` 统一写回 YAML，便于导入与后续扩展 merge。

## 实测快照

- 某 `accounts-backup-2026-07-18.json`：导入 25 站，跳过 15 个 disabled。
- `go test ./internal/config` 通过。

## 文档分层（当前）

- `agent.md`：背景、架构、测试、Agent 约定
- `doc/*`：实现细节
- `README.md`：用户快速开始

## 可选后续（未做）

见 `agent.md` §7。
