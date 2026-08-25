# DB Compare

跨平台 MySQL 表结构对比与同步工具（桌面客户端，架构上支持 Web/server 形态）。

- **引擎**: [`ariga.io/atlas`](https://github.com/ariga/atlas)（Apache-2.0）内省 / Diff / DDL 生成
- **外壳**: [Wails v3](https://wails.io)（桌面）+ `-tags server`（HTTP 服务）
- **前端**: Vue 3 + Element Plus
- **重点**: MySQL 5.7 ↔ 8.0 跨版本归一化（消除假差异）与能力降级矩阵

## 功能（第一期）

- 连接管理（密码存 OS Keychain，失败时回退内存）
- 双库结构对比，按 **新增 / 修改 / 删除** 三标签展示并可勾选
- SQL 预览、预检（保留字、FK 名、索引前缀、8.0 独有特性降级等）
- 执行同步 + **同步后自动复查**
- 同步方案（Profile）保存 / 导出（默认不含密码）

**明确不在第一期**：分区 `PARTITION BY` 完整同步、视图/触发器/存储过程、Web 多用户鉴权。

## 开发环境

```bash
# Go 1.22+、Node 20+、Docker
export PATH="$(brew --prefix go)/bin:$HOME/go/bin:$PATH"
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.12

# 测试数据库（8.0 + MariaDB 10.5 作 5.7 兼容端；官方 mysql:5.7 在 Apple Silicon 上常崩溃）
cd docker && docker-compose up -d
```

| 角色 | 地址 | 库 |
|------|------|----|
| 5.7 兼容（MariaDB 10.5） | `127.0.0.1:33057` | `src_db` / root / root |
| MySQL 8.0 | `127.0.0.1:33080` | `dst_db` / `sync_target` / root / root |

## 命令

```bash
# 前端
cd frontend && npm install && npm run build

# 生成 Wails bindings
wails3 generate bindings -ts -names -i ./...

# 单元测试
go test ./internal/...

# 集成测试（需 docker-compose）
go test -tags integration ./test/integration/ -v

# 桌面（需要 CGO）
CGO_ENABLED=1 go build -o bin/dbcompare .
# 或: wails3 task build / wails3 task run

# Server 形态（无 GUI，CGO 可关）
CGO_ENABLED=0 go build -tags server -o bin/dbcompare-server .
./bin/dbcompare-server   # 默认 HTTP，见 Wails server 文档
```

## 架构

```
main.go                 Wails 入口（唯一依赖 Wails）
internal/engine/        纯 Go：inspect → normalize → diff → plan → apply → verify
internal/store/         SQLite + keyring
internal/service/       业务服务（可脱 GUI 单测）
frontend/               Vue UI
```

归一化作用在 Atlas 的 `schema.Schema` 上（不另建平行模型）；两侧各自 inspect，用**目标端** driver 做 Diff/Plan。

## 许可证

本项目代码见仓库 LICENSE（若未添加则默认保留版权）。第三方组件见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
