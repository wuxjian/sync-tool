# 文件同步工具

> Linux 服务端 → Windows 客户端的增量文件同步工具，使用 Go 开发，自带 HTML 控制台。

## ✨ 特性

- **跨平台同步**：Linux 作为数据源（服务端），Windows 作为接收端（客户端）
- **可视化选择**：客户端内置 HTML 页面，浏览目录树、勾选要同步的文件/目录
- **增量比对**：本地保存元信息（size / mtime / SHA256），未变化的文件**不会重复下载**
- **断点续传**：大文件下载中断后可恢复
- **gzip 压缩**：传输过程自动压缩，节省带宽
- **Token 认证**：服务端可选启用 Bearer Token
- **过滤规则**：可按后缀 / 正则 / 大小排除文件
- **零依赖**：使用 Go 标准库，仅 `gopkg.in/yaml.v3` 一个第三方包
- **单文件部署**：HTML 页面通过 `embed` 嵌入到二进制，客户端只需要一个 exe

## 📁 目录结构

```
sync-tool/
├── server/             # Linux 服务端
│   ├── main.go         # 入口
│   ├── config.go       # 配置加载与过滤器
│   ├── scanner.go      # 目录扫描、安全路径
│   ├── handler.go      # HTTP 处理器
│   ├── util.go
│   └── go.mod
├── client/             # Windows 客户端
│   ├── main.go
│   ├── config.go
│   ├── client.go       # 远程 API 客户端
│   ├── sync.go         # 同步核心逻辑
│   ├── meta.go         # 本地元信息
│   ├── server.go       # 本地 HTTP 服务（含 embed 的 HTML）
│   ├── gzip.go
│   ├── web/index.html  # 嵌入的 HTML 页面
│   └── go.mod
├── config/
│   ├── server.yaml.example
│   └── client.yaml.example
└── README.md
```

## 🚀 快速开始

### 1. 编译

```bash
# 服务端（在 Linux 上编译）
cd server
go build -o synctool-server .

# 客户端（从 Linux 交叉编译到 Windows）
cd ../client
GOOS=windows GOARCH=amd64 go build -o synctool-client.exe .

# 在 Windows 上直接编译
cd client
go build -o synctool-client.exe .
cd server
go build -o synctool-server.exe .
```

Windows 下也可直接运行 `build.bat` 一键编译两者。

### 2. 在 Linux 上启动服务端

```bash
cp ../config/server.yaml.example config.yaml
# 修改 config.yaml：设置 root 为要同步的目录
./synctool-server -config=config.yaml
```

输出类似：
```
文件同步服务端启动
  监听地址: :8080
  根目录  : /data/files
  Token   : yo****re
```

> 默认前台运行，终端退出进程即停。后台运行方式：`nohup ./synctool-server -config=config.yaml &` 或配置为 systemd 服务。

### 3. 在 Windows 上启动客户端

```bat
:: 把 synctool-client.exe 复制到 Windows
:: 把 client.yaml.example 改名为 client.yaml，修改 remote.url 指向 Linux 服务端
synctool-client.exe -config=config.yaml
```

输出类似：
```
文件同步客户端启动
  监听地址 : :9090
  远程地址 : http://192.168.1.10:8080
  本地目录 : D:\synced
打开浏览器访问: http://localhost:9090
```

### 4. 打开浏览器使用

浏览器访问 `http://localhost:9090`：

- 自动加载远程目录树
- 勾选要同步的文件/目录
- 点击"开始同步"
- 实时查看进度、事件日志

## 🔧 配置详解

### 服务端 `server.yaml`

| 字段 | 说明 |
|------|------|
| `listen` | HTTP 监听地址，默认 `:8080` |
| `token` | Bearer Token，留空不鉴权 |
| `root` | 同步根目录，**必须绝对路径** |
| `filter.exclude_ext` | 排除后缀列表（如 `.tmp`、`.log`） |
| `filter.exclude_pattern` | 排除正则（针对相对路径） |
| `filter.max_size` | 单文件大小上限，0 不限制 |

### 客户端 `client.yaml`

| 字段 | 说明 |
|------|------|
| `listen` | 本地 HTTP 监听，默认 `:9090` |
| `remote.url` | Linux 服务端完整 URL |
| `remote.token` | 与服务端一致 |
| `local.root` | 同步到 Windows 的目录 |
| `local.meta_file` | 元信息文件路径 |

## 📡 服务端 API

所有接口（除 `/health`）需 `Authorization: Bearer <token>` 或 `?token=<token>`。

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/health` | 健康检查 |
| GET | `/api/list?path=xxx` | 列目录 |
| GET | `/api/info?path=xxx` | 单个文件信息 |
| GET | `/api/hash?path=xxx` | 计算 SHA256 |
| GET | `/api/tree?path=xxx&depth=N` | 递归目录树（`depth=0` 表示无限深度） |
| GET | `/api/download?path=xxx` | 下载文件，支持 `Range` 和 `Accept-Encoding: gzip` |

## 🧠 同步逻辑

1. **用户在前端勾选**要同步的文件或目录
2. 客户端把勾选路径**展开为完整文件列表**
3. 对每个文件，读取本地元信息（`meta.json`）和远端信息：
   - **size + mtime 一致** → 跳过（`skipped`）
   - **本地不存在或不一致** → 下载
4. 下载支持：
   - **断点续传**：若本地已存在部分文件，发送 `Range` 头
   - **gzip 压缩**：客户端发送 `Accept-Encoding: gzip`
5. 下载完成后：
   - 修正本地文件的 mtime 为远端 mtime
   - 同步写入 `size+mtime` 到 meta（保证下次能 skip）
   - 异步计算 SHA256 补充写入 meta
6. 同步结束保存 `meta.json`

> 第一遍同步所有文件会被下载。第二遍起，**未变化的文件不再传输**。

## 🔐 安全建议

- 服务端必须设置 `token`
- 生产环境建议加上 TLS（在前面套 nginx / caddy 反代即可）
- Linux 防火墙只放通服务端到客户端的网络

## 🐛 故障排查

**浏览器打开页面空白**
- 确认 `client.exe` 启动时未报错
- 确认 `local.root` 目录可写
- 检查 `meta.json` 是否被损坏（删除后会自动重建）

**下载失败 / 跳过判定异常**
- 查看 `meta.json`，删除某个文件的条目可强制下次重下
- 如果文件被外部修改过，mtime 会变，会被识别为"需重下"

**中文路径乱码**
- 终端使用 `chcp 65001`（UTF-8）后启动二进制

## 📜 License

MIT
