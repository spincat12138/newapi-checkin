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

- 增加独立的 `import-config` CLI 与 `ImportOctopus*` API，避免每次手工转 YAML。
- 默认跳过 disabled、无凭证、不支持平台；可选 include-disabled / require-auto-checkin。
- `Save` 统一写回 YAML，便于导入与后续扩展 merge。

## 实测快照

- 某 `accounts-backup-2026-07-18.json`：导入 25 站，跳过 15 个 disabled。
- `go test ./internal/config` 通过。

## 文档分层（当前）

- `agent.md`：背景、架构、测试、Agent 约定
- `doc/*`：实现细节
- `README.md`：用户快速开始

## 验证码签到（2026-07-19）

- 根因：部分站 `GET /api/user/checkin` 返回 `success:true` +「查询成功」+ `captcha_enabled`，旧逻辑误判为签到成功。
- 修复：状态查询与签到动作分离；状态形态响应不得记为签到成功。
- 流程：`GET` 状态 → 需要时 `POST captcha` → 交互/`-captcha-cmd` 填答案 → `POST checkin` 提交 → 可选再验 `checked_in_today`。
- CLI：`-captcha-interactive` / `-captcha-cmd` / `-captcha-dir` / `-no-open-captcha`；示例 OCR 脚本 `scripts/solve_captcha.py`。

## Turnstile 人机验证（2026-07-19）

- 站点例：`https://cngov.cc.cd`（`turnstile_check: true`）。
- NewAPI：`POST /api/user/checkin?turnstile=<token>`；空 token 返回「Turnstile token 为空」。
- 支持 `-turnstile-token` / `-turnstile-cmd` / 交互粘贴；示例 `scripts/solve_turnstile.py`（CapSolver/2captcha）。
- 与图片 captcha 分流，避免把 Turnstile 当 captcha 图片处理。

## user_id / New-Api-User（2026-07-19，简直了）

- 现象：余额能查到，签到 `401 New-Api-User header not provided`，易被误当成验证码问题。
- 根因：`/api/user/self` 常可不带头，`/api/user/checkin` 强制要 `New-Api-User`。
- 修复：加强 `extractUserID` / 失败后二次探测；缺 id 时明确提示填 `user_id`，并注明与图片验证码无关。

## 假成功再修（2026-07-19，大喵喵/cngov）

- 现象：`OK checkin success` 且 `本次获得=不可用`，实际未签到。
- 根因：
  1. NewAPI 状态里 `checked_in_today` 在 `data.stats` 下，旧解析读不到；
  2. `success:true` + 空 `message` 被默认成 `checkin success`。
- 修复：解析嵌套 `stats`；动作成功必须有 `quota_awarded` / 明确成功文案 / 已签到文案；成功后尽量再查 `checked_in_today` 复核。

## 可选后续（未做）

见 `agent.md` §7。
- 内置 OCR（当前外挂命令，避免 CGO/重依赖）
