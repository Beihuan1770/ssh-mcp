package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type ExecOptions struct {
	Timeout  time.Duration
	MaxBytes int
	Pty      bool
	Stdin    string
	Encoding string
}

type ExecResult struct {
	Stdout        string
	Stderr        string
	ExitStatus    *int
	Truncated     bool
	TimedOut      bool
	Duration      time.Duration
	EncodingIssue bool
}

type Connection struct {
	host       *Host
	cfg        *Config
	client     *ssh.Client
	parents    []*ssh.Client
	sftpClient *sftp.Client
	sftpMu     sync.Mutex
	dead       int32
	stop       chan struct{}
	closeOnce  sync.Once
}

func (c *Connection) alive() bool {
	return c.client != nil && atomic.LoadInt32(&c.dead) == 0
}

func (c *Connection) Close() {
	c.closeOnce.Do(func() {
		if c.stop != nil {
			close(c.stop)
		}
		if c.sftpClient != nil {
			_ = c.sftpClient.Close()
		}
		if c.client != nil {
			_ = c.client.Close()
		}
		for i := len(c.parents) - 1; i >= 0; i-- {
			_ = c.parents[i].Close()
		}
	})
}

func (c *Connection) WithSFTP(fn func(*sftp.Client) error) error {
	c.sftpMu.Lock()
	defer c.sftpMu.Unlock()
	if c.sftpClient == nil {
		client, err := sftp.NewClient(c.client)
		if err != nil {
			return fmt.Errorf("打开 SFTP 失败: %w", err)
		}
		c.sftpClient = client
	}
	return fn(c.sftpClient)
}

type Pool struct {
	mgr   *ConfigManager
	mu    sync.Mutex
	conns map[string]*Connection
	locks map[string]*sync.Mutex
}

func NewPool(mgr *ConfigManager) *Pool {
	return &Pool{
		mgr:   mgr,
		conns: map[string]*Connection{},
		locks: map[string]*sync.Mutex{},
	}
}

func (p *Pool) Get(host *Host, cfg *Config) (*Connection, error) {
	key := fmt.Sprintf("%d|%s", cfg.Generation(), host.ID)
	prefix := fmt.Sprintf("%d|", cfg.Generation())

	p.mu.Lock()
	for oldKey, connection := range p.conns {
		if !strings.HasPrefix(oldKey, prefix) {
			connection.Close()
			delete(p.conns, oldKey)
			delete(p.locks, oldKey)
		}
	}
	lock := p.locks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		p.locks[key] = lock
	}
	p.mu.Unlock()

	lock.Lock()
	defer lock.Unlock()

	p.mu.Lock()
	connection := p.conns[key]
	p.mu.Unlock()
	if connection != nil && connection.alive() {
		return connection, nil
	}
	if connection != nil {
		connection.Close()
		p.mu.Lock()
		delete(p.conns, key)
		p.mu.Unlock()
	}

	connection, err := dialHost(host, cfg)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.conns[key] = connection
	p.mu.Unlock()
	return connection, nil
}

func (p *Pool) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, connection := range p.conns {
		connection.Close()
		delete(p.conns, key)
	}
	p.locks = map[string]*sync.Mutex{}
}

func buildChain(host *Host, cfg *Config, seen map[string]bool) ([]*Host, error) {
	if seen[host.ID] {
		return nil, fmt.Errorf("跳板机循环引用: %s", host.ID)
	}
	seen[host.ID] = true

	if strings.TrimSpace(host.Jump) == "" {
		return []*Host{host}, nil
	}
	jumpHost, err := resolveJumpHost(host, cfg)
	if err != nil {
		return nil, err
	}
	prefix, err := buildChain(jumpHost, cfg, seen)
	if err != nil {
		return nil, err
	}
	return append(prefix, host), nil
}

