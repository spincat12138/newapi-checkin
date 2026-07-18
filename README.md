# NewAPI Checkin

[![Build Docker Image](https://github.com/spincat12138/newapi-checkin/actions/workflows/docker.yml/badge.svg)](https://github.com/spincat12138/newapi-checkin/actions/workflows/docker.yml)

从 [Hureru/octopus](https://github.com/Hureru/octopus) 中提取的 **站点签到** 独立工具。

仅保留 NewAPI / AnyRouter 等管理平台站点的签到能力，去掉 Octopus 里的同步、代理池、调度、数据库等无关模块。

**文档**

| 文档 | 用途 |
|------|------|
| [agent.md](agent.md) | Agent / 开发入口：背景、架构、测试方式 |
| [doc/](doc/README.md) | 实现细节（配置、导入、签到流程等） |

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

## 快速开始

### 1. 准备配置

**方式 A：从 Octopus / AionUi accounts 备份 JSON 导入（推荐）**

导出的备份一般为 `accounts-backup-*.json`，结构含 `accounts.accounts[]`：

```bash
go run ./cmd/checkin import -from accounts-backup.json -out config.yaml
```

常用选项：

```bash
# 同时导入已禁用账号
go run ./cmd/checkin import -from accounts-backup.json -out config.yaml -include-disabled

# 仅导入开启了自动签到的账号
go run ./cmd/checkin import -from accounts-backup.json -out config.yaml -require-auto-checkin
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
sites:
  - name: "我的站点"
    base_url: "https://example.com"
    platform: "new-api"
    credential_type: "access_token"
    access_token: "xxx"
    user_id: 1
```

或使用账密：

```yaml
  - name: "站点2"
    base_url: "https://example2.com"
    platform: "any-router"
    credential_type: "username_password"
    username: "user"
    password: "pass"
```

使用 Session Cookie：

```yaml
  - name: "站点3"
    base_url: "https://example3.com"
    platform: "any-router"
    credential_type: "session_cookie"
    session_cookie: "session=xxx"
    user_id: 2
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

### 3. 编译

```bash
go build -o newapi-checkin.exe ./cmd/checkin
./newapi-checkin.exe import -from accounts-backup.json -out config.yaml
./newapi-checkin.exe -config config.yaml
```

## Docker

本地构建并运行：

```powershell
docker compose build
docker compose run --rm checkin
```

容器以一次性任务运行，配置以只读方式挂载，签到日志写入宿主机的 `logs/checkin.log`。

使用 GHCR 镜像时设置镜像地址后运行：

```powershell
$env:NEWAPI_CHECKIN_IMAGE = "ghcr.io/spincat12138/newapi-checkin:latest"
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
| `sites[].name` | 站点名称（展示用） |
| `sites[].base_url` | 站点根地址，如 `https://xxx.com` |
| `sites[].platform` | 平台类型，见上文 |
| `sites[].credential_type` | `access_token`、`session_cookie` 或 `username_password` |
| `sites[].access_token` | Authorization token，仅用于 `access_token` |
| `sites[].session_cookie` | 完整 Cookie 值，仅用于 `session_cookie` |
| `sites[].username` / `password` | 账密登录 |
| `sites[].user_id` | **多数 NewAPI 站点必填**，用户 ID（对应请求头 `New-Api-User`） |
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

签到核心逻辑参考 Octopus 的 `internal/sitesync`：

1. 若为账密，先 `POST /api/user/login` 获取明确类型的登录凭证
2. 按 `credential_type` 生成 Authorization 或 Cookie 请求头
3. 必要时请求 `GET /api/user/self` 探测用户 ID
4. 调用 `POST/GET /api/user/checkin`
5. 签到后请求 `GET /api/user/self` 获取当前总余额
6. 成功或“已签到”都记为成功

## 退出码

- `0`：全部成功
- `1`：配置/参数错误
- `2`：存在签到失败站点
