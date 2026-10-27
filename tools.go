package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func objectSchema(props map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func intProp(desc string, def int) map[string]any {
	return map[string]any{"type": "integer", "description": desc, "default": def}
}

func numProp(desc string) map[string]any {
	return map[string]any{"type": "number", "description": desc}
}

func boolProp(desc string, def bool) map[string]any {
	return map[string]any{"type": "boolean", "description": desc, "default": def}
}

var hostProp = strProp("主机别名，对应 ssh_config.json 中的 id / name / aliases，例如 88.11、虚拟机88.11、node1")

var Tools = []Tool{
	{
		Name:        "ssh_hosts",
		Description: "列出 ssh_config.json 中所有已记住的主机、别名与连接方式（不显示密码）。换机器或不确定别名时先调用它。",
		InputSchema: objectSchema(map[string]any{}),
	},
	{
		Name:        "ssh_find_host",
		Description: "按别名、名称、IP 尾段、标签模糊查找主机。例如查 88.11 会返回 node1/虚拟机88.11。返回候选列表，供后续 ssh_connect/ssh_exec 使用。",
		InputSchema: objectSchema(map[string]any{
			"query": strProp("查找关键词，例如 88.11、虚拟机、测试机"),
			"limit": intProp("最多返回条数，默认 20", 20),
		}, "query"),
	},
	{
		Name:        "ssh_add_host",
		Description: "记住一台主机：写入 ssh_config.json 并可立即验证连接。密码、别名、标签都保存在同一个配置文件。若同一 IP:端口 已存在，默认把新别名合并到现有主机，避免重复条目。",
		InputSchema: objectSchema(map[string]any{
			"id":              strProp("主机主键，例如 node1；不填则用 alias"),
			"alias":           strProp("主机别名，例如 虚拟机88.11；id 为空时作为主键"),
			"name":            strProp("显示名称，例如 虚拟机88.11"),
			"aliases":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "额外别名，例如 [\"88.11\",\"测试机\"]"},
			"hostname":        strProp("IP 或域名，例如 192.168.88.11"),
			"port":            intProp("SSH 端口，默认 22", 22),
			"user":            strProp("登录用户，默认 root"),
			"password":        strProp("登录密码，写入配置文件"),
			"key_file":        strProp("私钥路径，例如 ~/.ssh/id_rsa"),
			"key_passphrase":  strProp("私钥口令"),
			"sudo_password":   strProp("sudo 密码；不填则用登录密码"),
			"jump":            strProp("跳板机：可以是本配置文件中的主机 id，或 [user@]host[:port]"),
			"jump_password":   strProp("跳板机密码（跳板机不在配置文件中时使用）"),
			"encoding":        strProp("远端输出编码，默认 utf-8，可选 gbk/gb18030"),
			"host_key_policy": strProp("主机指纹策略：auto/strict/off"),
			"tags":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "标签，例如 [\"vmware\",\"test\"]"},
			"description":     strProp("备注"),
			"verify":          boolProp("保存前是否实际连接验证，默认 true", true),
			"force":           boolProp("验证失败时是否仍保存，默认 false", false),
			"merge":           boolProp("同 IP:端口 已存在时是否合并别名，默认 true", true),
		}, "hostname"),
	},
	{
		Name:        "ssh_forget_host",
		Description: "从配置文件中删除主机或别名。默认删除整台主机（含密码）；alias_only=true 时只删除指定别名，主机仍保留。",
		InputSchema: objectSchema(map[string]any{
			"host":       strProp("要删除的主机别名"),
			"alias_only": boolProp("只删除别名，保留主机，默认 false", false),
		}, "host"),
	},
	{
		Name:        "ssh_connect",
		Description: "解析别名并实际建立 SSH 连接，返回使用的地址、用户与耗时。等价于“连接某某机器”。后续命令继续用同一个别名即可。",
		InputSchema: objectSchema(map[string]any{
			"host": hostProp,
		}, "host"),
	},
	{
		Name:        "ssh_exec",
		Description: "在远程主机执行 shell 命令，返回 stdout、stderr 与退出码。输出超过 max_bytes 会截断；长任务建议自己用 nohup 后台化。",
		InputSchema: objectSchema(map[string]any{
			"host":      hostProp,
			"command":   strProp("要执行的 shell 命令"),
			"timeout":   numProp("超时秒数，默认取主机配置的 command_timeout_seconds"),
			"max_bytes": intProp("stdout+stderr 最大字节数，默认 262144", 262144),
			"cwd":       strProp("执行前切换到的远程目录"),
			"env":       map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "执行前导出的环境变量"},
			"pty":       boolProp("是否分配伪终端", false),
		}, "host", "command"),
	},
	{
		Name:        "ssh_sudo",
		Description: "以 sudo 执行命令。密码优先用参数，其次用配置中的 sudo_password，最后用登录密码。",
		InputSchema: objectSchema(map[string]any{
			"host":          hostProp,
			"command":       strProp("要执行的 shell 命令"),
			"timeout":       numProp("超时秒数"),
			"max_bytes":     intProp("输出上限字节数", 262144),
			"cwd":           strProp("执行前切换到的远程目录"),
			"env":           map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
			"sudo_password": strProp("临时覆盖 sudo 密码"),
			"pty":           boolProp("是否分配伪终端", false),
		}, "host", "command"),
	},
	{
		Name:        "ssh_read",
		Description: "读取远程文本文件，支持起始行、最大行数与字节上限；二进制文件以 base64 返回。读大日志时用 start_line/max_lines 控制范围。",
		InputSchema: objectSchema(map[string]any{
			"host":       hostProp,
			"path":       strProp("远程文件绝对路径"),
			"encoding":   strProp("文件编码，默认取主机配置"),
			"start_line": intProp("起始行号，从 1 开始", 1),
			"max_lines":  intProp("最多返回行数，默认 2000", 2000),
			"max_bytes":  intProp("最多返回字节数", 262144),
		}, "host", "path"),
	},
	{
		Name:        "ssh_write",
		Description: "写入远程文件。overwrite 默认先备份再原子替换；append 追加；create 仅新建。自动创建父目录。",
		InputSchema: objectSchema(map[string]any{
			"host":          hostProp,
			"path":          strProp("远程文件绝对路径"),
			"content":       strProp("要写入的文本内容"),
			"mode":          map[string]any{"type": "string", "enum": []string{"overwrite", "append", "create"}, "description": "写入模式，默认 overwrite"},
			"encoding":      strProp("编码，默认取主机配置"),
			"backup":        boolProp("overwrite 前是否备份，默认 true", true),
			"atomic":        boolProp("是否原子写，默认 true", true),
			"mkdir_parents": boolProp("是否自动创建父目录，默认 true", true),
		}, "host", "path", "content"),
	},
	{
		Name:        "ssh_list",
		Description: "列出远程目录，显示权限、大小、修改时间。recursive=true 递归列目录（不跟随符号链接）。",
		InputSchema: objectSchema(map[string]any{
			"host":        hostProp,
			"path":        strProp("远程目录绝对路径"),
			"recursive":   boolProp("是否递归", false),
			"max_entries": intProp("最多返回条目数，默认 500", 500),
			"show_hidden": boolProp("是否显示 . 开头的文件", false),
		}, "host", "path"),
	},
	{
		Name:        "ssh_stat",
		Description: "查看远程文件或目录的类型、大小、权限、属主、修改时间与符号链接目标。",
		InputSchema: objectSchema(map[string]any{
			"host": hostProp,
			"path": strProp("远程路径"),
		}, "host", "path"),
	},
	{
		Name:        "ssh_mkdir",
		Description: "创建远程目录；parents=true 时递归创建父目录。",
		InputSchema: objectSchema(map[string]any{
			"host":    hostProp,
			"path":    strProp("远程目录路径"),
			"parents": boolProp("是否递归创建父目录", true),
		}, "host", "path"),
	},
	{
		Name:        "ssh_remove",
		Description: "删除远程文件或目录。删除非空目录必须 recursive=true；符号链接只删除链接本身。",
		InputSchema: objectSchema(map[string]any{
			"host":      hostProp,
			"path":      strProp("远程路径"),
			"recursive": boolProp("是否递归删除非空目录", false),
		}, "host", "path"),
	},
	{
		Name:        "ssh_move",
		Description: "移动或重命名远程文件/目录；overwrite=false 时目标存在会报错。",
		InputSchema: objectSchema(map[string]any{
			"host":      hostProp,
			"src":       strProp("源路径"),
			"dst":       strProp("目标路径"),
			"overwrite": boolProp("是否覆盖目标", false),
		}, "host", "src", "dst"),
	},
	{
		Name:        "ssh_upload",
		Description: "上传本地文件或目录到远程；recursive=true 时才允许上传目录。",
		InputSchema: objectSchema(map[string]any{
			"host":        hostProp,
			"local_path":  strProp("本地文件或目录路径"),
			"remote_path": strProp("远程目标路径"),
			"recursive":   boolProp("上传目录时必须为 true", false),
			"overwrite":   boolProp("是否覆盖已存在文件", true),
		}, "host", "local_path", "remote_path"),
	},
	{
		Name:        "ssh_download",
		Description: "下载远程文件或目录到本地；recursive=true 时才允许下载目录。",
		InputSchema: objectSchema(map[string]any{
			"host":        hostProp,
			"remote_path": strProp("远程文件或目录路径"),
			"local_path":  strProp("本地目标路径"),
			"recursive":   boolProp("下载目录时必须为 true", false),
			"overwrite":   boolProp("是否覆盖本地文件", true),
		}, "host", "remote_path", "local_path"),
	},
	{
		Name:        "ssh_info",
		Description: "查看远程主机概况：系统版本、内核、CPU 核数、内存、根分区、IP。",
		InputSchema: objectSchema(map[string]any{
			"host": hostProp,
		}, "host"),
	},
}