func resolveJumpHost(host *Host, cfg *Config) (*Host, error) {
	spec := strings.TrimSpace(host.Jump)
	if spec == "" {
		return nil, nil
	}
	if exact, _ := findExactHost(cfg, spec); exact != nil {
		return exact, nil
	}
	user, hostname, port, err := parseJumpSpec(spec)
	if err != nil {
		return nil, err
	}
	jump := &Host{
		ID:               "jump:" + spec,
		Name:             spec,
		Hostname:         hostname,
		Port:             port,
		User:             user,
		Password:         host.JumpPassword,
		Encoding:         host.Encoding,
		HostKeyPolicy:    host.HostKeyPolicy,
		TimeoutSeconds:   host.TimeoutSeconds,
		KeepaliveSeconds: host.KeepaliveSeconds,
	}
	jump.ApplyDefaults(cfg.Defaults)
	if jump.Password == "" {
		jump.Password = host.JumpPassword
	}
	return jump, nil
}

func parseJumpSpec(spec string) (user, host string, port int, err error) {
	s := strings.TrimSpace(spec)
	if at := strings.LastIndex(s, "@"); at >= 0 {
		user = s[:at]
		s = s[at+1:]
	}
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return "", "", 0, fmt.Errorf("无法解析跳板机: %s", spec)
		}
		host = s[1:end]
		rest := s[end+1:]
		if strings.HasPrefix(rest, ":") {
			port, _ = strconv.Atoi(rest[1:])
		}
	} else if strings.Count(s, ":") == 1 {
		colon := strings.LastIndex(s, ":")
		host = s[:colon]
		port, _ = strconv.Atoi(s[colon+1:])
	} else {
		host = s
	}
	if host == "" {
		return "", "", 0, fmt.Errorf("无法解析跳板机: %s，格式应为 [user@]host[:port]", spec)
	}
	return user, host, port, nil
}

func makeSSHConfig(host *Host, cfg *Config) (*ssh.ClientConfig, error) {
	auth, err := authMethods(host)
	if err != nil {
		return nil, fmt.Errorf("主机 %s 认证配置错误: %w", host.ID, err)
	}
	return &ssh.ClientConfig{
		User:            host.User,
		Auth:            auth,
		HostKeyCallback: hostKeyCallback(host, cfg),
		Timeout:         time.Duration(host.TimeoutSeconds) * time.Second,
	}, nil
}

func authMethods(host *Host) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if host.Password != "" {
		methods = append(methods, ssh.Password(host.Password))
	}
	if host.KeyFile != "" {
		data, err := os.ReadFile(expandHome(host.KeyFile))
		if err != nil {
			if host.Password == "" {
				return nil, fmt.Errorf("读取私钥失败: %w", err)
			}
		} else {
			var signer ssh.Signer
			if host.KeyPassphrase != "" {
				signer, err = ssh.ParsePrivateKeyWithPassphrase(data, []byte(host.KeyPassphrase))
			} else {
				signer, err = ssh.ParsePrivateKey(data)
			}
			if err != nil {
				if host.Password == "" {
					return nil, fmt.Errorf("解析私钥失败: %w", err)
				}
			} else {
				methods = append(methods, ssh.PublicKeys(signer))
			}
		}
	}
	if len(methods) == 0 {
		home, _ := os.UserHomeDir()
		for _, name := range []string{"id_ed25519", "id_rsa", "id_ecdsa"} {
			data, err := os.ReadFile(filepath.Join(home, ".ssh", name))
			if err != nil {
				continue
			}
			signer, err := ssh.ParsePrivateKey(data)
			if err != nil {
				continue
			}
			methods = append(methods, ssh.PublicKeys(signer))
			break
		}
	}
	if len(methods) == 0 {
		return nil, fmt.Errorf("未配置密码或私钥")
	}
	return methods, nil
}

var knownHostsMu sync.Mutex

func hostKeyCallback(host *Host, cfg *Config) ssh.HostKeyCallback {
	policy := strings.ToLower(strings.TrimSpace(host.HostKeyPolicy))
	if policy == "off" {
		return ssh.InsecureIgnoreHostKey()
	}
	path := host.KnownHostsPath(cfg.Defaults)
	if path == "" {
		path = expandHome("~/.ssh/known_hosts")
	}
	if dir := filepath.Dir(path); dir != "" {
		_ = os.MkdirAll(dir, 0o700)
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		_ = os.WriteFile(path, []byte{}, 0o600)
	}
	base, err := knownhosts.New(path)
	if err != nil {
		return func(_ string, _ net.Addr, _ ssh.PublicKey) error {
			return fmt.Errorf("known_hosts 解析失败 (%s): %w", path, err)
		}
	}
	if policy == "strict" {
		return base
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		callbackErr := base(hostname, remote, key)
		if callbackErr == nil {
			return nil
		}
		var keyErr *knownhosts.KeyError
		if errors.As(callbackErr, &keyErr) && len(keyErr.Want) == 0 {
			return appendKnownHost(path, hostname, key)
		}
		return fmt.Errorf("主机密钥校验失败: %w", callbackErr)
	}
}

