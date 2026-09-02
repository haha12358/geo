# AGENTS.md

本文件为 ZCode agent 提供本仓库的工作指引。

## 项目概述

`github.com/haha12358/geo` 是 MetaCubeX 的命令行工具，用于查询（look/look 源站）、转换（convert）、校验（check）和解包（unpack）各类 GeoIP / GeoSite 数据库格式。支持 MaxMind MMDB、V2Ray dat、sing-geoip/geosite、Meta-geoip 等格式。Go 1.20+，无第三方 CLI 之外的框架。

## 目录结构

- `cmd/geo/` — cobra CLI 入口。子命令文件为 `cmd_look.go`、`cmd_convert.go`、`cmd_check.go`、`cmd_unpack.go`；各命令的具体实现位于 `cmd/geo/internal/{convert,unpack}/`。`-D` 全局参数设置工作目录（默认 `~/.geo`）。
- `geoip/` — GeoIP 数据库抽象层。`database.go` 的 `FromBytes`/`FromFile` 负责格式嗅探：先尝试 MMDB（maxminddb-golang），失败后尝试 V2Ray dat。
- `geosite/` — GeoSite 数据库抽象层。嗅探顺序相反：先 sing-geosite，再 V2Ray dat。
- `convert/` — 格式间转换器（`maxmind.go`、`meta.go`、`sing.go`、`v2ray_ip.go`、`v2ray_site.go`），由 `cmd/geo/internal/convert` 调用。
- `encoding/` — 底层编解码：`v2raygeo/`（protobuf，`config.pb.go` 为生成文件）、`singgeo/`（sing-geosite 格式读写）、`clashrule/`。
- `constant.go` — 版本号常量。

## 构建与验证

```shell
go build ./...          # 编译全部
go build ./cmd/geo      # 构建 CLI（CI 以此产物发布）
go vet ./...            # 静态检查（仓库无 lint 配置、无测试文件）
```

CI（`.github/workflows/build.yml`）交叉编译 7 个平台（linux/darwin/windows × amd64/arm64 及 linux armv7），改动需保持跨平台可编译，不要引入平台特定代码。`cmd/geo/dns.go` 中 Windows 专属逻辑（nslookup 调用）已用 `runtime.GOOS` 守卫，其余 DNS 逻辑跨平台通用。

## 架构与分层规则

- 依赖方向：`cmd/geo` → `convert` → `geoip`/`geosite`/`encoding`。`encoding` 是最底层，不得反向引用上层包。
- 新增数据库格式时：在 `geoip` 或 `geosite` 包中扩展 `Type` 常量与 `Database` 实现（`LookupCode` / geosite 对应方法），并在 `database.go` 的嗅探逻辑中注册，再在 `convert/` 添加转换器。
- `geoip.Database` 通过 `SourceType`（输入格式）与 `MemoryType`（查询时行为）区分格式；MMDB 的 `DatabaseType` metadata 字符串即类型标识（如 `"sing-geoip"`、`"Meta-geoip0"`），改字符串会影响格式嗅探，需谨慎。
- `encoding/v2raygeo/config.pb.go` 是 protoc 生成文件，手改会被覆盖；改协议需更新 proto 定义后重新生成。

## 已知限制与陷阱

- **转换矩阵不对称**（见 README 表格）：转出 MaxMind 因法律原因不可用；GeoSite 仅支持 v2ray → sing；MaxMind/sing-geoip → Meta-geoip 有意不支持（单结果库无此需求）。新增转换前先确认此设计意图。
- Meta-geoip 是唯一支持一个 IP 对应多个国家码（IPList/IPSet）的格式，`LookupCode` 返回 `[]string` 即为此设计。
- V2Ray dat 无魔数可嗅探，解析失败时静默 fallthrough，`FromBytes` 返回 `ErrInvalidDatabase` 前会依次尝试所有格式——调试解析问题时应按该顺序排查。
- `geoip.Database` 内嵌的 `*maxminddb.Reader` 持有资源，关闭语义见 `Close()`；不要在复制 `Database` 值后多处并发关闭。
- **TUN fake-ip 环境**：`geo look` 解析域名时若系统解析命中 fake-ip 段（198.18.0.0/15、28.0.0.0/8、fc00::/18，见 `cmd/geo/dns.go`），自动绑定物理网卡 IPv4 源地址向公共 DNS（223.5.5.5/119.29.29.29）直发 UDP 查询以绕过 TUN 的 53 端口劫持；`--dns` 参数则走系统 `nslookup domain [server]`。注意 nslookup 参数顺序必须是 domain 在前、server 在后；输出解析需兼容 GBK 代码页（中文标签会乱码，只匹配 ASCII 的 "Address" 关键字并跳过服务器信息段）。

## 参考文档

- `README.md` — 支持的格式、命令用法、转换矩阵及 FAQ（含格式设计差异的原因）。