type toolResult struct {
	Text    string
	IsError bool
}

func okResult(format string, args ...any) toolResult {
	return toolResult{Text: fmt.Sprintf(format, args...)}
}

func errResult(format string, args ...any) toolResult {
	return toolResult{Text: fmt.Sprintf(format, args...), IsError: true}
}

func errFrom(err error) toolResult {
	return toolResult{Text: "错误: " + err.Error(), IsError: true}
}

type Server struct {
	mgr      *ConfigManager
	pool     *Pool
	audit    *AuditLogger
	handlers map[string]func(map[string]any) toolResult
}

func NewServer(mgr *ConfigManager) *Server {
	s := &Server{mgr: mgr, pool: NewPool(mgr), audit: &AuditLogger{}}
	s.handlers = map[string]func(map[string]any) toolResult{
		"ssh_hosts":       s.toolHosts,
		"ssh_find_host":   s.toolFindHost,
		"ssh_add_host":    s.toolAddHost,
		"ssh_forget_host": s.toolForgetHost,
		"ssh_connect":     s.toolConnect,
		"ssh_exec":        s.toolExec,
		"ssh_sudo":        s.toolSudo,
		"ssh_read":        s.toolRead,
		"ssh_write":       s.toolWrite,
		"ssh_list":        s.toolList,
		"ssh_stat":        s.toolStat,
		"ssh_mkdir":       s.toolMkdir,
		"ssh_remove":      s.toolRemove,
		"ssh_move":        s.toolMove,
		"ssh_upload":      s.toolUpload,
		"ssh_download":    s.toolDownload,
		"ssh_info":        s.toolInfo,
	}
	return s
}

