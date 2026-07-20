# NewAPI Checkin

[![Build Docker Image](https://github.com/spincat12138/newapi-checkin/actions/workflows/docker.yml/badge.svg)](https://github.com/spincat12138/newapi-checkin/actions/workflows/docker.yml)

从 [Hureru/octopus](https://github.com/Hureru/octopus) 中提取的 **站点签到** 独立工具。

仅保留 NewAPI / AnyRouter 等管理平台站点的签到能力，去掉 Octopus 里的同步、代理池、调度、数据库等无关模块。

**文档**

| 文档 | 用途 |
|------|------|
| [agent.md](agent.md) | Agent / 开发入口：背景、架构、测试方式 |
| [doc/](doc/README.md) | 实现细节（配置、导入、签到流程等） |
| [项目结构图](doc/project-architecture.html) | 可交互浏览模块、调用链与核心约束 |

## 支持平台

- `new-api`
- `any-router`
- `one-api`
- `veloera`
- `done-hub`
- `new-api-like`

## 功能

- 使用 `access_token`、`session_cookie` 或 `username/password` 签到
- 由 `credential_type` 明确选择 Authorization 或 Cookie 鉴权
- 自动探测 `New-API-User` 等用户 ID 请求头
- 识别“今日已签到”为成功
- 输出每个站点的签到时间、成功状态、本次奖励和签到后总余额
- 支持多站点批量签到
- 支持通过 Telegram Bot 发送 Markdown 表格通知
- Telegram 请求可单独配置 HTTP / HTTPS / SOCKS5 代理

## 快速开始

### 1. 准备配置

**方式 A：从 Octopus / ALL API HUB 备份 JSON 导入（推荐）**

导出的备份一般为 `accounts-backup-*.json`，结构含 `accounts.accounts[]`：

```bash
go run ./cmd/import-config -from accounts-backup.json -out config.yaml
```

常用选项：

```bash
# 同时导入已禁用账号
go run ./cmd/import-config -from accounts-backup.json -out config.yaml -include-disabled

# 仅导入开启了自动签到的账号
go run ./cmd/import-config -from accounts-backup.json -out config.yaml -require-auto-checkin
```

导入规则：

| 字段 | 映射到 config.yaml |
|------|-------------------|
| `site_name` | `name` |
| `site_url` | `base_url` |
| `site_type` | `platform`（`anyrouter`→`any-router`，`unknown`→`new-api-like`） |
| `account_info.access_token` | `credential_type: access_token` + `access_token` |
| `cookieAuth.sessionCookie` | `credential_type: session_cookie` + `session_cookie` |
| `account_info.id` | `user_id` |
| `disabled: true` | 默认跳过（可用 `-include-disabled`） |
| 不支持的 `site_type`（如 `sub2api`） | 跳过 |
| 无 token/cookie | 跳过 |

**方式 B：手工编辑**

```bash
copy config.example.yaml config.yaml
```

编辑 `config.yaml`，填入站点信息：

```yaml
timeout_seconds: 30
telegram:
  enabled: false
  bot_token: ""
  chat_id: ""
  proxy_url: ""
sites:
  - name: "我的站点"
    base_url: "https://example.com"
    platform: "new-api"
    credential_type: "access_token"
    access_token: "xxx"
    user_id: 1
    additional_verification: none
```

或使用账密：

```yaml
  - name: "站点2"
    base_url: "https://example2.com"
    platform: "any-router"
    credential_type: "username_password"
    username: "user"
    password: "pass"
    additional_verification: none
```

使用 Session Cookie：

```yaml
  - name: "站点3"
    base_url: "https://example3.com"
    platform: "any-router"
    credential_type: "session_cookie"
    session_cookie: "session=xxx"
    user_id: 2
    additional_verification: none
```

`session_cookie` 必须是完整的 Cookie 请求头值（`name=value`），不要包含 `Cookie:` 前缀。

### 2. 运行

```bash
go run ./cmd/checkin -config config.yaml
```

只签到名称包含关键字的站点：

```bash
go run ./cmd/checkin -config config.yaml -only "站点A,站点B"
```

覆盖超时：

```bash
go run ./cmd/checkin -config config.yaml -timeout 60
```

签到日志默认追加保存到当前目录的 `checkin.log`。指定其他路径：

```bash
go run ./cmd/checkin -config config.yaml -log logs/checkin.log
```

日志文件的父目录需要提前存在；无法打开或写入日志文件时程序返回退出码 `1`。

每个站点完成后会输出一条结果日志：

```text
[2026-07-18 22:30:01] 站点="我的站点" 签到成功=是 本次获得=$0.005 总余额=$2.50
```

控制台中的站点明细、结果日志、余额查询错误和最终汇总会原样追加到日志文件。金额按 NewAPI 的 `500000 quota = $1` 换算。站点未返回奖励或余额接口不可用时显示 `不可用`，并额外打印余额查询错误。

### Telegram Bot 通知

在 `config.yaml` 中配置根级 `telegram`：

```yaml
telegram:
  enabled: true
  bot_token: "123456:替换为 BotFather 提供的 Token"
  chat_id: "-1001234567890"
  proxy_url: "http://127.0.0.1:7890" # 可留空；也支持 https、socks5、socks5h
```

`proxy_url` 只用于 Telegram API，不会改变站点签到请求的网络路径；留空时使用 `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY` 等系统代理环境变量。全部站点执行完成后，程序发送以下列的 Markdown 表格：

```text
| 站点 | 是否签到成功 | 本次签到余额 | 历史总余额 | 备注 |
| --- | --- | --- | --- | --- |
| 我的站点 | 是 | $0.005 | $2.50 | - |
| 失败站点 | 否 | 不可用 | 不可用 | 请求超时 |
```

程序使用 Bot API 10.1+ 的 `sendRichMessage` 和 Rich Markdown 发送 GFM 管道表格，Telegram 客户端会将其渲染为原生表格。表格内容会转义站点名、金额和备注中的 Markdown 特殊字符；超过 32768 字符或安全行数上限时自动拆成多条消息。发送开始、成功或失败状态都会写入控制台和签到日志；通知失败时返回退出码 `1`，Bot Token 不会出现在错误日志中。

协议参考：[Rich Markdown style](https://core.telegram.org/bots/api#rich-markdown-style) / [sendRichMessage](https://core.telegram.org/bots/api#sendrichmessage)。

### 图片验证码签到（如「简直了」jianzhile.vip）

部分站点开启了签到验证码：`GET /api/user/checkin` 只是**状态查询**（`查询成功` / `captcha_enabled`），**不是**签到动作。程序会：

1. 用 `GET /api/user/checkin?month=YYYY-MM` 判断 `checked_in_today` / `captcha_enabled`
2. 若需验证码：`POST /api/user/checkin/captcha` 取图 → 2Captcha `ImageToTextTask` → `POST /api/user/checkin` 提交 `captcha_id` + `captcha_answer`
3. 再查状态确认

这类站点需要在对应配置中设置：

```yaml
additional_verification: CAPTCHA
```

设置 2Captcha API Key 后直接运行：

```powershell
$env:TWOCAPTCHA_API_KEY = "你的 2Captcha API Key"
go run ./cmd/checkin -config config.yaml -only "简直了"
```

验证码图片仅在内存中编码并提交给 2Captcha，不写入本地文件。任务最多等待约 5 分钟，不计入站点 HTTP `-timeout`。

API 协议参考：[Normal CAPTCHA](https://2captcha.com/api-docs/normal-captcha) / [getTaskResult](https://2captcha.com/api-docs/get-task-result)。

### Cloudflare Turnstile 人机验证（如 cngov.cc.cd）

部分 NewAPI 站在 `POST /api/user/checkin` 上挂了 `TurnstileCheck` 中间件。失败时常见文案：

- `Turnstile token 为空`
- `Turnstile 校验失败，请刷新重试！`

这类站点需要在对应配置中设置：

```yaml
additional_verification: Turnstile
```

`Turnstile` 是按需模式：程序先尝试普通签到，只有接口明确返回 Turnstile 拒绝才向 2Captcha 创建 `TurnstileTaskProxyless` 任务。后端临时关闭验证时不会创建任务，也不会产生打码费用。

正确提交方式是 **query 参数**（不是 body 里的 trusted_token）：

```http
POST /api/user/checkin?turnstile=<TurnstileResponseToken>
```

`/api/status` 里可看到 `turnstile_check` 与 `turnstile_site_key`（sitekey 绑定域名，**不能**在 localhost 自己渲 widget 骗过）。

全自动运行：

```powershell
$env:TWOCAPTCHA_API_KEY = "你的 2Captcha API Key"
go run ./cmd/checkin -config config.yaml -only "cngov"
```

若使用已通过验证的 session cookie，普通签到可能直接成功；纯 API Token 通常会触发 2Captcha。程序不接受人工输入、外部求解命令或手工提供的一次性 token。

API 协议参考：[Cloudflare Turnstile](https://2captcha.com/api-docs/cloudflare-turnstile)。

### 3. 编译

```bash
go build -o newapi-checkin.exe ./cmd/checkin
go build -o newapi-import-config.exe ./cmd/import-config
./newapi-import-config.exe -from accounts-backup.json -out config.yaml
./newapi-checkin.exe -config config.yaml
```

## Docker

本地构建并运行：

```powershell
$env:TWOCAPTCHA_API_KEY = "你的 2Captcha API Key" # 配置含 CAPTCHA / Turnstile 站点时需要
docker compose build
docker compose run --rm checkin
```

容器以一次性任务运行，配置以只读方式挂载，签到日志写入宿主机的 `logs/checkin.log`。

使用 GHCR 镜像时设置镜像地址后运行：

```powershell
$env:NEWAPI_CHECKIN_IMAGE = "ghcr.io/spincat12138/newapi-checkin:latest"
$env:TWOCAPTCHA_API_KEY = "你的 2Captcha API Key"
docker compose run --rm --no-build checkin
```

仓库内的 `.github/workflows/docker.yml` 会在以下场景构建并推送 `linux/amd64`、`linux/arm64` 镜像：

- 推送到默认 `main` 分支：发布 `latest` 和提交 SHA 标签
- 推送 `v*` Git 标签：发布对应版本标签
- 在 GitHub Actions 页面手动触发

镜像地址为 `ghcr.io/spincat12138/newapi-checkin:latest`。构建完成后，Actions 任务摘要会同时显示镜像地址、已发布标签和 [GHCR Package 页面](https://github.com/spincat12138/newapi-checkin/pkgs/container/newapi-checkin)。公开拉取前需要在 GitHub Package settings 中将镜像可见性设置为 Public。

## 配置说明

| 字段 | 说明 |
|------|------|
| `timeout_seconds` | 单站请求超时秒数，默认 30 |
| `telegram.enabled` | 是否发送 Telegram Bot 通知，默认 `false` |
| `telegram.bot_token` | BotFather 提供的 Bot Token；启用通知时必填 |
| `telegram.chat_id` | 接收通知的私聊、群组、频道 ID 或 `@channel_username`；启用通知时必填 |
| `telegram.proxy_url` | Telegram API 专用代理；支持 `http`、`https`、`socks5`、`socks5h` |
| `sites[].name` | 站点名称（展示用） |
| `sites[].base_url` | 站点根地址，如 `https://xxx.com` |
| `sites[].platform` | 平台类型，见上文 |
| `sites[].credential_type` | `access_token`、`session_cookie` 或 `username_password` |
| `sites[].access_token` | Authorization token，仅用于 `access_token` |
| `sites[].session_cookie` | 完整 Cookie 值，仅用于 `session_cookie` |
| `sites[].username` / `password` | 账密登录 |
| `sites[].user_id` | **多数 NewAPI 站点必填**，用户 ID（对应请求头 `New-Api-User`） |
| `sites[].additional_verification` | 附加验证方式：普通站点 `none`、图片验证码 `CAPTCHA`、Cloudflare 人机验证 `Turnstile`；默认 `none` |
| `sites[].headers` | 可选自定义请求头 |

### 如何获取 `user_id`

很多 NewAPI 站点（包括 `newapi.zmoon.top`）会校验：

```http
Authorization: <access_token>
New-Api-User: <你的用户ID>
```

仅填写 token、不填 `user_id` 时会返回：

- `New-Api-User header not provided`
- 或 `New-Api-User does not match logged in user`

获取步骤：

1. 浏览器登录目标站点
2. 按 `F12` 打开开发者工具 → `Network`
3. 刷新页面，点开任意 `/api/user/self` 或 `/api/user/*` 请求
4. 在 Request Headers 中找到 `New-Api-User`，把这个数字填到 `config.yaml` 的 `user_id`

## 实现说明

签到核心逻辑参考 Octopus 的 `internal/sitesync`，并兼容验证码站：

1. 若为账密，先 `POST /api/user/login` 获取明确类型的登录凭证
2. 按 `credential_type` 生成 Authorization 或 Cookie 请求头
3. 必要时请求 `GET /api/user/self` 探测用户 ID
4. `GET /api/user/checkin` 查状态（已签到 / 是否要验证码）；**不把「查询成功」当签到成功**
5. 按 `additional_verification` 选择普通、图片验证码或 Turnstile 流程；不再根据全局状态字段自动切换验证方式
6. 签到后请求 `GET /api/user/self` 获取当前总余额
7. 成功或“已签到”都记为成功

## 退出码

- `0`：全部成功
- `1`：配置/参数、日志或 Telegram 通知错误
- `2`：存在签到失败站点
