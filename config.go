package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type Defaults struct {
	Port                  int    `json:"port" yaml:"port"`
	TimeoutSeconds        int    `json:"timeout_seconds" yaml:"timeout_seconds"`
	CommandTimeoutSeconds int    `json:"command_timeout_seconds" yaml:"command_timeout_seconds"`
	Encoding              string `json:"encoding" yaml:"encoding"`
	HostKeyPolicy         string `json:"host_key_policy" yaml:"host_key_policy"`
	KnownHosts            string `json:"known_hosts" yaml:"known_hosts"`
	KeepaliveSeconds      int    `json:"keepalive_seconds" yaml:"keepalive_seconds"`
	Audit                 bool   `json:"audit" yaml:"audit"`
	AuditDir              string `json:"audit_dir" yaml:"audit_dir"`
}

type Host struct {
	ID                    string   `json:"id" yaml:"id"`
	Name                  string   `json:"name,omitempty" yaml:"name,omitempty"`
	Aliases               []string `json:"aliases,omitempty" yaml:"aliases,omitempty"`
	Hostname              string   `json:"hostname" yaml:"hostname"`
	Port                  int      `json:"port,omitempty" yaml:"port,omitempty"`
	User                  string   `json:"user,omitempty" yaml:"user,omitempty"`
	Password              string   `json:"password,omitempty" yaml:"password,omitempty"`
	KeyFile               string   `json:"key_file,omitempty" yaml:"key_file,omitempty"`
	KeyPassphrase         string   `json:"key_passphrase,omitempty" yaml:"key_passphrase,omitempty"`
	SudoPassword          string   `json:"sudo_password,omitempty" yaml:"sudo_password,omitempty"`
	Jump                  string   `json:"jump,omitempty" yaml:"jump,omitempty"`
	JumpPassword          string   `json:"jump_password,omitempty" yaml:"jump_password,omitempty"`
	KnownHosts            string   `json:"known_hosts,omitempty" yaml:"known_hosts,omitempty"`
	Encoding              string   `json:"encoding,omitempty" yaml:"encoding,omitempty"`
	HostKeyPolicy         string   `json:"host_key_policy,omitempty" yaml:"host_key_policy,omitempty"`
	TimeoutSeconds        int      `json:"timeout_seconds,omitempty" yaml:"timeout_seconds,omitempty"`
	CommandTimeoutSeconds int      `json:"command_timeout_seconds,omitempty" yaml:"command_timeout_seconds,omitempty"`
	KeepaliveSeconds      int      `json:"keepalive_seconds,omitempty" yaml:"keepalive_seconds,omitempty"`
	Tags                  []string `json:"tags,omitempty" yaml:"tags,omitempty"`
	Description           string   `json:"description,omitempty" yaml:"description,omitempty"`
}

type Config struct {
	Version     int      `json:"version" yaml:"version"`
	DefaultHost string   `json:"default_host,omitempty" yaml:"default_host,omitempty"`
	Defaults    Defaults `json:"defaults" yaml:"defaults"`
	Hosts       []Host   `json:"hosts" yaml:"hosts"`

	Path       string   `json:"-" yaml:"-"`
	Warnings   []string `json:"-" yaml:"-"`
	generation int
}

func defaultDefaults() Defaults {
	return Defaults{
		Port:                  22,
		TimeoutSeconds:        10,
		CommandTimeoutSeconds: 30,
		Encoding:              "utf-8",
		HostKeyPolicy:         "auto",
		KnownHosts:            "~/.ssh/known_hosts",
		KeepaliveSeconds:      30,
		Audit:                 true,
		AuditDir:              "~/.ssh-mcp/logs",
	}
}

