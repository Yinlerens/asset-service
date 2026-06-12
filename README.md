# 资产服务

资产服务是一个 Go HTTP 服务，负责用户账户余额和只追加的账务流水。

前端不能直接调用这个服务。请求必须先经过网关：网关完成用户登录态校验，提取用户 UUID，然后只把可信的内部请求转发给资产服务。

## 身份边界

资产服务不关心前端使用哪种身份系统，也不处理前端登录态。它只信任两个由网关注入的请求头：

- `X-Internal-Token`：网关和资产服务之间共享的内部调用密钥
- `X-User-Id`：网关校验用户后提取出的用户 UUID

资产服务会把 `X-User-Id` 当作账户拥有者 ID 使用。

## 配置

必填环境变量：

```text
DATABASE_URL=postgres://...
INTERNAL_TOKEN=...
```

可选环境变量：

```text
PORT=8080
MAX_LEDGER_LIMIT=100
```

## 数据库迁移

迁移文件放在 `migrations/` 目录下，是普通 SQL 文件。

使用 `golang-migrate` 执行迁移：

```bash
migrate -path migrations -database "$DATABASE_URL" up
```

## API

健康检查：

```http
GET /health
GET /ready
```

查询或懒创建当前用户账户：

```http
GET /v1/me/account
X-Internal-Token: <网关内部调用密钥>
X-User-Id: <网关校验后的用户 UUID>
```

如果数据库里还没有这个用户，服务会自动创建账户，初始余额为 `0`。

创建账务流水：

```http
POST /v1/me/entries
X-Internal-Token: <网关内部调用密钥>
X-User-Id: <网关校验后的用户 UUID>
Idempotency-Key: <本次操作的唯一幂等键>
Content-Type: application/json

{
  "delta_minor": 1000,
  "reason": "initial_credit",
  "metadata": {
    "source": "gateway"
  }
}
```

查询账务流水：

```http
GET /v1/me/ledger?limit=50&cursor=<next_cursor>
X-Internal-Token: <网关内部调用密钥>
X-User-Id: <网关校验后的用户 UUID>
```

## 本地开发

安装 Go 1.22 或更新版本，然后运行：

```bash
go test ./...
go run ./cmd/asset-service
```

如果你想一键启动本地环境，直接用 Docker Compose：

```bash
docker compose up --build
```

这会启动：

- `db`：本地 Postgres
- `migrate`：执行 `migrations/` 里的建表脚本
- `asset-service`：资产服务本体，监听 `127.0.0.1:8081`

`GET /v1/me/account` 的懒创建逻辑：

1. 如果 `asset.accounts` 中不存在该 `user_id`，插入一行余额为 `0` 的账户。
2. 返回账户信息。

余额写入会在事务中使用 `for update` 锁住账户行，因此同一个用户的并发请求不会把余额扣成负数。