func appendKnownHost(path, hostname string, key ssh.PublicKey) error {
	knownHostsMu.Lock()
	defer knownHostsMu.Unlock()
	line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("追加 known_hosts 失败: %w", err)
	}
	defer f.Close()
	_, err = f.WriteString(line + "\n")
	return err
}

func dialHost(host *Host, cfg *Config) (*Connection, error) {
	chain, err := buildChain(host, cfg, map[string]bool{})
	if err != nil {
		return nil, err
	}

	var clients []*ssh.Client
	var previous *ssh.Client
	closeClients := func() {
		for i := len(clients) - 1; i >= 0; i-- {
			_ = clients[i].Close()
		}
	}

	for _, hop := range chain {
		addr := net.JoinHostPort(hop.Hostname, strconv.Itoa(hop.Port))
		config, err := makeSSHConfig(hop, cfg)
		if err != nil {
			closeClients()
			return nil, err
		}

		var raw net.Conn
		if previous == nil {
			raw, err = net.DialTimeout("tcp", addr, time.Duration(hop.TimeoutSeconds)*time.Second)
		} else {
			raw, err = previous.Dial("tcp", addr)
		}
		if err != nil {
			closeClients()
			return nil, fmt.Errorf("连接 %s (%s) 失败: %w", hop.ID, addr, err)
		}

		sshConn, channels, requests, err := ssh.NewClientConn(raw, addr, config)
		if err != nil {
			_ = raw.Close()
			closeClients()
			return nil, wrapSSHError(hop, addr, err)
		}
		client := ssh.NewClient(sshConn, channels, requests)
		clients = append(clients, client)
		previous = client
	}

	connection := &Connection{
		host:    host,
		cfg:     cfg,
		client:  clients[len(clients)-1],
		parents: clients[:len(clients)-1],
	}
	go func() {
		_ = connection.client.Wait()
		atomic.StoreInt32(&connection.dead, 1)
	}()
	startKeepalive(connection)
	return connection, nil
}

func wrapSSHError(host *Host, addr string, err error) error {
	var keyErr *knownhosts.KeyError
	if errors.As(err, &keyErr) {
		if len(keyErr.Want) > 0 {
			return fmt.Errorf("主机密钥与 known_hosts 不一致: %s (%s)", host.ID, addr)
		}
		return fmt.Errorf("主机 %s (%s) 不在 known_hosts 中，且策略不允许自动接受", host.ID, addr)
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "unable to authenticate") || strings.Contains(text, "no supported methods") {
		return fmt.Errorf("认证失败: %s (%s)。请检查密码、私钥或 ssh-agent: %w", host.ID, addr, err)
	}
	return fmt.Errorf("SSH 连接失败: %s (%s): %w", host.ID, addr, err)
}

func startKeepalive(connection *Connection) {
	interval := time.Duration(connection.host.KeepaliveSeconds) * time.Second
	if interval <= 0 {
		return
	}
	connection.stop = make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-connection.stop:
				return
			case <-ticker.C:
				if _, _, err := connection.client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
					atomic.StoreInt32(&connection.dead, 1)
					return
				}
			}
		}
	}()
}