func (h *Host) ApplyDefaults(d Defaults) {
	if h.Port == 0 {
		h.Port = d.Port
	}
	if h.Port == 0 {
		h.Port = 22
	}
	if h.User == "" {
		h.User = "root"
	}
	if h.Encoding == "" {
		h.Encoding = d.Encoding
	}
	if h.Encoding == "" {
		h.Encoding = "utf-8"
	}
	if h.HostKeyPolicy == "" {
		h.HostKeyPolicy = d.HostKeyPolicy
	}
	if h.HostKeyPolicy == "" {
		h.HostKeyPolicy = "auto"
	}
	if h.TimeoutSeconds == 0 {
		h.TimeoutSeconds = d.TimeoutSeconds
	}
	if h.TimeoutSeconds == 0 {
		h.TimeoutSeconds = 10
	}
	if h.CommandTimeoutSeconds == 0 {
		h.CommandTimeoutSeconds = d.CommandTimeoutSeconds
	}
	if h.CommandTimeoutSeconds == 0 {
		h.CommandTimeoutSeconds = 30
	}
	if h.KeepaliveSeconds == 0 {
		h.KeepaliveSeconds = d.KeepaliveSeconds
	}
	if h.Name == "" {
		h.Name = h.ID
	}
	h.Aliases = normalizeList(append(append(h.Aliases, h.ID, h.Name), h.Aliases...))
	h.Tags = normalizeList(h.Tags)
}

func (h *Host) KnownHostsPath(d Defaults) string {
	if h.KnownHosts != "" {
		return expandHome(h.KnownHosts)
	}
	return expandHome(d.KnownHosts)
}

func (h *Host) AuthKind() string {
	if h.Password != "" {
		return "password"
	}
	if h.KeyFile != "" {
		return "key"
	}
	return "agent/default-key"
}

func normalizeList(items []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key := strings.ToLower(item)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	return out
}

func (c *Config) normalize() {
	base := defaultDefaults()
	if c.Defaults.Port == 0 {
		c.Defaults.Port = base.Port
	}
	if c.Defaults.TimeoutSeconds == 0 {
		c.Defaults.TimeoutSeconds = base.TimeoutSeconds
	}
	if c.Defaults.CommandTimeoutSeconds == 0 {
		c.Defaults.CommandTimeoutSeconds = base.CommandTimeoutSeconds
	}
	if c.Defaults.Encoding == "" {
		c.Defaults.Encoding = base.Encoding
	}
	if c.Defaults.HostKeyPolicy == "" {
		c.Defaults.HostKeyPolicy = base.HostKeyPolicy
	}
	if c.Defaults.KnownHosts == "" {
		c.Defaults.KnownHosts = base.KnownHosts
	}
	if c.Defaults.KeepaliveSeconds == 0 {
		c.Defaults.KeepaliveSeconds = base.KeepaliveSeconds
	}
	if c.Defaults.AuditDir == "" {
		c.Defaults.AuditDir = base.AuditDir
	}
	if c.Version == 0 {
		c.Version = 1
	}

	c.Warnings = nil
	seenIDs := map[string]bool{}
	seenAliases := map[string]string{}
	hosts := make([]Host, 0, len(c.Hosts))
	for _, host := range c.Hosts {
		host.ID = strings.TrimSpace(host.ID)
		host.Name = strings.TrimSpace(host.Name)
		host.Hostname = strings.TrimSpace(host.Hostname)
		if host.ID == "" {
			c.Warnings = append(c.Warnings, "跳过没有 id 的主机")
			continue
		}
		key := strings.ToLower(host.ID)
		if seenIDs[key] {
			c.Warnings = append(c.Warnings, "跳过重复 id: "+host.ID)
			continue
		}
		seenIDs[key] = true
		if host.Hostname == "" {
			c.Warnings = append(c.Warnings, "主机 "+host.ID+" 没有 hostname")
		}
		host.ApplyDefaults(c.Defaults)
		for _, alias := range host.Aliases {
			aliasKey := strings.ToLower(alias)
			if owner, ok := seenAliases[aliasKey]; ok && owner != host.ID {
				c.Warnings = append(c.Warnings, fmt.Sprintf("别名 %q 同时属于 %s 和 %s", alias, owner, host.ID))
				continue
			}
			seenAliases[aliasKey] = host.ID
		}
		hosts = append(hosts, host)
	}
	c.Hosts = hosts
}

