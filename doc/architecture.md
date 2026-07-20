# 架构说明

## 1. 目标与边界

**目标**：对配置中的多个 NewAPI 兼容站点执行每日签到，并支持从 Octopus/AionUi 账号备份 JSON 生成配置。

**在边界内**

- 读取/写入 YAML 配置
- 解析 accounts-backup JSON 并过滤
- 登录（可选）+ 调用签到 API
- 查询签到后的当前余额并输出结果日志
- 批量串行签到、过滤站点名、超时控制

**在边界外（刻意不包含）**

- 账号余额持久化同步、历史用量统计
- 代理池、浏览器过盾、调度器、数据库
- 非 NewAPI 类站点（如 `sub2api`）

上游参考：Octopus 中与站点签到相关的 HTTP 约定（路径、`New-Api-User`、token/cookie 等），而非完整复制其工程结构。

## 2. 目录结构

交互版本见 [`project-architecture.html`](project-architecture.html)，可按签到链、配置导入链和验证分支筛选，并点击模块查看关键符号与约束。

```text
newapi-checkin/
├── agent.md                 # Agent 入口（背景/架构/测试）
├── README.md                # 用户文档
├── doc/                     # 本目录：实现细节
├── cmd/checkin/main.go      # 签到进程入口
├── cmd/import-config/main.go # 配置导入进程入口
├── config.example.yaml
├── config.yaml              # 本地密钥配置（gitignore）
├── go.mod
└── internal/
    ├── config/              # 配置 + 导入
    ├── checkin/             # 签到运行时
    ├── notification/        # Telegram 表格与 Bot API 发送
    └── report/              # CLI / 通知共享金额格式
```

## 3. 模块职责

### 3.1 `cmd/checkin`

- `config.Load` → 按 `-only` 过滤 → 打开追加日志 → 对每个 site `checkin.Run` → 汇总退出码
- 不包含业务 HTTP 细节

### 3.2 `cmd/import-config`

- 独立解析外部 Octopus / AionUi accounts 备份
- `config.ImportOctopusFile` → 打印 skipped → `config.Save`
- 不依赖签到运行时，可单独构建和执行

### 3.3 `internal/config`

| 符号 | 作用 |
|------|------|
| `Config` / `Site` | 配置模型 |
| `Load` | 读 YAML + `normalize` 校验 |
| `Save` | 写 YAML（省略空可选字段，权限 0600） |
| `ImportOctopus` / `ImportOctopusFile` | JSON → `Config` |
| `OctopusImportOptions` | 是否含 disabled、是否要求 autoCheckIn 等 |

无 `net/http` 依赖，便于单测。

### 3.4 `internal/checkin`

| 符号 | 作用 |
|------|------|
| `Run` | 单站签到编排入口，返回 `Result` |
| `login` | `POST /api/user/login` |
| `checkinSite` | 状态查询 → 普通签到 / 验证码签到 |
| captcha helpers | 图片验证码：取图、解码、2Captcha `ImageToTextTask`、提交 |
| turnstile helpers | Cloudflare Turnstile：2Captcha `TurnstileTaskProxyless`、`?turnstile=` 提交 |
| twocaptcha client | 原生 JSON API：创建任务、轮询结果、统一错误处理 |
| `discoverUserID` | `GET /api/user/self` |
| `fetchAccountBalance` | 签到后通过 `GET /api/user/self` 读取当前余额 |
| `buildAuthHeaderVariants` | 按显式凭证类型生成 Authorization 或 Cookie 请求头 |
| `interpretCheckinPayload` | success / 已签到文案 |

平台常量见 `types.go`（`supportsCheckin`）。

### 3.5 `internal/notification` / `internal/report`

- `report.FormatUSD` 是控制台日志与 Telegram 表格共用的金额格式事实来源。
- `notification.SendTelegram` 在整批签到结束后调用 `sendRichMessage` 发送 Rich Markdown 原生表格，并按 32768 字符和表格行数限制自动分片。
- Telegram 专用 `proxy_url` 在独立 `http.Transport` 上生效，不修改签到包的 HTTP 客户端。
- 网络错误会剥离请求 URL，避免 Bot Token 随错误信息写入日志。

## 4. 运行时数据流

### 4.1 签到

```text
main.runCheckin
  → config.Load(path)
  → filter by -only
  → for each site:
       ctx + timeout
       checkin.Run(ctx, site)
         → [username_password] login → token, userID
         → [userID<=0] discoverUserID (optional)
         → checkinSite: auth × userID → GET status → POST/captcha/GET action
         → fetchAccountBalance: GET /api/user/self
         → Result{CheckedAt, Success, RewardUSD, TotalBalanceUSD, Error}
       print per-site result log
       tee console output → append checkin.log
  → [telegram.enabled] notification.SendTelegram(results)
       → Rich Markdown table → split by 32768 runes / row limit → sendRichMessage via optional proxy
  → exit 0 / 1 / 2
```

### 4.2 导入

```text
import-config.run
  → config.ImportOctopusFile(from, opts)
       → json.Unmarshal 多种外形
       → 逐账号过滤/映射 → []Site
  → config.Save(out, cfg)
  → 打印 imported / skipped 列表
```

## 5. 依赖

| 依赖 | 用途 |
|------|------|
| Go 1.22+ | 语言与标准库 |
| `gopkg.in/yaml.v3` | 配置编解码 |
| 标准库 `net/http` | 签到请求（支持环境变量代理） |

无数据库、无第三方 HTTP 框架。

## 6. 并发与错误模型

- 站点签到目前为 **串行**，避免对公益站并发过高；后续若并行需限流。
- 单站失败不中断后续站点；进程结束时若有失败则 exit 2。
- 日志文件在签到前打开并以追加模式写入；无法打开、同步或关闭时 exit 1。
- 导入：全部被跳过则返回 error；部分跳过仍成功写文件。

## 7. 安全

- `config.yaml` 含 access_token / session_cookie → **gitignore**
- `Save` 使用 `0o600` 文件权限（在支持的 OS 上）
- 日志/文档中避免打印完整 token（CLI 列表仅展示 uid/platform/url）
