# Octopus Accounts 导入

实现：`internal/config/import_octopus.go`
CLI：`newapi-checkin import ...`

## 1. 输入格式

常见为 Octopus / AionUi 导出的 `accounts-backup-*.json`：

```json
{
  "version": "2.0",
  "type": "accounts",
  "timestamp": 0,
  "accounts": {
    "accounts": [
      {
        "id": "account-...",
        "site_name": "站点名",
        "site_url": "https://example.com",
        "site_type": "new-api",
        "disabled": false,
        "authType": "access_token",
        "account_info": {
          "id": "123",
          "access_token": "...",
          "username": "..."
        },
        "cookieAuth": {
          "sessionCookie": "session=..."
        },
        "checkIn": {
          "autoCheckInEnabled": true,
          "enableDetection": true
        }
      }
    ]
  }
}
```

### 兼容外形

`extractOctopusAccounts` 依次尝试：

1. `accounts.accounts[]`（标准备份）
2. 顶层 `accounts[]`（扁平）
3. 根即为账号数组

## 2. CLI 参数

| 参数 | 默认 | 说明 |
|------|------|------|
| `-from` | （必填） | 备份 JSON 路径 |
| `-out` | `config.yaml` | 输出 YAML |
| `-include-disabled` | false | 导入 `disabled: true` |
| `-require-auto-checkin` | false | 仅 `checkIn.autoCheckInEnabled == true` |
| `-timeout` | 30 | 写入 `timeout_seconds` |

## 3. 过滤与跳过原因

按账号顺序处理，跳过时记录可读原因（CLI 打印 `skipped`）：

| 条件 | 默认行为 | 原因文案示例 |
|------|----------|----------------|
| `disabled` 且未开 include | 跳过 | `name: disabled` |
| require-auto-checkin 且未开启 | 跳过 | `name: auto check-in disabled` |
| 不支持的 `site_type` | 跳过 | `name: unsupported site_type "sub2api"` |
| 空 `site_url` | 跳过 | `name: empty site_url` |
| 无 token 且无 session cookie | 跳过 | `name: no access_token/session cookie` |

全部跳过 → 返回 error（`no importable sites found`）。

## 4. 字段映射

| 源字段 | 目标 | 备注 |
|--------|------|------|
| `site_name`（空则 `id`） | `name` | |
| `site_url` | `base_url` | trim 尾 `/` |
| `site_type` | `platform` | 见映射表 |
| `account_info.access_token` | `credential_type: access_token` + `access_token` | |
| `cookieAuth.sessionCookie` | `credential_type: session_cookie` + `session_cookie` | 必须为 `name=value` |
| `account_info.id` | `user_id` | 支持 string/number |

### site_type → platform

| site_type | platform |
|-----------|----------|
| `new-api` | `new-api` |
| `anyrouter` / `any-router` | `any-router` |
| `one-api` | `one-api` |
| `veloera` | `veloera` |
| `done-hub` / `donehub` | `done-hub` |
| `new-api-like` | `new-api-like` |
| `unknown` / 空 | `new-api-like` |
| 其他（如 `sub2api`） | **不导入** |

### 凭证解析 `resolveOctopusCredential`

- `authType == cookie`：优先生成 `session_cookie`；缺少 Cookie 时回退为 `access_token`
- 其他：优先生成 `access_token`；缺少 token 时回退为 `session_cookie`

导入结果保留明确的凭证类型，签到层不再根据凭证字符串内容猜测 Authorization 或 Cookie。

## 5. 公开 API

```go
type OctopusImportOptions struct {
    IncludeDisabled    bool
    RequireAutoCheckIn bool
    TimeoutSeconds     int
}

func ImportOctopusFile(path string, opts OctopusImportOptions) (*ImportResult, error)
func ImportOctopus(data []byte, opts OctopusImportOptions) (*ImportResult, error)

type ImportResult struct {
    Config   *Config
    Imported int
    Skipped  []string
}
```

## 6. 实测参考（会话记录）

某次 `accounts-backup-2026-07-18.json`：

- 导入 **25** 个未禁用且有凭证的兼容站
- 跳过 **15** 个 disabled 账号
- 配置可通过 `config.Load` 校验

数字随备份变化，仅作行为参考。

## 7. 未实现（可选增强）

- 与已有 YAML 按 `base_url` merge / 覆盖策略
- 导入时名称过滤
- 将 `authType` 映射为显式 `username_password`（备份通常不带密码）