func (s *Server) call(name string, args map[string]any) (result toolResult) {
	started := time.Now()
	defer func() {
		if recovered := recover(); recovered != nil {
			result = errResult("内部错误: %v", recovered)
		}
		cfg, _ := s.mgr.Get()
		host, _ := args["host"].(string)
		errText := ""
		if result.IsError {
			errText = truncateString(result.Text, 500)
		}
		s.audit.Log(cfg, name, host, args, !result.IsError, time.Since(started), errText)
	}()
	handler := s.handlers[name]
	if handler == nil {
		return errResult("未知工具: %s", name)
	}
	return handler(args)
}

func (s *Server) resolve(args map[string]any) (*Host, *Config, error) {
	cfg, err := s.mgr.Get()
	if err != nil {
		return nil, nil, err
	}
	query, err := getString(args, "host", false, true)
	if err != nil {
		return nil, cfg, err
	}
	host, matches, err := cfg.Resolve(query)
	if err != nil {
		if len(matches) > 0 {
			return nil, cfg, fmt.Errorf("%v\n候选:\n%s", err, FormatMatches(matches))
		}
		return nil, cfg, err
	}
	return host, cfg, nil
}

func (s *Server) toolHosts(_ map[string]any) toolResult {
	cfg, err := s.mgr.Get()
	if err != nil {
		return errFrom(err)
	}
	lines := []string{
		"配置文件: " + s.mgr.Path(),
		fmt.Sprintf("主机数: %d", len(cfg.Hosts)),
		"默认主机: " + defaultString(cfg.DefaultHost, "(未设置)"),
		"",
	}
	if len(cfg.Hosts) == 0 {
		lines = append(lines, "(配置为空。可用 ssh_add_host 添加，例如：别名 虚拟机88.11，地址 192.168.88.11，root/你的密码)")
	}
	for i := range cfg.Hosts {
		host := &cfg.Hosts[i]
		jump := ""
		if host.Jump != "" {
			jump = "  跳板=" + host.Jump
		}
		lines = append(lines, fmt.Sprintf("  %-16s %-18s %s@%s:%d  认证=%s  别名=%s%s",
			host.ID, host.Name, host.User, host.Hostname, host.Port, host.AuthKind(),
			strings.Join(host.Aliases, ", "), jump))
	}
	if len(cfg.Warnings) > 0 {
		lines = append(lines, "", "警告:")
		for _, warning := range cfg.Warnings {
			lines = append(lines, "  - "+warning)
		}
	}
	return okResult("%s", strings.Join(lines, "\n"))
}