func isYAMLConfig(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return true
	}
	return false
}

func prettifyYAML(data []byte) []byte {
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	out := make([]string, 0, len(lines)+len(lines)/6+3)
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "defaults:") || strings.HasPrefix(line, "hosts:"):
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
		case strings.HasPrefix(line, "  - id:"):
			if len(out) > 0 && !strings.HasSuffix(out[len(out)-1], "hosts:") {
				out = append(out, "")
			}
		}
		out = append(out, line)
	}
	body := strings.Join(out, "\n") + "\n"
	header := "# ssh-mcp 配置（含明文密码，请勿外传）\n" +
		"# 由 ssh-mcp.exe 自动维护；下次保存时会重新排版。\n\n"
	return []byte(header + body)
}

func LoadConfig(path string) (*Config, error) {
	cfg := &Config{Version: 1, Defaults: defaultDefaults()}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg.Path = path
		cfg.normalize()
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	if isYAMLConfig(path) {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("配置 YAML 解析失败: %w", err)
		}
	} else if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("配置 JSON 解析失败: %w", err)
	}
	cfg.Path = path
	cfg.normalize()
	return cfg, nil
}

func SaveConfig(path string, cfg *Config) error {
	cfg.Path = path
	cfg.normalize()

	saveCfg := *cfg
	saveCfg.Hosts = make([]Host, len(cfg.Hosts))
	copy(saveCfg.Hosts, cfg.Hosts)
	for index := range saveCfg.Hosts {
		host := &saveCfg.Hosts[index]
		if host.Port == cfg.Defaults.Port {
			host.Port = 0
		}
		if host.Encoding == cfg.Defaults.Encoding {
			host.Encoding = ""
		}
		if host.HostKeyPolicy == cfg.Defaults.HostKeyPolicy {
			host.HostKeyPolicy = ""
		}
		if host.TimeoutSeconds == cfg.Defaults.TimeoutSeconds {
			host.TimeoutSeconds = 0
		}
		if host.CommandTimeoutSeconds == cfg.Defaults.CommandTimeoutSeconds {
			host.CommandTimeoutSeconds = 0
		}
		if host.KeepaliveSeconds == cfg.Defaults.KeepaliveSeconds {
			host.KeepaliveSeconds = 0
		}
	}

	var data []byte
	var err error
	if isYAMLConfig(path) {
		var buffer bytes.Buffer
		encoder := yaml.NewEncoder(&buffer)
		encoder.SetIndent(2)
		if err := encoder.Encode(&saveCfg); err != nil {
			return fmt.Errorf("序列化 YAML 配置失败: %w", err)
		}
		_ = encoder.Close()
		data = prettifyYAML(buffer.Bytes())
	} else {
		data, err = json.MarshalIndent(&saveCfg, "", "  ")
		if err != nil {
			return fmt.Errorf("序列化 JSON 配置失败: %w", err)
		}
		data = append(data, '\n')
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".ssh_config-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时配置失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	_ = tmp.Chmod(0o600)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时配置失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("同步临时配置失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时配置失败: %w", err)
	}
	restrictFile(tmpName)
	if err := replaceFile(tmpName, path); err != nil {
		return fmt.Errorf("替换配置失败: %w", err)
	}
	restrictFile(path)
	return nil
}

type ConfigManager struct {
	mu         sync.RWMutex
	path       string
	cfg        *Config
	modTime    time.Time
	size       int64
	generation int
}

func NewConfigManager(path string) *ConfigManager {
	return &ConfigManager{path: path}
}

func (m *ConfigManager) Path() string {
	return m.path
}