func (c *Connection) Exec(command string, opt ExecOptions) (ExecResult, error) {
	if opt.Timeout <= 0 {
		opt.Timeout = time.Duration(c.host.CommandTimeoutSeconds) * time.Second
	}
	if opt.MaxBytes <= 0 {
		opt.MaxBytes = 256 * 1024
	}
	if opt.Encoding == "" {
		opt.Encoding = c.host.Encoding
	}

	started := time.Now()
	session, err := c.client.NewSession()
	if err != nil {
		return ExecResult{}, fmt.Errorf("创建 SSH 会话失败: %w", err)
	}
	defer session.Close()

	if opt.Pty {
		modes := ssh.TerminalModes{
			ssh.ECHO:          1,
			ssh.TTY_OP_ISPEED: 14400,
			ssh.TTY_OP_OSPEED: 14400,
		}
		_ = session.RequestPty("xterm", 40, 120, modes)
	}

	stdoutPipe, err := session.StdoutPipe()
	if err != nil {
		return ExecResult{}, fmt.Errorf("获取 stdout 失败: %w", err)
	}
	stderrPipe, err := session.StderrPipe()
	if err != nil {
		return ExecResult{}, fmt.Errorf("获取 stderr 失败: %w", err)
	}
	stdinPipe, err := session.StdinPipe()
	if err != nil {
		return ExecResult{}, fmt.Errorf("获取 stdin 失败: %w", err)
	}

	if err := session.Start(command); err != nil {
		return ExecResult{}, fmt.Errorf("启动命令失败: %w", err)
	}
	if opt.Stdin != "" {
		_, _ = io.WriteString(stdinPipe, opt.Stdin)
	}
	_ = stdinPipe.Close()

	collector := &outputCollector{limit: opt.MaxBytes}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(collector.writer(false), stdoutPipe)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(collector.writer(true), stderrPipe)
	}()

	var timedOut int32
	var timer *time.Timer
	if opt.Timeout > 0 {
		timer = time.AfterFunc(opt.Timeout, func() {
			atomic.StoreInt32(&timedOut, 1)
			_ = session.Signal(ssh.SIGKILL)
			_ = session.Close()
		})
	}
	waitErr := session.Wait()
	if timer != nil {
		timer.Stop()
	}
	wg.Wait()

	result := ExecResult{
		Stdout:    "",
		Stderr:    "",
		Truncated: collector.truncated,
		TimedOut:  atomic.LoadInt32(&timedOut) == 1,
		Duration:  time.Since(started),
	}
	if collector.stdout.Len() > 0 {
		result.Stdout, result.EncodingIssue = decodeText(collector.stdout.Bytes(), opt.Encoding)
	}
	if collector.stderr.Len() > 0 {
		text, issue := decodeText(collector.stderr.Bytes(), opt.Encoding)
		result.Stderr = text
		result.EncodingIssue = result.EncodingIssue || issue
	}
	if result.TimedOut {
		return result, nil
	}
	if waitErr == nil {
		status := 0
		result.ExitStatus = &status
		return result, nil
	}
	var exitErr *ssh.ExitError
	if errors.As(waitErr, &exitErr) {
		status := exitErr.ExitStatus()
		result.ExitStatus = &status
		return result, nil
	}
	if !errors.Is(waitErr, io.EOF) {
		return result, fmt.Errorf("命令执行中断: %w", waitErr)
	}
	return result, nil
}

type outputCollector struct {
	mu        sync.Mutex
	total     int
	limit     int
	truncated bool
	stdout    bytes.Buffer
	stderr    bytes.Buffer
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) {
	return f(p)
}

func (c *outputCollector) writer(isErr bool) io.Writer {
	return writerFunc(func(p []byte) (int, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		remaining := c.limit - c.total
		if remaining > 0 {
			take := len(p)
			if take > remaining {
				take = remaining
			}
			if isErr {
				_, _ = c.stderr.Write(p[:take])
			} else {
				_, _ = c.stdout.Write(p[:take])
			}
			c.total += take
		}
		if len(p) > remaining {
			c.truncated = true
		}
		return len(p), nil
	})
}

func ProbeHost(host *Host, cfg *Config) (time.Duration, error) {
	started := time.Now()
	connection, err := dialHost(host, cfg)
	if err != nil {
		return 0, err
	}
	defer connection.Close()
	result, err := connection.Exec("echo ok", ExecOptions{
		Timeout:  10 * time.Second,
		MaxBytes: 1024,
		Encoding: host.Encoding,
	})
	if err != nil {
		return 0, err
	}
	if result.ExitStatus == nil {
		return 0, fmt.Errorf("验证命令没有返回退出码: %s", strings.TrimSpace(result.Stderr))
	}
	if *result.ExitStatus != 0 {
		return 0, fmt.Errorf("验证命令退出码 %d: %s", *result.ExitStatus, strings.TrimSpace(result.Stderr))
	}
	return time.Since(started), nil
}