func (s *Server) toolFindHost(args map[string]any) toolResult {
	cfg, err := s.mgr.Get()
	if err != nil {
		return errFrom(err)
	}
	query, err := getString(args, "query", true, false)
	if err != nil {
		return errFrom(err)
	}
	limit, err := getInt(args, "limit", 20, 1, 100)
	if err != nil {
		return errFrom(err)
	}
	matches := cfg.FindMatches(query)
	if len(matches) == 0 {
		return errResult("未找到匹配 %q 的主机。可用主机: %s", query, cfg.HostIDs())
	}
	if len(matches) > limit {
		matches = matches[:limit]
	}
	lines := []string{fmt.Sprintf("查询: %s", query), "匹配:"}
	for _, match := range matches {
		lines = append(lines, fmt.Sprintf("  %s  %s@%s:%d  评分=%d  %s",
			match.Host.ID, match.Host.User, match.Host.Hostname, match.Host.Port, match.Score, match.Reason))
	}
	lines = append(lines, "", "下一步: ssh_connect 或 ssh_exec 的 host 参数可直接填上面的 id 或别名")
	return okResult("%s", strings.Join(lines, "\n"))
}

func (s *Server) toolAddHost(args map[string]any) toolResult {
	id, err := getString(args, "id", false, true)
	if err != nil {
		return errFrom(err)
	}
	alias, err := getString(args, "alias", false, true)
	if err != nil {
		return errFrom(err)
	}
	name, err := getString(args, "name", false, true)
	if err != nil {
		return errFrom(err)
	}
	if id == "" {
		id = alias
	}
	if id == "" {
		return errResult("缺少 id 或 alias")
	}
	if name == "" {
		name = id
	}
	hostname, err := getString(args, "hostname", true, false)
	if err != nil {
		return errFrom(err)
	}
	port, err := getInt(args, "port", 0, 1, 65535)
	if err != nil {
		return errFrom(err)
	}
	user, err := getString(args, "user", false, true)
	if err != nil {
		return errFrom(err)
	}
	password, err := getString(args, "password", false, true)
	if err != nil {
		return errFrom(err)
	}
	keyFile, err := getString(args, "key_file", false, true)
	if err != nil {
		return errFrom(err)
	}
	keyPassphrase, err := getString(args, "key_passphrase", false, true)
	if err != nil {
		return errFrom(err)
	}
	sudoPassword, err := getString(args, "sudo_password", false, true)
	if err != nil {
		return errFrom(err)
	}
	jump, err := getString(args, "jump", false, true)
	if err != nil {
		return errFrom(err)
	}
	jumpPassword, err := getString(args, "jump_password", false, true)
	if err != nil {
		return errFrom(err)
	}
	encoding, err := getString(args, "encoding", false, true)
	if err != nil {
		return errFrom(err)
	}
	hostKeyPolicy, err := getString(args, "host_key_policy", false, true)
	if err != nil {
		return errFrom(err)
	}
	description, err := getString(args, "description", false, true)
	if err != nil {
		return errFrom(err)
	}
	aliases, err := getStringSlice(args, "aliases")
	if err != nil {
		return errFrom(err)
	}
	tags, err := getStringSlice(args, "tags")
	if err != nil {
		return errFrom(err)
	}
	verify, err := getBool(args, "verify", true)
	if err != nil {
		return errFrom(err)
	}
	force, err := getBool(args, "force", false)
	if err != nil {
		return errFrom(err)
	}
	merge, err := getBool(args, "merge", true)
	if err != nil {
		return errFrom(err)
	}

	host := Host{
		ID:            id,
		Name:          name,
		Aliases:       append(aliases, alias),
		Hostname:      hostname,
		Port:          port,
		User:          user,
		Password:      password,
		KeyFile:       keyFile,
		KeyPassphrase: keyPassphrase,
		SudoPassword:  sudoPassword,
		Jump:          jump,
		JumpPassword:  jumpPassword,
		Encoding:      encoding,
		HostKeyPolicy: hostKeyPolicy,
		Description:   description,
		Tags:          tags,
	}
	cfg, err := s.mgr.Get()
	if err != nil {
		return errFrom(err)
	}
	host.ApplyDefaults(cfg.Defaults)

	verifyWarning := ""
	verifyDuration := time.Duration(0)
	if verify {
		duration, probeErr := ProbeHost(&host, cfg)
		if probeErr != nil {
			if !force {
				return errResult("验证失败，未保存: %v", probeErr)
			}
			verifyWarning = probeErr.Error()
		} else {
			verifyDuration = duration
		}
	}

	saved, mergedInto, err := s.mgr.AddHost(host, merge)
	if err != nil {
		return errFrom(err)
	}
	lines := []string{"已保存: " + DescribeHost(&saved)}
	if mergedInto != "" && !strings.EqualFold(mergedInto, saved.ID) {
		lines = append(lines, "已合并到现有主机: "+mergedInto)
	}
	if verifyWarning != "" {
		lines = append(lines, "[警告] 验证失败，但已按 force=true 保存: "+verifyWarning)
	} else if verify {
		lines = append(lines, fmt.Sprintf("验证通过，耗时 %d ms", verifyDuration.Milliseconds()))
	}
	return okResult("%s", strings.Join(lines, "\n"))
}