func (m *ConfigManager) Get() (*Config, error) {
	info, statErr := os.Stat(m.path)
	var modTime time.Time
	var size int64
	if statErr == nil {
		modTime = info.ModTime()
		size = info.Size()
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("读取配置状态失败: %w", statErr)
	}

	m.mu.RLock()
	cached := m.cfg
	unchanged := cached != nil && m.modTime.Equal(modTime) && m.size == size
	m.mu.RUnlock()
	if unchanged {
		return cached, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, err := LoadConfig(m.path)
	if err != nil {
		return nil, err
	}
	m.generation++
	cfg.generation = m.generation
	m.cfg = cfg
	m.modTime = modTime
	m.size = size
	return cfg, nil
}

func (m *ConfigManager) Save(cfg *Config) error {
	if err := SaveConfig(m.path, cfg); err != nil {
		return err
	}
	info, err := os.Stat(m.path)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generation++
	cfg.generation = m.generation
	m.cfg = cfg
	m.modTime = info.ModTime()
	m.size = info.Size()
	return nil
}

func (c *Config) Generation() int {
	return c.generation
}

func (c *Config) HostIDs() string {
	var ids []string
	for _, host := range c.Hosts {
		ids = append(ids, host.ID)
	}
	if len(ids) == 0 {
		return "(空)"
	}
	return strings.Join(ids, ", ")
}

type Match struct {
	Host   *Host
	Score  int
	Reason string
}

func (c *Config) FindMatches(query string) []Match {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}
	qLower := strings.ToLower(q)
	isIPLike := true
	for _, r := range q {
		if (r < '0' || r > '9') && r != '.' {
			isIPLike = false
			break
		}
	}

	best := map[string]Match{}
	consider := func(host *Host, score int, reason string) {
		current, ok := best[host.ID]
		if !ok || score > current.Score {
			best[host.ID] = Match{Host: host, Score: score, Reason: reason}
		}
	}

	for i := range c.Hosts {
		host := &c.Hosts[i]
		hostLower := strings.ToLower(host.Hostname)
		switch {
		case strings.EqualFold(host.ID, q):
			consider(host, 100, "id 精确匹配")
		case strings.EqualFold(host.Name, q):
			consider(host, 96, "名称精确匹配")
		case containsEqualFold(host.Aliases, q):
			consider(host, 92, "别名精确匹配")
		case strings.EqualFold(host.Hostname, q):
			consider(host, 88, "主机地址精确匹配")
		case isIPLike && strings.HasSuffix(hostLower, "."+qLower):
			consider(host, 84, "IP 尾段匹配")
		case isIPLike && strings.HasPrefix(hostLower, qLower+"."):
			consider(host, 80, "IP 前缀匹配")
		case containsEqualFold(host.Tags, q):
			consider(host, 74, "标签精确匹配")
		}

		if strings.Contains(strings.ToLower(host.ID), qLower) ||
			strings.Contains(strings.ToLower(host.Name), qLower) ||
			containsFoldSubstring(host.Aliases, qLower) {
			consider(host, 62, "名称/别名包含")
		}
		if strings.Contains(hostLower, qLower) && !isIPLike {
			consider(host, 58, "主机地址包含")
		}
		if containsFoldSubstring(host.Tags, qLower) || strings.Contains(strings.ToLower(host.Description), qLower) {
			consider(host, 54, "标签/描述包含")
		}
	}

	matches := make([]Match, 0, len(best))
	for _, match := range best {
		matches = append(matches, match)
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].Host.ID < matches[j].Host.ID
	})
	return matches
}

func containsEqualFold(items []string, query string) bool {
	for _, item := range items {
		if strings.EqualFold(item, query) {
			return true
		}
	}
	return false
}

func containsFoldSubstring(items []string, queryLower string) bool {
	for _, item := range items {
		if strings.Contains(strings.ToLower(item), queryLower) {
			return true
		}
	}
	return false
}

