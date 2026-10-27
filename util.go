package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func expandHome(p string) string {
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err == nil {
			if p == "~" {
				return home
			}
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func humanSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	value := float64(n)
	units := []string{"K", "M", "G", "T"}
	for _, unit := range units {
		value /= 1024
		if value < 1024 || unit == "T" {
			return fmt.Sprintf("%.1f%s", value, unit)
		}
	}
	return fmt.Sprintf("%.1fT", value)
}

func isGBK(encoding string) bool {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "gbk", "gb2312", "cp936":
		return true
	}
	return false
}

func isGB18030(encoding string) bool {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "gb18030":
		return true
	}
	return false
}

func decodeText(data []byte, encoding string) (string, bool) {
	if isGBK(encoding) {
		out, err := simplifiedchinese.GBK.NewDecoder().Bytes(data)
		if err != nil {
			return strings.ToValidUTF8(string(data), "\uFFFD"), true
		}
		return string(out), false
	}
	if isGB18030(encoding) {
		out, err := simplifiedchinese.GB18030.NewDecoder().Bytes(data)
		if err != nil {
			return strings.ToValidUTF8(string(data), "\uFFFD"), true
		}
		return string(out), false
	}
	if !utf8.Valid(data) {
		return strings.ToValidUTF8(string(data), "\uFFFD"), true
	}
	return string(data), false
}

func encodeText(text string, encoding string) ([]byte, error) {
	if isGBK(encoding) {
		return simplifiedchinese.GBK.NewEncoder().Bytes([]byte(text))
	}
	if isGB18030(encoding) {
		return simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(text))
	}
	return []byte(text), nil
}

func sanitizeArgs(args map[string]any) map[string]any {
	if args == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(args))
	for key, value := range args {
		switch key {
		case "password", "sudo_password", "key_passphrase", "content":
			out[key] = "<redacted>"
		case "command":
			text := fmt.Sprint(value)
			if len(text) > 500 {
				text = text[:500] + "..."
			}
			out[key] = text
		default:
			out[key] = value
		}
	}
	return out
}

type AuditLogger struct {
	mu sync.Mutex
}

func (a *AuditLogger) Log(cfg *Config, tool, host string, args map[string]any, ok bool, duration time.Duration, errText string) {
	if cfg == nil || !cfg.Defaults.Audit {
		return
	}
	dir := expandHome(cfg.Defaults.AuditDir)
	if dir == "" {
		dir = expandHome("~/.ssh-mcp/logs")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	record := map[string]any{
		"ts":          time.Now().Format(time.RFC3339),
		"tool":        tool,
		"host":        host,
		"ok":          ok,
		"duration_ms": duration.Milliseconds(),
		"args":        sanitizeArgs(args),
		"error":       errText,
	}
	line, err := json.Marshal(record)
	if err != nil {
		return
	}
	path := filepath.Join(dir, "audit-"+time.Now().Format("2006-01")+".jsonl")
	a.mu.Lock()
	defer a.mu.Unlock()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

func truncateString(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}