func (s *Server) toolForgetHost(args map[string]any) toolResult {
	query, err := getString(args, "host", true, false)
	if err != nil {
		return errFrom(err)
	}
	aliasOnly, err := getBool(args, "alias_only", false)
	if err != nil {
		return errFrom(err)
	}
	if aliasOnly {
		host, removed, err := s.mgr.RemoveAlias(query)
		if err != nil {
			return errFrom(err)
		}
		return okResult("已删除别名: %s（主机 %s 仍保留）\n%s", removed, host.ID, DescribeHost(&host))
	}
	removed, err := s.mgr.RemoveHost(query)
	if err != nil {
		return errFrom(err)
	}
	return okResult("已删除主机: %s", DescribeHost(&removed))
}

func (s *Server) toolConnect(args map[string]any) toolResult {
	host, cfg, err := s.resolve(args)
	if err != nil {
		return errFrom(err)
	}
	started := time.Now()
	if _, err := s.pool.Get(host, cfg); err != nil {
		return errFrom(err)
	}
	return okResult("OK %s -> %s@%s:%d (%d ms)\n别名: %s",
		host.ID, host.User, host.Hostname, host.Port,
		time.Since(started).Milliseconds(), strings.Join(host.Aliases, ", "))
}

func (s *Server) toolExec(args map[string]any) toolResult {
	host, cfg, err := s.resolve(args)
	if err != nil {
		return errFrom(err)
	}
	command, err := getString(args, "command", true, false)
	if err != nil {
		return errFrom(err)
	}
	timeout, err := getFloat(args, "timeout", float64(host.CommandTimeoutSeconds), 1, 3600)
	if err != nil {
		return errFrom(err)
	}
	maxBytes, err := getInt(args, "max_bytes", 262144, 1024, 8*1024*1024)
	if err != nil {
		return errFrom(err)
	}
	cwd, err := getString(args, "cwd", false, true)
	if err != nil {
		return errFrom(err)
	}
	env, err := getStringMap(args, "env")
	if err != nil {
		return errFrom(err)
	}
	pty, err := getBool(args, "pty", false)
	if err != nil {
		return errFrom(err)
	}
	fullCommand, err := buildCommand(command, cwd, env)
	if err != nil {
		return errFrom(err)
	}
	connection, err := s.pool.Get(host, cfg)
	if err != nil {
		return errFrom(err)
	}
	result, execErr := connection.Exec(fullCommand, ExecOptions{
		Timeout:  time.Duration(timeout * float64(time.Second)),
		MaxBytes: maxBytes,
		Pty:      pty,
		Encoding: host.Encoding,
	})
	if execErr != nil {
		if result.Stdout != "" || result.Stderr != "" {
			text := formatExec(result).Text + "\n[错误] " + execErr.Error()
			return toolResult{Text: text, IsError: true}
		}
		return errFrom(execErr)
	}
	return formatExec(result)
}

func (s *Server) toolSudo(args map[string]any) toolResult {
	host, cfg, err := s.resolve(args)
	if err != nil {
		return errFrom(err)
	}
	command, err := getString(args, "command", true, false)
	if err != nil {
		return errFrom(err)
	}
	timeout, err := getFloat(args, "timeout", float64(host.CommandTimeoutSeconds), 1, 3600)
	if err != nil {
		return errFrom(err)
	}
	maxBytes, err := getInt(args, "max_bytes", 262144, 1024, 8*1024*1024)
	if err != nil {
		return errFrom(err)
	}
	cwd, err := getString(args, "cwd", false, true)
	if err != nil {
		return errFrom(err)
	}
	env, err := getStringMap(args, "env")
	if err != nil {
		return errFrom(err)
	}
	pty, err := getBool(args, "pty", false)
	if err != nil {
		return errFrom(err)
	}
	password, err := getString(args, "sudo_password", false, true)
	if err != nil {
		return errFrom(err)
	}
	if password == "" {
		password = host.SudoPassword
	}
	if password == "" {
		password = host.Password
	}
	if password == "" {
		return errResult("未配置 sudo 密码。请在 ssh_config.json 中为该主机设置 sudo_password 或 password")
	}
	inner, err := buildCommand(command, cwd, env)
	if err != nil {
		return errFrom(err)
	}
	fullCommand := "sudo -S -p '' sh -c " + shellQuote(inner)
	connection, err := s.pool.Get(host, cfg)
	if err != nil {
		return errFrom(err)
	}
	result, execErr := connection.Exec(fullCommand, ExecOptions{
		Timeout:  time.Duration(timeout * float64(time.Second)),
		MaxBytes: maxBytes,
		Pty:      pty,
		Stdin:    password + "\n",
		Encoding: host.Encoding,
	})
	if execErr != nil {
		if result.Stdout != "" || result.Stderr != "" {
			text := formatExec(result).Text + "\n[错误] " + execErr.Error()
			return toolResult{Text: text, IsError: true}
		}
		return errFrom(execErr)
	}
	output := formatExec(result)
	if strings.Contains(strings.ToLower(result.Stderr), "sorry, try again") ||
		strings.Contains(strings.ToLower(result.Stderr), "incorrect password") {
		output.IsError = true
		output.Text += "\n[提示] sudo 密码错误，请检查 ssh_config.json"
	}
	return output
}

