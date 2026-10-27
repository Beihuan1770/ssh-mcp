# ssh-mcp（Go 单文件版）

- 日期：2026-10-27
- 版本：v1.0
- 状态：已构建并实测通过（测试机 `192.168.88.11`，BCLinux）
- 上一版：`90-工具与脚本/01-ssh-mcp`（Python + paramiko，保留作为回退）
- 可执行文件：`ssh-mcp.exe`（约 6.1 MB，Windows amd64）
- 配置文件：`ssh_config.yaml`（本地生成，含密码；已被 `.gitignore` 排除，不入库）

## 一、结论

这一版按「Go 单文件 + 单配置含密码」实现：

- 另一台电脑只需要复制 `ssh-mcp.exe` 和 `ssh_config.yaml`，不需要 Python、PowerShell 7 或特定 OpenSSH 版本
- 一个配置文件同时保存地址、端口、用户、密码、私钥、跳板机、别名、标签、备注
- AI 可以把「连接虚拟机88.11」「在 88.11 上执行 df -h」直接映射到工具调用
- 连接池、SFTP、原子写、备份、sudo、跳板机、多级别名解析都在进程内完成
- Windows 下会自动用 `icacls` 把配置文件限制为当前用户可读写

## 二、为什么不是 PowerShell

PowerShell 调用系统 `ssh/scp` 的实测结论：

| 能力 | 结果 |
|---|---|
| `ssh` 执行命令、退出码 | 可用 |
| 密码非交互认证 | 可用（`SSH_ASKPASS_REQUIRE=force` + 环境变量 askpass） |
| `scp` 文件/目录/中文名 | 可用 |
| `sftp -b` 密码认证 | 不可用（批处理模式禁用密码交互） |
| 连接复用 | Windows OpenSSH 无 ControlMaster，每条命令重新握手 |
| 跳板机不同密码 | `-J` 只有一套 askpass，多密码场景难处理 |
| 运行依赖 | Windows + OpenSSH 8.4+ + pwsh 7 |

结论：PowerShell 能覆盖「执行命令 + 传文件」，但做不到完整的 SFTP 语义、连接复用与多跳不同凭据。Go 版把这些问题一次性解决，并且换机成本最低。

## 三、快速开始

### 3.1 在 Trae 中加载

把下面 JSON 加到 Trae 的 MCP 设置：
这个路径按照实际放置的路径来使用即可！

```json
{
  "mcpServers": {
    "ssh-mcp": {
      "command": "D:\\文档\\AI工作站\\02-ssh-mcp-go\\ssh-mcp.exe",
      "args": []
    }
  }
}
```

加载后先调用 `ssh_hosts` 确认配置，再调用 `ssh_connect` 测试连通。

仓库只包含 `ssh_config.example.yaml`。首次使用先复制一份并填入真实地址与密码：

```powershell
Copy-Item .\ssh_config.example.yaml .\ssh_config.yaml
```

真实的 `ssh_config.yaml` 已被 `.gitignore` 排除，不会提交到 GitHub。

### 3.2 配置文件在哪

默认读取 exe 同目录的 `ssh_config.yaml`。如果只有旧的 `ssh_config.json`，会自动回退读取它。也可以用环境变量指定：

```json
"env": { "SSH_MCP_CONFIG": "D:\\其他路径\\ssh_config.yaml" }
```

配置文件按扩展名解析：`.yaml` / `.yml` 走 YAML，其他扩展名走 JSON。

### 3.3 你第 4 点的完整流程

你说：

> 连接 虚拟机88.11 192.168.88.11 端口22 root/&lt;你的密码&gt;

AI 调用：

```
ssh_add_host(
  id="虚拟机88.11",
  name="虚拟机88.11",
  hostname="192.168.88.11",
  port=22,
  user="root",
  password="你的密码",
  aliases=["88.11","测试机"],
  tags=["vmware","test"],
  verify=true
)
```

工具会：

1. 用这组信息实际连接并执行 `echo ok`
2. 验证通过后写入 `ssh_config.yaml`
3. 如果同 `IP:端口` 已有主机（例如 `node1`），默认把新别名合并到现有主机，不产生重复条目
4. 返回「已保存 / 验证通过 / 合并到 node1」

下次你说「连接虚拟机88.11」或「在 88.11 上执行 hostname」，AI 调用：

```
ssh_connect(host="虚拟机88.11")
ssh_exec(host="88.11", command="hostname")
```

不需要再提供地址和密码。

## 四、配置说明

```yaml
version: 1
default_host: node1

defaults:
  port: 22
  timeout_seconds: 10
  command_timeout_seconds: 30
  encoding: utf-8
  host_key_policy: auto
  known_hosts: ~/.ssh/known_hosts
  keepalive_seconds: 30
  audit: true
  audit_dir: ~/.ssh-mcp/logs

hosts:
  - id: node1
    name: 虚拟机88.11
    aliases:
      - 虚拟机88.11
      - "88.11"
      - 测试机
    hostname: 192.168.88.11
    user: root
    password: "你的密码"
    tags:
      - vmware
      - test
    description: 本地测试虚拟机
```

YAML 里 `88.11` 这种像数字的别名和纯数字密码建议加引号，避免被解析成数字。保存配置时程序会自己处理好。

`port`、`encoding`、`timeout_seconds` 等与 `defaults` 相同的字段，保存时会自动省略，保持文件简洁；只有覆盖默认值时才写进主机条目。

