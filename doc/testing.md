# 测试说明

## 1. 自动化测试

### 运行

```powershell
cd C:\Personal\Code\newapi-checkin
go test ./...
go test ./internal/config/ -count=1 -v
```

### 当前覆盖

| 包 | 文件 | 覆盖点 |
|----|------|--------|
| `internal/config` | `config_test.go` / `import_octopus_test.go` | 凭证互斥校验、导入过滤、显式 Cookie 映射、Save/Load 往返 |
| `internal/checkin` | `checkin_test.go` / `http_test.go` / `twocaptcha_test.go` | 鉴权、奖励、余额、状态判定、验证码提交、Turnstile、2Captcha 创建任务/轮询/错误 |
| `cmd/checkin` | `main_test.go` | 成功/失败日志格式、未知金额展示、文件追加写入 |
| `cmd/import-config` | `main_test.go` | 必填输入校验、独立入口生成可加载的 YAML |

签到 HTTP 测试使用本地 `httptest` 服务，不依赖真实站点或密钥。

### 测试数据

`import_octopus_test.go` 内嵌 `sampleBackup` JSON，覆盖：

- 启用 new-api + token + 字符串 user id
- disabled 账号
- anyrouter + cookie
- sub2api（应跳过）
- unknown → new-api-like
- 无凭证
- autoCheckIn 开关

## 2. 手工集成测试

### 导入

```powershell
go run ./cmd/import-config -from <accounts-backup.json> -out config.yaml
# 检查：导入数量、skipped 原因、YAML 可被 Load
```

可选回归：

```powershell
go run ./cmd/import-config -from <file> -out t.yaml -include-disabled
go run ./cmd/import-config -from <file> -out t.yaml -require-auto-checkin
# 缺 -from 应 exit 1
go run ./cmd/import-config
```

### 签到

```powershell
# 配置校验失败
go run ./cmd/checkin -config not-exist.yaml

# 无匹配站点 exit 1
go run ./cmd/checkin -config config.yaml -only "__no_such_site__"

# 单站冒烟
$env:TWOCAPTCHA_API_KEY = "你的 2Captcha API Key"
go run ./cmd/checkin -config config.yaml -only "已知站名关键字" -timeout 60

# 全量（耗时、依赖外网）
go run ./cmd/checkin -config config.yaml -timeout 60
```

关注：

- 已签到是否显示 OK
- 缺 `user_id` 时错误信息是否可操作
- token 失效时 FAIL 信息是否清晰
- 日志是否包含站点、签到时间、成功状态、本次奖励与总余额
- 连续运行两次后日志是否保留前一次内容并继续追加

## 3. 变更回归清单

| 改动 | 建议 |
|------|------|
| 导入映射/过滤 | `go test ./internal/config/` + 真实 backup 抽测 |
| Save YAML 形状 | Save 后 `Load` 成功；diff 无多余空字段 |
| 鉴权/签到判定 | 至少 1 个 token 站 + 1 个“已签到”场景 |
| CLI 入口 | `cmd/checkin` 帮助/签到参数，`cmd/import-config` 导入参数 |
| 文档 | `agent.md` 摘要与 `doc/*` 细节一致 |

## 4. 构建

```powershell
go build -o newapi-checkin.exe ./cmd/checkin
go build -o newapi-import-config.exe ./cmd/import-config
```

`*.exe` 已在 `.gitignore`。

## 5. 安全测试注意

- 勿将含真实 token 的 `config.yaml` 或备份 JSON 提交到 git
- CI 中只跑无密钥单测；集成测试仅在本地