func (s *Server) toolRead(args map[string]any) toolResult {
	host, cfg, err := s.resolve(args)
	if err != nil {
		return errFrom(err)
	}
	path, err := getString(args, "path", true, false)
	if err != nil {
		return errFrom(err)
	}
	encoding, err := getString(args, "encoding", false, true)
	if err != nil {
		return errFrom(err)
	}
	if encoding == "" {
		encoding = host.Encoding
	}
	startLine, err := getInt(args, "start_line", 1, 1, 1_000_000_000)
	if err != nil {
		return errFrom(err)
	}
	maxLines, err := getInt(args, "max_lines", 2000, 1, 200000)
	if err != nil {
		return errFrom(err)
	}
	maxBytes, err := getInt(args, "max_bytes", 262144, 1024, 8*1024*1024)
	if err != nil {
		return errFrom(err)
	}
	connection, err := s.pool.Get(host, cfg)
	if err != nil {
		return errFrom(err)
	}
	text, err := readRemoteFile(connection, path, encoding, startLine, maxLines, maxBytes)
	if err != nil {
		return errFrom(err)
	}
	return okResult("%s", text)
}

func (s *Server) toolWrite(args map[string]any) toolResult {
	host, cfg, err := s.resolve(args)
	if err != nil {
		return errFrom(err)
	}
	path, err := getString(args, "path", true, false)
	if err != nil {
		return errFrom(err)
	}
	content, err := getRawString(args, "content", true, true)
	if err != nil {
		return errFrom(err)
	}
	mode, err := getString(args, "mode", false, true)
	if err != nil {
		return errFrom(err)
	}
	if mode == "" {
		mode = "overwrite"
	}
	if mode != "overwrite" && mode != "append" && mode != "create" {
		return errResult("mode 只能是 overwrite / append / create")
	}
	encoding, err := getString(args, "encoding", false, true)
	if err != nil {
		return errFrom(err)
	}
	if encoding == "" {
		encoding = host.Encoding
	}
	backup, err := getBool(args, "backup", true)
	if err != nil {
		return errFrom(err)
	}
	atomic, err := getBool(args, "atomic", true)
	if err != nil {
		return errFrom(err)
	}
	mkdirParents, err := getBool(args, "mkdir_parents", true)
	if err != nil {
		return errFrom(err)
	}
	data, err := encodeText(content, encoding)
	if err != nil {
		return errFrom(err)
	}
	connection, err := s.pool.Get(host, cfg)
	if err != nil {
		return errFrom(err)
	}
	message, err := writeRemoteFile(connection, path, data, mode, backup, atomic, mkdirParents)
	if err != nil {
		return errFrom(err)
	}
	return okResult("%s", message)
}

func (s *Server) toolList(args map[string]any) toolResult {
	host, cfg, err := s.resolve(args)
	if err != nil {
		return errFrom(err)
	}
	path, err := getString(args, "path", true, false)
	if err != nil {
		return errFrom(err)
	}
	recursive, err := getBool(args, "recursive", false)
	if err != nil {
		return errFrom(err)
	}
	maxEntries, err := getInt(args, "max_entries", 500, 1, 10000)
	if err != nil {
		return errFrom(err)
	}
	showHidden, err := getBool(args, "show_hidden", false)
	if err != nil {
		return errFrom(err)
	}
	connection, err := s.pool.Get(host, cfg)
	if err != nil {
		return errFrom(err)
	}
	text, err := listRemote(connection, path, recursive, maxEntries, showHidden)
	if err != nil {
		return errFrom(err)
	}
	return okResult("%s", text)
}

func (s *Server) toolStat(args map[string]any) toolResult {
	host, cfg, err := s.resolve(args)
	if err != nil {
		return errFrom(err)
	}
	path, err := getString(args, "path", true, false)
	if err != nil {
		return errFrom(err)
	}
	connection, err := s.pool.Get(host, cfg)
	if err != nil {
		return errFrom(err)
	}
	text, err := statRemote(connection, path)
	if err != nil {
		return errFrom(err)
	}
	return okResult("%s", text)
}

func (s *Server) toolMkdir(args map[string]any) toolResult {
	host, cfg, err := s.resolve(args)
	if err != nil {
		return errFrom(err)
	}
	path, err := getString(args, "path", true, false)
	if err != nil {
		return errFrom(err)
	}
	parents, err := getBool(args, "parents", true)
	if err != nil {
		return errFrom(err)
	}
	connection, err := s.pool.Get(host, cfg)
	if err != nil {
		return errFrom(err)
	}
	text, err := mkdirRemote(connection, path, parents)
	if err != nil {
		return errFrom(err)
	}
	return okResult("%s", text)
}

