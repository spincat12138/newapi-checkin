# 配置说明

## 1. 文件

| 文件 | 说明 |
|------|------|
| `config.example.yaml` | 无密钥示例，可提交 |
| `config.yaml` | 本地真实配置，**gitignore** |
| 导入输出 | 默认 `-out config.yaml`，可用其他路径 |

## 2. Schema

```yaml
timeout_seconds: 30          # 单站 context 超时（秒），<=0 时 Load 归一为 30

sites:
  - name: "展示名"
    base_url: "https://example.com"   # 必填，Load 时去尾部 /
    platform: "new-api"               # 见下表；空则默认 new-api
    credential_type: "access_token"   # access_token | session_cookie | username_password
    access_token: "..."               # credential_type=access_token 时必填
    session_cookie: "session=..."     # session_cookie 时必填；与 access_token 二选一
    username: "..."                   # username_password 时必填
    password: "..."
    user_id: 1                        # 推荐填写；对应 New-Api-User
    headers:                          # 可选，合并进请求
      X-Custom: value
```

### platform

| 值 | 说明 |
|----|------|
| `new-api` | 标准 NewAPI |
| `any-router` | AnyRouter |
| `one-api` | One API |
| `veloera` | Veloera |
| `done-hub` | Done Hub |
| `new-api-like` | 兼容实现 / Octopus `unknown` 映射结果 |

不在 `supportsCheckin` 列表内的平台，`checkin.Run` 直接失败。

### credential_type

| 值 | 要求 |
|----|------|
| `access_token` | 仅允许非空 `access_token`；运行时只发送 Authorization |
| `session_cookie` | 仅允许非空 `session_cookie`，格式为 `name=value`；运行时只发送 Cookie |
| `username_password` | 非空 `username` + `password` |

三种凭证字段不可混填。空 `credential_type` 时按 `session_cookie`、`access_token`、`username_password` 的字段存在性推断；显式填写更清晰。

## 3. Load 行为（`internal/config/config.go`）

1. 读文件、YAML 反序列化
2. `normalize`：
   - trim 字符串、`base_url` 去尾 `/`
   - platform / credential_type 小写
   - 空 name → `site-N`
   - 校验凭证类型、对应字段和字段互斥关系
3. `sites` 为空 → error

## 4. Save 行为

- 写入文件头注释（说明可由 import 生成）
- 通过 `buildExportNode` 省略空可选字段（无 user_id 不写 0、无 headers 不写）
- 父目录不存在则创建
- 文件模式 `0o600`

## 5. 获取 user_id

多数 NewAPI 部署会校验请求头：

```http
Authorization: <token 或 Bearer token>
New-Api-User: <用户数字 ID>
```

浏览器：登录站点 → F12 → Network → `/api/user/self` 或任意 `/api/user/*` → Request Headers 中的 `New-Api-User`。

导入时一般从备份的 `account_info.id` 写入，可减少手工步骤。

## 6. 与 CLI 的关系

| 参数 | 作用 |
|------|------|
| `-config` | Load 路径（签到） |
| `-log` | 签到日志路径，默认 `checkin.log`，追加写入 |
| `-captcha-cmd` | 图片验证码识别命令；`{image}` 占位或追加图片路径 |
| `-captcha-interactive` | 强制终端人工输入（图片验证码 / Turnstile 粘贴） |
| `-no-captcha-interactive` | 禁用人工输入（批处理需配合 `-captcha-cmd` / `-turnstile-cmd`） |
| `-captcha-dir` | 验证码图片保存目录 |
| `-no-open-captcha` | 不自动打开验证码图片 |
| `-turnstile-token` | 一次性 Cloudflare Turnstile token |
| `-turnstile-cmd` | 获取 Turnstile token 的命令；`{sitekey}` `{url}` `{base_url}` `{site}` |
| `-no-open-turnstile-page` | 交互获取 Turnstile 时不自动打开站点 |
| `-timeout` | 覆盖 `timeout_seconds`（仅运行时，不改文件） |
| `-only` | 名称子串过滤（逗号分隔，大小写不敏感） |
| import-config `-out` | Save 路径 |
| import-config `-timeout` | 写入生成配置的 `timeout_seconds` |
