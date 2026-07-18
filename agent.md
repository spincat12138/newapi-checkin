# Agent 指南：newapi-checkin

面向 AI Agent / 后续开发者的项目入口文档。实现细节见 [`doc/`](doc/)。

---

## 1. 项目背景

| 项 | 说明 |
|----|------|
| 路径 | `C:\Personal\Code\newapi-checkin` |
| 模块名 | `newapi-checkin`（Go 1.22+） |
| 来源 | 从 [Hureru/octopus](https://github.com/Hureru/octopus) 的站点同步/签到能力中**抽出**的独立工具 |
| 目标 | **只做** NewAPI 系管理站的批量签到与签到后余额日志；不做余额持久化同步、代理池、调度、数据库 |
| 上游参考 | Octopus `internal/sitesync` 中与 checkin 相关的请求约定 |

典型数据流：

```text
Octopus/AionUi accounts-backup JSON
        │  import 子命令
        ▼
   config.yaml  ──►  checkin 主流程  ──►  各站点 /api/user/checkin
```

---

## 2. 架构一览

```text
cmd/checkin/main.go
    ├── 默认：runCheckin  → config.Load → checkin.Run(per site)
    └── import：runImport → config.ImportOctopusFile → config.Save

internal/config/          # 配置与导入（无网络）
    ├── Config / Site
    ├── Load / Save / normalize
    └── ImportOctopus*    # accounts-backup JSON → Config

internal/checkin/         # 签到（有网络）
    ├── Run               # 单站入口
    ├── login / checkinSite / discoverUserID / fetchAccountBalance
    └── http helpers      # 显式鉴权类型、New-Api-User、JSON 解析
```

| 包 | 职责 | 依赖 |
|----|------|------|
| `cmd/checkin` | CLI 参数、批处理循环、日志追加写入、退出码 | config, checkin |
| `internal/config` | YAML 配置、Octopus JSON 导入 | `gopkg.in/yaml.v3` |
| `internal/checkin` | HTTP 登录/签到/兼容逻辑 | config |

**设计原则**

- 配置与网络逻辑分离：导入可单测、无需外网。
- 签到按显式凭证类型选择 Authorization 或 Cookie，并兼容已签到中英文文案。
- 不暴力枚举 `user_id`；配置优先，其次 `/api/user/self` 探测。
- 密钥仅落在 `config.yaml`（已 gitignore）。

详细模块说明与请求约定见：

- [doc/architecture.md](doc/architecture.md)
- [doc/checkin.md](doc/checkin.md)
- [doc/import.md](doc/import.md)
- [doc/configuration.md](doc/configuration.md)

---

## 3. 支持范围

**平台**（`sites[].platform`）：

- `new-api` / `any-router` / `one-api` / `veloera` / `done-hub` / `new-api-like`

**凭证**：

- `access_token`（Authorization）
- `session_cookie`（Cookie header value）
- `username_password`（先 `POST /api/user/login`）

**明确不做 / 跳过**：

- `sub2api` 等非兼容 `site_type`（导入时跳过）
- Octopus 的同步、标签、健康检查 UI 状态等

---

## 4. 常用命令

```powershell
cd C:\Personal\Code\newapi-checkin

# 编译
go build -o newapi-checkin.exe ./cmd/checkin

# 从 Octopus/AionUi 备份导入配置
.\newapi-checkin.exe import -from accounts-backup.json -out config.yaml
.\newapi-checkin.exe import -from accounts-backup.json -out config.yaml -include-disabled
.\newapi-checkin.exe import -from accounts-backup.json -out config.yaml -require-auto-checkin

# 签到
.\newapi-checkin.exe -config config.yaml
.\newapi-checkin.exe -config config.yaml -only "关键字1,关键字2"
.\newapi-checkin.exe -config config.yaml -timeout 60
.\newapi-checkin.exe -config config.yaml -log logs\checkin.log

# 帮助
.\newapi-checkin.exe help
```

开发态可用 `go run ./cmd/checkin ...`。

**退出码**

| 码 | 含义 |
|----|------|
| 0 | 全部成功（含“今日已签到”） |
| 1 | 配置/参数/导入错误 |
| 2 | 至少一个站点签到失败 |

---

## 5. 测试方式

### 5.1 单元测试（默认、CI 友好）

当前覆盖包括导入/配置读写，以及使用 `httptest` 的签到与余额日志（无外网）：

```powershell
go test ./...
go test ./internal/config/ -count=1 -v
```

关键用例：`internal/config/import_octopus_test.go`、`internal/checkin/checkin_test.go`、`cmd/checkin/main_test.go`

- 默认过滤 disabled / 无凭证 / 不支持平台
- cookie 鉴权、`unknown` → `new-api-like`
- `-include-disabled` / `-require-auto-checkin`
- `Save` + `Load` 往返
- `quota_awarded` 奖励解析、签到后总余额、OneAPI 剩余额度
- 已签到奖励为 0、余额未知不伪装成 0、CLI 日志格式与文件追加
- `access_token` / `session_cookie` 请求头隔离、Cookie 导入映射和混填校验

### 5.2 手工集成（需真实站点与 token）

```powershell
# 导入后抽查配置
go run ./cmd/checkin import -from <backup.json> -out config.yaml

# 单站冒烟
go run ./cmd/checkin -config config.yaml -only "某站名关键字"

# 全量（注意超时与站点可用性）
go run ./cmd/checkin -config config.yaml -timeout 60
```

### 5.3 变更时建议自测清单

| 改动类型 | 至少执行 |
|----------|----------|
| 导入/映射/Save | `go test ./internal/config/` |
| 签到 HTTP/成功判定 | `go test ./internal/checkin/` + 单站 `-only` 冒烟 |
| CLI 参数/退出码 | `import` 缺 `-from`、空配置、`-only` 无匹配、日志文件不可写 |
| 文档 | 与 `README.md` / `doc/*` 保持一致 |

更细的测试说明见 [doc/testing.md](doc/testing.md)。

---

## 6. Agent 工作约定

1. **先读本文 + `doc/` 索引**，再改代码；用户级用法以 `README.md` 为准。
2. **不要提交** `config.yaml`、真实 token、完整 accounts 备份中的密钥。
3. 改签到逻辑时优先兼容多站差异，避免只适配单一站点。
4. 新增功能优先：纯函数/可单测的 `internal/*`，CLI 只做编排。
5. 文档分层：
   - `agent.md`：背景、架构、测试、Agent 入口
   - `doc/*`：实现细节、字段映射、请求流
   - `README.md`：人类用户快速开始

---

## 7. 已知可选后续

- 导入时与已有 `config.yaml` 按 `base_url` **merge**
- 导入侧 `-only` 名称过滤
- 签到结果落盘日志 / 定时任务对接

---

## 8. 文档索引

| 文档 | 内容 |
|------|------|
| [README.md](README.md) | 用户快速开始 |
| [doc/README.md](doc/README.md) | 项目文档目录 |
| [doc/architecture.md](doc/architecture.md) | 架构与模块边界 |
| [doc/configuration.md](doc/configuration.md) | config.yaml 字段与校验 |
| [doc/import.md](doc/import.md) | Octopus JSON 导入实现 |
| [doc/checkin.md](doc/checkin.md) | 签到流程与 HTTP 约定 |
| [doc/testing.md](doc/testing.md) | 测试策略与清单 |
| [doc/changelog-notes.md](doc/changelog-notes.md) | 演进记录（会话进展摘要） |