func (s *Server) toolRemove(args map[string]any) toolResult {
	host, cfg, err := s.resolve(args)
	if err != nil {
		return errFrom(err)
	}
	path, err := getString(args, "path", true, false)
	if err != nil {
		return errFrom(err)
	}
	recursive, err := getBool(args, "recursive", false)
	if err != nil {
		return errFrom(err)
	}
	connection, err := s.pool.Get(host, cfg)
	if err != nil {
		return errFrom(err)
	}
	text, err := removeRemote(connection, path, recursive)
	if err != nil {
		return errFrom(err)
	}
	return okResult("%s", text)
}

func (s *Server) toolMove(args map[string]any) toolResult {
	host, cfg, err := s.resolve(args)
	if err != nil {
		return errFrom(err)
	}
	src, err := getString(args, "src", true, false)
	if err != nil {
		return errFrom(err)
	}
	dst, err := getString(args, "dst", true, false)
	if err != nil {
		return errFrom(err)
	}
	overwrite, err := getBool(args, "overwrite", false)
	if err != nil {
		return errFrom(err)
	}
	connection, err := s.pool.Get(host, cfg)
	if err != nil {
		return errFrom(err)
	}
	text, err := moveRemote(connection, src, dst, overwrite)
	if err != nil {
		return errFrom(err)
	}
	return okResult("%s", text)
}

func (s *Server) toolUpload(args map[string]any) toolResult {
	host, cfg, err := s.resolve(args)
	if err != nil {
		return errFrom(err)
	}
	localPath, err := getString(args, "local_path", true, false)
	if err != nil {
		return errFrom(err)
	}
	remotePath, err := getString(args, "remote_path", true, false)
	if err != nil {
		return errFrom(err)
	}
	recursive, err := getBool(args, "recursive", false)
	if err != nil {
		return errFrom(err)
	}
	overwrite, err := getBool(args, "overwrite", true)
	if err != nil {
		return errFrom(err)
	}
	connection, err := s.pool.Get(host, cfg)
	if err != nil {
		return errFrom(err)
	}
	text, err := uploadPath(connection, localPath, remotePath, recursive, overwrite)
	if err != nil {
		return errFrom(err)
	}
	return okResult("%s", text)
}

func (s *Server) toolDownload(args map[string]any) toolResult {
	host, cfg, err := s.resolve(args)
	if err != nil {
		return errFrom(err)
	}
	remotePath, err := getString(args, "remote_path", true, false)
	if err != nil {
		return errFrom(err)
	}
	localPath, err := getString(args, "local_path", true, false)
	if err != nil {
		return errFrom(err)
	}
	recursive, err := getBool(args, "recursive", false)
	if err != nil {
		return errFrom(err)
	}
	overwrite, err := getBool(args, "overwrite", true)
	if err != nil {
		return errFrom(err)
	}
	connection, err := s.pool.Get(host, cfg)
	if err != nil {
		return errFrom(err)
	}
	text, err := downloadPath(connection, remotePath, localPath, recursive, overwrite)
	if err != nil {
		return errFrom(err)
	}
	return okResult("%s", text)
}

func (s *Server) toolInfo(args map[string]any) toolResult {
	host, cfg, err := s.resolve(args)
	if err != nil {
		return errFrom(err)
	}
	command := "(cat /etc/os-release 2>/dev/null | head -8 || true); " +
		"echo '---KERNEL---'; (uname -a 2>/dev/null || true); " +
		"echo '---CPU---'; (nproc 2>/dev/null || getconf _NPROCESSORS_ONLN 2>/dev/null || echo unknown); " +
		"echo '---MEM---'; (free -h 2>/dev/null | head -2 || head -3 /proc/meminfo 2>/dev/null || true); " +
		"echo '---DISK---'; (df -h / 2>/dev/null | tail -1 || true); " +
		"echo '---IP---'; (ip -4 addr show 2>/dev/null | grep 'inet ' | grep -v 127.0.0.1 | head -3 " +
		"|| ifconfig 2>/dev/null | grep 'inet ' | head -3 || true)"
	connection, err := s.pool.Get(host, cfg)
	if err != nil {
		return errFrom(err)
	}
	result, err := connection.Exec(command, ExecOptions{
		Timeout:  20 * time.Second,
		MaxBytes: 65536,
		Encoding: host.Encoding,
	})
	if err != nil {
		return errFrom(err)
	}
	text := strings.TrimSpace(result.Stdout)
	if strings.TrimSpace(result.Stderr) != "" {
		text += "\n--- stderr ---\n" + strings.TrimSpace(result.Stderr)
	}
	if text == "" {
		text = "(无输出)"
	}
	return toolResult{Text: text, IsError: result.TimedOut}
}

