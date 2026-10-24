# DB Compare

跨平台 MySQL 表结构对比与同步工具（桌面客户端，架构上支持 Web/server 形态）。

- **引擎**: [`ariga.io/atlas`](https://github.com/ariga/atlas)（Apache-2.0）内省 / Diff / DDL 生成
- **外壳**: [Wails v3](https://wails.io)（桌面）+ `-tags server`（HTTP 服务）
- **前端**: Vue 3 + Naive UI
- **重点**: MySQL 5.7 ↔ 8.0 跨版本归一化（消除假差异）与能力降级矩阵

## 功能（第一期）

- 连接管理（密码存 OS Keychain，失败时回退内存）
- 双库结构对比，按 **新增 / 修改 / 删除** 三标签展示并可勾选
- SQL 预览、预检（保留字、FK 名、索引前缀、8.0 独有特性降级等）
- 执行同步 + **同步后自动复查**
- 同步方案（Profile）保存 / 导出（默认不含密码）

**明确不在第一期**：分区 `PARTITION BY` 完整同步、视图/触发器/存储过程、Web 多用户鉴权。

## 连接与方案迁移

在“连接与方案”中导出全部配置，或单独导出连接/方案；方案会携带关联连接。导入 JSON 时先预览，再确认新增，同名自动另存，不覆盖已有配置。默认不导出密码；每次导出可选择包含已保存的密码（明文），目标机导入后写入系统凭据存储。自定义或本机专用连接参数会提示并省略。详见 [配置迁移](docs/settings-transfer.md)。

## 同步工作流

已支持按表选择、默认开启的索引对比开关及 SQL 差异关联、同步前结构快照、逐条执行报告与持久化历史、失败后重新读取结构规划剩余项，以及可逆注释乱码检测和修复预览。0.2.6 新增对比时自动检测并纳入注释修复计划，源库、目标库分组预览并确认执行。可导出 SQL、结构快照和报告。

使用方法、恢复边界及测试方式见 [同步工作流](docs/sync-workflow.md)。结构快照不含业务数据，不能代替数据备份。

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

## macOS 安装包

```bash
# 在 macOS 上构建 Apple Silicon + Intel 通用 DMG
./scripts/package-macos.sh
```

产物位于 `bin/releases/<构建时间>/`，包含 DMG、`SHA256SUMS.txt` 和安装说明。
要求目标 Mac 使用 macOS 13 或更高版本；打开 DMG 后将应用拖到“应用程序”。
打包使用本地临时签名，不包含 Developer ID 公证，首次打开可能受到系统拦截。
安装包不包含本机数据库连接、密码或历史，另一台 Mac 需重新配置连接。

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

## 结构快照（0.2.0）

支持多连接多数据库手动/定时保存结构版本、macOS 后台采集、保留策略与重要版本、历史快照及当前库对比、快照包/SQL 导出和失败记录。使用方式与范围见 [结构快照说明](docs/structure-snapshots.md)。