- `id`：主键，工具参数 `host` 最终解析到它
- `name`：显示名称，也参与匹配
- `aliases`：额外别名；`id` 和 `name` 会自动加入
- `jump`：跳板机，可填本配置文件中的 `id`，也可填 `[user@]host[:port]`；不同跳板机密码用 `jump_password`
- `host_key_policy`：`auto` 首次自动接受并写入 known_hosts；`strict` 只接受已知指纹；`off` 全部接受
- `audit`：是否写 `~/.ssh-mcp/logs/audit-YYYY-MM.jsonl`
- `default_host`：不传 `host` 时使用的主机

匹配顺序：`id` 精确 > `name` 精确 > 别名精确 > IP 完整匹配 > IP 尾段（`88.11`）> IP 前缀 > 标签精确 > 名称/别名包含。多个同分候选会返回候选列表，不会瞎猜。

## 五、工具清单（17 个）

| 工具 | 用途 |
|---|---|
| `ssh_hosts` | 列出所有主机、别名与认证方式，不显示密码 |
| `ssh_find_host` | 按别名/名称/IP 尾段/标签模糊查找 |
| `ssh_add_host` | 记住新主机，可验证后保存；同 IP 自动合并别名 |
| `ssh_forget_host` | 删除整台主机；`alias_only=true` 只删别名 |
| `ssh_connect` | 解析别名并实际连接验证 |
| `ssh_exec` | 执行命令，返回 stdout/stderr/退出码，支持 cwd/env/pty |
| `ssh_sudo` | sudo 执行，密码来自配置或参数 |
| `ssh_read` | 读文本，支持行区间与字节上限；二进制转 base64 |
| `ssh_write` | overwrite/append/create，默认备份 + 原子写 |
| `ssh_list` | 列目录，支持递归、隐藏文件、条目上限 |
| `ssh_stat` | 类型、大小、权限、属主、时间、链接目标 |
| `ssh_mkdir` | 创建目录，可递归建父目录 |
| `ssh_remove` | 删除文件/目录，非空目录需 recursive=true |
| `ssh_move` | 移动/重命名 |
| `ssh_upload` | 上传文件或目录 |
| `ssh_download` | 下载文件或目录 |
| `ssh_info` | 系统、内核、CPU、内存、磁盘、IP 概况 |

## 六、两台电脑怎么用

1. 复制 `ssh-mcp.exe` + `ssh_config.yaml` 到另一台电脑
2. 修改 `ssh_config.yaml` 中该网络能访问的主机、密码与别名
3. Trae 的 MCP JSON 里把 `command` 改成新路径
4. 先调 `ssh_connect` 验证

不需要安装 Go。只有在需要重新编译时才需要 Go 工具链。

## 七、实测记录（2026-10-27）

测试机：`192.168.88.11`，BCLinux 21.10，OpenSSH root 登录。

| 项目 | 结果 |
|---|---|
| `ssh_connect 88.11` | 成功，23 ms，解析到 node1 |
| `ssh_exec` | 返回 `node1`、root、BCLinux 内核信息，退出码 0 |
| `ssh_sudo` | `id -u` 返回 0 |
| 文件写入/读取 | 12 字节内容完整保留，行区间读取正确 |
| 原子写/备份 | 覆盖后生成 `.bak-时间戳`，原内容保留 |
| 目录操作 | mkdir/move/remove/recursive 全部通过 |
| 上传/下载 | 单文件、递归目录、中文路径均通过 |
| 二进制读取 | base64 返回正确 |
| 输出截断 | `seq 1 20000` 超过 4KB 正确截断 |
| 命令超时 | `sleep 5` 1 秒超时，`isError=true` |
| 跳板机 | `jump=node1` 再连 `127.0.0.1:22`，执行成功 |
| 别名合并 | 新增别名自动合并到 node1，`alias_only` 可单独删除 |
| 模糊查询 | `88.11` 命中 node1；`虚拟机` 返回多个候选并提示明确指定 |
| 配置持久化 | `ssh_add_host` / `ssh_forget_host` 写入并热加载 |
| YAML 配置 | 加载、保存、增删主机后仍保持 YAML 格式；旧 JSON 配置可回退读取 |

## 八、已知限制

- `ssh_config.yaml` 是明文密码。已用 `icacls` 限制为当前用户，但复制、备份、截图、发给别人仍会泄漏；这是你选择的单文件模式的代价
- 服务端按请求顺序处理；一个长时间命令会阻塞后续工具调用
- 不支持 ssh-agent，只支持密码与私钥；私钥口令用 `key_passphrase`
- 只支持 `jump` 跳板链，不支持 `ProxyCommand`
- 递归传输无进度、无断点续传
- `ssh_info` 依赖 Linux 常用命令（`/etc/os-release`、`free`、`df`、`ip`）
- exe 未签名，首次运行可能触发 Windows SmartScreen 或杀毒软件提示
- 单条命令输出超过 `max_bytes` 会截断；默认 256 KB


## 九、文件清单

| 文件 | 作用 |
|---|---|
| `ssh-mcp.exe` | 最终交付物，MCP stdio 服务 |
| `ssh_config.example.yaml` | 配置模板，复制为 `ssh_config.yaml` 后填写 |
| `ssh_config.yaml` | 本地真实配置（含密码，`.gitignore` 排除，不入库） |
| `main.go` | MCP JSON-RPC 入口 |
| `config.go` | 配置加载、保存、别名解析、增删主机 |
| `ssh.go` | 连接池、跳板机、主机指纹、命令执行 |
| `files.go` | SFTP 读写、目录、上传下载 |
| `tools.go` | 17 个工具的定义与参数校验 |
| `util.go` | 编码、审计日志、shell 引号等 |
| `platform_windows.go` | Windows 原子替换配置、ACL |
| `platform_other.go` | 非 Windows 的等价实现 |
| `go.mod` / `go.sum` | 依赖锁定 |