func formatExec(result ExecResult) toolResult {
	lines := []string{
		"[exit] " + exitString(result.ExitStatus),
		fmt.Sprintf("[耗时] %.2fs", result.Duration.Seconds()),
	}
	if result.TimedOut {
		lines = append(lines, "[超时] 已关闭会话，远端进程可能仍在运行")
	}
	if result.Truncated {
		lines = append(lines, "[截断] 输出超过 max_bytes，已停止读取")
	}
	if result.EncodingIssue {
		lines = append(lines, "[编码] 输出含无法解码的字节，已用替换字符显示")
	}
	if result.Stdout != "" {
		lines = append(lines, "--- stdout ---\n"+result.Stdout)
	}
	if result.Stderr != "" {
		lines = append(lines, "--- stderr ---\n"+result.Stderr)
	}
	if result.Stdout == "" && result.Stderr == "" {
		lines = append(lines, "(无输出)")
	}
	return toolResult{Text: strings.Join(lines, "\n"), IsError: result.TimedOut}
}

func exitString(status *int) string {
	if status == nil {
		return "未知"
	}
	return strconv.Itoa(*status)
}

func buildCommand(command, cwd string, env map[string]string) (string, error) {
	var prefix []string
	if strings.TrimSpace(cwd) != "" {
		prefix = append(prefix, "cd "+shellQuote(cwd))
	}
	if len(env) > 0 {
		keys := make([]string, 0, len(env))
		for key := range env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var exports []string
		for _, key := range keys {
			if !validEnvName(key) {
				return "", fmt.Errorf("env 变量名非法: %s", key)
			}
			exports = append(exports, key+"="+shellQuote(env[key]))
		}
		prefix = append(prefix, "export "+strings.Join(exports, " "))
	}
	if len(prefix) == 0 {
		return command, nil
	}
	return strings.Join(prefix, " && ") + " && " + command, nil
}

func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for index, r := range name {
		if index == 0 {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_') {
				return false
			}
			continue
		}
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return true
}

func getString(args map[string]any, key string, required, allowEmpty bool) (string, error) {
	value, ok := args[key]
	if !ok || value == nil {
		if required {
			return "", fmt.Errorf("缺少参数 %s", key)
		}
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("参数 %s 必须是字符串", key)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		if required && !allowEmpty {
			return "", fmt.Errorf("参数 %s 不能为空", key)
		}
		return "", nil
	}
	return text, nil
}

func getRawString(args map[string]any, key string, required, allowEmpty bool) (string, error) {
	value, ok := args[key]
	if !ok || value == nil {
		if required {
			return "", fmt.Errorf("缺少参数 %s", key)
		}
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("参数 %s 必须是字符串", key)
	}
	if text == "" && required && !allowEmpty {
		return "", fmt.Errorf("参数 %s 不能为空", key)
	}
	return text, nil
}

func getInt(args map[string]any, key string, def, min, max int) (int, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return def, nil
	}
	var number int
	switch v := value.(type) {
	case float64:
		number = int(v)
	case int:
		number = v
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, fmt.Errorf("参数 %s 必须是整数", key)
		}
		number = parsed
	default:
		return 0, fmt.Errorf("参数 %s 必须是整数", key)
	}
	if number < min {
		number = min
	}
	if number > max {
		number = max
	}
	return number, nil
}

func getFloat(args map[string]any, key string, def, min, max float64) (float64, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return def, nil
	}
	var number float64
	switch v := value.(type) {
	case float64:
		number = v
	case int:
		number = float64(v)
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, fmt.Errorf("参数 %s 必须是数字", key)
		}
		number = parsed
	default:
		return 0, fmt.Errorf("参数 %s 必须是数字", key)
	}
	if number < min {
		number = min
	}
	if number > max {
		number = max
	}
	return number, nil
}

func getBool(args map[string]any, key string, def bool) (bool, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return def, nil
	}
	switch v := value.(type) {
	case bool:
		return v, nil
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			return false, fmt.Errorf("参数 %s 必须是布尔值", key)
		}
		return parsed, nil
	case float64:
		return v != 0, nil
	default:
		return false, fmt.Errorf("参数 %s 必须是布尔值", key)
	}
}

func getStringSlice(args map[string]any, key string) ([]string, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return nil, nil
	}
	switch v := value.(type) {
	case []any:
		var out []string
		for _, item := range v {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("参数 %s 必须是字符串数组", key)
			}
			if strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
		return out, nil
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, nil
		}
		return []string{strings.TrimSpace(v)}, nil
	default:
		return nil, fmt.Errorf("参数 %s 必须是字符串数组", key)
	}
}

func getStringMap(args map[string]any, key string) (map[string]string, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return nil, nil
	}
	raw, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("参数 %s 必须是对象", key)
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		out[k] = fmt.Sprint(v)
	}
	return out, nil
}
