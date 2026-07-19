# 签到实现细节

实现目录：`internal/checkin/`
入口：`checkin.Run` / `checkin.RunWithOptions(ctx, site, Options) Result`

## 1. 总流程

```text
RunWithOptions(site, opts)
  →  1. supportsCheckin(platform)? 否则 Error
  →  2. 若 credential_type == username_password:
         login → accessToken, userID
  →  3. accessToken 为空 → Error
  →  4. userID <= 0:
         discoverUserID via GET /api/user/self（无 User 头）
  →  5. 仍无 userID 时：
         探测站点是否强制 New-Api-User；若强制则返回明确错误提示
  →  6. checkinSite(token, userID, opts)
         a. GET /api/user/checkin?month=YYYY-MM 状态查询
            - checked_in_today / 已签到文案 → 成功（奖励 0）
            - captcha_enabled → captcha 流程
         b. 否则 POST（失败再试 GET）/api/user/checkin 无 body
            - 响应若为状态形态 → 不当作签到成功
            - 文案提示需要验证码 → captcha 流程
         c. captcha 流程：
            POST /api/user/checkin/captcha
            → opts.SolveCaptcha（交互或 -captcha-cmd）
            → POST /api/user/checkin {captcha_id, captcha_answer}
            → 可选再 GET 状态核对 checked_in_today
  →  7. 签到后请求 GET /api/user/self 获取当前总余额
  →  8. 返回签到时间、成功状态、奖励和总余额
```

## 2. API 约定

| 步骤 | 方法 | 路径 | 说明 |
|------|------|------|------|
| 登录 | POST | `/api/user/login` | body: `username`, `password` |
| 用户信息 | GET | `/api/user/self` | 取 id；探测是否要 User 头；读取签到后余额 |
| 签到状态 | GET | `/api/user/checkin?month=YYYY-MM` | 解析 `checked_in_today` / `captcha_enabled` |
| 取验证码 | POST | `/api/user/checkin/captcha` | 返回 `captcha_id` + 图片（data URL / base64） |
| 签到 | POST（必要时 GET） | `/api/user/checkin` | 无验证码时空 body；图片验证码时提交 captcha 字段；Turnstile 时用 `?turnstile=` |
| 站点公开状态 | GET | `/api/status` | 读取 `turnstile_check` / `turnstile_site_key` |

URL：`base_url` 去尾 `/` 后拼接路径。

## 3. 鉴权变体

`buildAuthHeaderVariants` 根据显式 `credential_type` 生成请求头：

- `access_token`：尝试 `Authorization: <raw token>` 和 `Authorization: Bearer <token>`
- `session_cookie`：只发送 `Cookie: <session_cookie>`
- `username_password`：登录响应的 token 字段映射为 `access_token`，session 字段映射为 `session_cookie`

运行时不再根据 `=`、前缀或字符串形态猜测凭证类型。`session_cookie` 必须在配置边界满足完整的 `name=value` 格式。

**不**对 user_id 做大范围爆破（避免无意义请求与封禁风险）。

## 4. New-Api-User

`managedUserIDHeaders(userID)` 在 `userID > 0` 时设置常见头名（如 `New-Api-User` 等，见代码）。

配置中的 `user_id` 应与 token 所属账号一致；不匹配时站点可能返回 “does not match logged in user”。

## 5. 成功判定（重要）

**状态查询 ≠ 签到成功。**

`GET /api/user/checkin` 若返回状态形态（任一成立）则按状态处理，**不得**因 `success: true` 记为签到成功：

- `data.checked_in_today` / `data.captcha_enabled`
- `data` 含日历/历史字段
- `message` 含「查询成功」

动作成功：`interpretCheckinActionPayload`

- `payload.success == true` 且非纯状态文案 → 成功
- 或消息匹配「已签到」类文案 → **仍记为成功**（幂等日签）

已签到关键词示例（中英）：

- `already checked in` / `checked in today`
- `今日已签到` / `已经签到` / `已签到` / `重复签到` 等

Reward 尝试从 `data.quota_awarded` / `data.quotaAwarded` / `data.reward` / `data.amount` 读取（不从状态查询的日历字段猜奖励）。

## 6. 图片验证码

`Options.SolveCaptcha`：