func (c *Config) Resolve(query string) (*Host, []Match, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		q = c.DefaultHost
	}
	if q == "" {
		return nil, nil, fmt.Errorf("请提供主机别名。当前配置可用主机: %s", c.HostIDs())
	}
	matches := c.FindMatches(q)
	if len(matches) == 0 {
		return nil, nil, fmt.Errorf("未找到主机 %q。当前配置可用主机: %s", q, c.HostIDs())
	}
	if len(matches) == 1 {
		return matches[0].Host, matches, nil
	}
	if matches[0].Score > matches[1].Score {
		return matches[0].Host, matches, nil
	}
	return nil, matches, fmt.Errorf("别名 %q 匹配到多台主机，请明确指定", q)
}

func FormatMatches(matches []Match) string {
	if len(matches) == 0 {
		return "(无匹配)"
	}
	var lines []string
	for _, match := range matches {
		lines = append(lines, fmt.Sprintf("  %s  %s@%s:%d  评分=%d  %s",
			match.Host.ID, match.Host.User, match.Host.Hostname, match.Host.Port,
			match.Score, match.Reason))
	}
	return strings.Join(lines, "\n")
}

func findExactHost(cfg *Config, query string) (*Host, int) {
	for i := range cfg.Hosts {
		host := &cfg.Hosts[i]
		if strings.EqualFold(host.ID, query) ||
			strings.EqualFold(host.Name, query) ||
			containsEqualFold(host.Aliases, query) {
			return host, i
		}
	}
	return nil, -1
}

func mergeList(base []string, extra ...string) []string {
	return normalizeList(append(base, extra...))
}

func (m *ConfigManager) AddHost(host Host, merge bool) (Host, string, error) {
	cfg, err := m.Get()
	if err != nil {
		return Host{}, "", err
	}
	host.ApplyDefaults(cfg.Defaults)
	if host.ID == "" {
		return Host{}, "", fmt.Errorf("缺少 id")
	}
	if host.Hostname == "" {
		return Host{}, "", fmt.Errorf("缺少 hostname")
	}

	existing, index := findExactHost(cfg, host.ID)
	if existing == nil {
		for i := range cfg.Hosts {
			if strings.EqualFold(cfg.Hosts[i].Hostname, host.Hostname) &&
				cfg.Hosts[i].Port == host.Port {
				existing = &cfg.Hosts[i]
				index = i
				break
			}
		}
	}

	mergedInto := ""
	if existing != nil {
		if !merge && !strings.EqualFold(existing.ID, host.ID) {
			return Host{}, "", fmt.Errorf("地址 %s:%d 已被主机 %s 使用，merge=false 时不能重复添加",
				host.Hostname, host.Port, existing.ID)
		}
		mergedInto = existing.ID
		extraAliases := append([]string{}, host.Aliases...)
		extraAliases = append(extraAliases, host.ID, host.Name)
		existing.Aliases = mergeList(existing.Aliases, extraAliases...)
		if host.Name != "" && existing.Name == "" {
			existing.Name = host.Name
		}
		if host.Hostname != "" {
			existing.Hostname = host.Hostname
		}
		if host.Port != 0 {
			existing.Port = host.Port
		}
		if host.User != "" {
			existing.User = host.User
		}
		if host.Password != "" {
			existing.Password = host.Password
		}
		if host.KeyFile != "" {
			existing.KeyFile = host.KeyFile
		}
		if host.KeyPassphrase != "" {
			existing.KeyPassphrase = host.KeyPassphrase
		}
		if host.SudoPassword != "" {
			existing.SudoPassword = host.SudoPassword
		}
		if host.Jump != "" {
			existing.Jump = host.Jump
		}
		if host.JumpPassword != "" {
			existing.JumpPassword = host.JumpPassword
		}
		if host.KnownHosts != "" {
			existing.KnownHosts = host.KnownHosts
		}
		if host.Encoding != "" {
			existing.Encoding = host.Encoding
		}
		if host.HostKeyPolicy != "" {
			existing.HostKeyPolicy = host.HostKeyPolicy
		}
		if len(host.Tags) > 0 {
			existing.Tags = mergeList(existing.Tags, host.Tags...)
		}
		if host.Description != "" {
			existing.Description = host.Description
		}
		if host.TimeoutSeconds > 0 {
			existing.TimeoutSeconds = host.TimeoutSeconds
		}
		if host.CommandTimeoutSeconds > 0 {
			existing.CommandTimeoutSeconds = host.CommandTimeoutSeconds
		}
		if host.KeepaliveSeconds > 0 {
			existing.KeepaliveSeconds = host.KeepaliveSeconds
		}
		cfg.Hosts[index] = *existing
	} else {
		cfg.Hosts = append(cfg.Hosts, host)
		existing = &cfg.Hosts[len(cfg.Hosts)-1]
	}

	if cfg.DefaultHost == "" {
		cfg.DefaultHost = existing.ID
	}
	if err := m.Save(cfg); err != nil {
		return Host{}, "", err
	}
	return *existing, mergedInto, nil
}

