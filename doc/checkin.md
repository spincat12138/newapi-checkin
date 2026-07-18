# 签到实现细节

实现目录：`internal/checkin/`
入口：`checkin.Run(ctx, site) Result`

## 1. 总流程

```text
Run(site)
  1. supportsCheckin(platform)? 否则 Error
  2. 若 credential_type == username_password:
       login → accessToken, userID
  3. accessToken 为空 → Error
  4. userID <= 0:
       discoverUserID via GET /api/user/self（无 User 头）
  5. 仍无 userID 时：
       探测站点是否强制 New-Api-User；若强制则返回明确错误提示
  6. checkinSite(token, userID)
  7. 签到后请求 GET /api/user/self 获取当前总余额
  8. 返回签到时间、成功状态、奖励和总余额
```

## 2. API 约定

| 步骤 | 方法 | 路径 | 说明 |
|------|------|------|------|
| 登录 | POST | `/api/user/login` | body: `username`, `password` |
| 用户信息 | GET | `/api/user/self` | 取 id；探测是否要 User 头；读取签到后余额 |
| 签到 | POST 或 GET | `/api/user/checkin` | 多组合重试 |

URL：`base_url` 去尾 `/` 后拼接路径。

## 3. 鉴权变体

`buildAuthHeaderVariants` 根据显式 `credential_type` 生成请求头：

- `access_token`：尝试 `Authorization: <raw token>` 和 `Authorization: Bearer <token>`
- `session_cookie`：只发送 `Cookie: <session_cookie>`
- `username_password`：登录响应的 token 字段映射为 `access_token`，session 字段映射为 `session_cookie`

运行时不再根据 `=`、前缀或字符串形态猜测凭证类型。`session_cookie` 必须在配置边界满足完整的 `name=value` 格式。

签到循环顺序概念上为：

```text
for auth in variantsForExplicitCredentialType:
  for uid in [configuredOrDiscovered, 0]:  # 0 表示不带 User 头
    for method in [POST, GET]:
      request /api/user/checkin
```

**不**对 user_id 做大范围爆破（避免无意义请求与封禁风险）。

## 4. New-Api-User

`managedUserIDHeaders(userID)` 在 `userID > 0` 时设置常见头名（如 `New-Api-User` 等，见代码）。

配置中的 `user_id` 应与 token 所属账号一致；不匹配时站点可能返回 “does not match logged in user”。

## 5. 成功判定

`interpretCheckinPayload`：

- `payload.success == true` → 成功
- 或消息匹配“已签到”类文案 → **仍记为成功**（幂等日签）

已签到关键词示例（中英）：

- `already checked in` / `checked in today`
- `今日已签到` / `已经签到` / `已签到` / `重复签到` 等

Reward 尝试从 `data.quota_awarded` / `data.quotaAwarded` / `data.reward` / `data.quota` / `data.amount` 读取。

## 6. 奖励与总余额

- NewAPI quota 按 `500000 quota = $1` 换算。
- `new-api` / `any-router` / `veloera` / `done-hub` / `new-api-like`：`data.quota` 视为当前剩余额度。
- `one-api`：当前余额为 `data.quota - data.used_quota`，最小为 0。
- “今日已签到”本次奖励记为 `$0.00`。
- 缺少奖励字段时奖励显示 `不可用`；余额查询失败时总余额显示 `不可用`，并保留 `BalanceError`，不伪装为 0。

## 7. HTTP 客户端行为（`http.go`）

- 使用环境代理：`http.ProxyFromEnvironment`
- 默认 UA、Accept、JSON Content-Type
- 自动补 `Origin` / `Referer` 为站点 origin（部分站校验）
- 合并 `site.Headers`
- Cloudflare 等拦截有简单识别，便于错误信息

超时由调用方 `context.WithTimeout` 控制（CLI 用 `timeout_seconds` 或 `-timeout`）。

## 8. 登录响应 token 提取

`login` 从多种字段尝试读取 token：

- `data.token` / `data.access_token` / `data.accessToken` / `data.session`
- 顶层 `token` / `access_token`

用户 ID 用 `extractUserID` 多路径提取（`data.id`、`data.user_id` 等）。

## 9. Result 与 CLI 展示

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

## 10. 修改建议

- 新站点仅路径不同：优先在现有循环中加 path 变体，并加注释说明来源站。
- 新成功文案：扩展 `isAlreadyCheckedInMessage`。
- 新平台：先在 `types.go` 的 `supportsCheckin` 注册，确认 API 是否兼容再导入映射。