| 来源 | 行为 |
|------|------|
| CLI TTY 默认 / `-captcha-interactive` | `InteractiveCaptchaSolver`：存图、可选打开、stdin 输入 |
| `-captcha-cmd` | `CommandCaptchaSolver`：跑外部命令，stdout 首行答案 |
| 均未提供 | 需验证码的站点失败，错误提示如何启用 |

外部命令约定：

- 支持 `{image}` 占位符
- 或将图片路径作为最后一个参数
- 示例：`python scripts/solve_captcha.py {image}`（可选依赖 `ddddocr`）

人工/OCR 等待使用独立 5 分钟超时（`context.WithoutCancel` + `captchaSolveTimeout`），与站点 HTTP `-timeout` 分离。

## 6.1 Cloudflare Turnstile（人机验证 / trusted token）

NewAPI 路由：`POST /api/user/checkin` + `middleware.TurnstileCheck()`。

中间件逻辑（上游）：

1. 若 gin session 已有 `turnstile` → 放行
2. 否则读 query：`c.Query("turnstile")`，空则返回 `Turnstile token 为空`
3. 向 Cloudflare `siteverify` 校验；成功则 `session.Set("turnstile", true)`

本工具：

| 来源 | 行为 |
|------|------|
| `-turnstile-token` | 直接使用一次性 token |
| `-turnstile-cmd` | `CommandTurnstileSolver`，stdout 首行 token |
| TTY 交互 | `InteractiveTurnstileSolver` 提示粘贴 |
| session 已验证 | 普通 POST 可能直接成功（无需 token） |

占位符：`{sitekey}` `{url}` `{base_url}` `{site}`。
示例：`python scripts/solve_turnstile.py {sitekey} {url}`（需 CapSolver/2captcha API key）。

检测到 `Turnstile token 为空` / `Turnstile 校验失败` 时走 Turnstile 流程，**不会**误进图片 captcha 流程。

## 7. 奖励与总余额

- NewAPI quota 按 `500000 quota = $1` 换算。
- `new-api` / `any-router` / `veloera` / `done-hub` / `new-api-like`：`data.quota` 视为当前剩余额度。
- `one-api`：当前余额为 `data.quota - data.used_quota`，最小为 0。
- “今日已签到”本次奖励记为 `$0.00`。
- 缺少奖励字段时奖励显示 `不可用`；余额查询失败时总余额显示 `不可用`，并保留 `BalanceError`，不伪装为 0。

## 8. HTTP 客户端行为（`http.go`）

- 使用环境代理：`http.ProxyFromEnvironment`
- 默认 UA、Accept、JSON Content-Type
- 自动补 `Origin` / `Referer` 为站点 origin（部分站校验）
- 合并 `site.Headers`
- Cloudflare 等拦截有简单识别，便于错误信息

超时由调用方 `context.WithTimeout` 控制（CLI 用 `timeout_seconds` 或 `-timeout`）。

## 9. 登录响应 token 提取

`login` 从多种字段尝试读取 token：

- `data.token` / `data.access_token` / `data.accessToken` / `data.session`
- 顶层 `token` / `access_token`

用户 ID 用 `extractUserID` 多路径提取（`data.id`、`data.user_id` 等）。

## 10. Result 与 CLI 展示

```go
type Result struct {
    Site            string
    CheckedAt       time.Time
    Success         bool
    Message         string
    Reward          string
    RewardUSD       *float64
    TotalBalanceUSD *float64
    BalanceError    string
    Error           string
}
```

CLI 对成功打印 `OK` + message（可能附带 `user_id=`）；失败打印 `FAIL` + error。每个站点随后输出：

```text
[2026-07-18 22:30:01] 站点="示例站点" 签到成功=是 本次获得=$0.005 总余额=$2.50
```

签到模式默认将控制台输出追加写入 `checkin.log`，可通过 `-log` 覆盖路径。日志文件在任何真实签到请求之前打开，打开失败则不会执行签到。

全站结束后：`success=N fail=M`，`fail>0` 则 exit 2。

## 11. 修改建议

- 新站点仅路径不同：优先在现有循环中加 path 变体，并加注释说明来源站。
- 新成功文案：扩展 `isAlreadyCheckedInMessage`。
- 新平台：先在 `types.go` 的 `supportsCheckin` 注册，确认 API 是否兼容再导入映射。
- 验证码字段名差异：扩展 `parseCaptchaPayload`。