func (m *ConfigManager) RemoveHost(query string) (Host, error) {
	cfg, err := m.Get()
	if err != nil {
		return Host{}, err
	}
	host, matches, err := cfg.Resolve(query)
	if err != nil {
		if len(matches) > 0 {
			return Host{}, fmt.Errorf("%v\n候选:\n%s", err, FormatMatches(matches))
		}
		return Host{}, err
	}
	for i := range cfg.Hosts {
		if cfg.Hosts[i].ID == host.ID {
			removed := cfg.Hosts[i]
			cfg.Hosts = append(cfg.Hosts[:i], cfg.Hosts[i+1:]...)
			if strings.EqualFold(cfg.DefaultHost, removed.ID) {
				cfg.DefaultHost = ""
			}
			if err := m.Save(cfg); err != nil {
				return Host{}, err
			}
			return removed, nil
		}
	}
	return Host{}, fmt.Errorf("未找到主机: %s", host.ID)
}

func (m *ConfigManager) RemoveAlias(query string) (Host, string, error) {
	cfg, err := m.Get()
	if err != nil {
		return Host{}, "", err
	}
	host, matches, err := cfg.Resolve(query)
	if err != nil {
		if len(matches) > 0 {
			return Host{}, "", fmt.Errorf("%v\n候选:\n%s", err, FormatMatches(matches))
		}
		return Host{}, "", err
	}
	q := strings.TrimSpace(query)
	if strings.EqualFold(host.ID, q) || strings.EqualFold(host.Name, q) {
		return Host{}, "", fmt.Errorf("不能删除主 id 或名称 %q；如需删除整台主机，请用 alias_only=false", query)
	}
	for i := range cfg.Hosts {
		if cfg.Hosts[i].ID != host.ID {
			continue
		}
		removed := ""
		kept := make([]string, 0, len(cfg.Hosts[i].Aliases))
		for _, alias := range cfg.Hosts[i].Aliases {
			if strings.EqualFold(alias, q) {
				removed = alias
				continue
			}
			kept = append(kept, alias)
		}
		if removed == "" {
			return Host{}, "", fmt.Errorf("%q 不是主机 %s 的别名", query, host.ID)
		}
		cfg.Hosts[i].Aliases = kept
		if err := m.Save(cfg); err != nil {
			return Host{}, "", err
		}
		return cfg.Hosts[i], removed, nil
	}
	return Host{}, "", fmt.Errorf("未找到主机: %s", host.ID)
}

func DescribeHost(host *Host) string {
	return fmt.Sprintf("%s (%s) -> %s@%s:%s  认证=%s  跳板=%s  别名=%s  标签=%s",
		host.ID, host.Name, host.User, host.Hostname, strconv.Itoa(host.Port),
		host.AuthKind(), defaultString(host.Jump, "-"),
		strings.Join(host.Aliases, ", "), strings.Join(host.Tags, ", "))
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
