package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const serverVersion = "1.0"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

var stdoutMu sync.Mutex

func configPath() string {
	if env := os.Getenv("SSH_MCP_CONFIG"); env != "" {
		return expandHome(env)
	}
	exe, err := os.Executable()
	dir := ""
	if err == nil {
		dir = filepath.Dir(exe)
	} else {
		dir, _ = os.Getwd()
	}
	yamlPath := filepath.Join(dir, "ssh_config.yaml")
	if _, err := os.Stat(yamlPath); err == nil {
		return yamlPath
	}
	jsonPath := filepath.Join(dir, "ssh_config.json")
	if _, err := os.Stat(jsonPath); err == nil {
		return jsonPath
	}
	return yamlPath
}

func main() {
	path := configPath()
	if _, err := os.Stat(path); err == nil {
		restrictFile(path)
	}
	mgr := NewConfigManager(path)
	srv := NewServer(mgr)
	if err := srv.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "ssh-mcp-go:", err)
		os.Exit(1)
	}
}

func (s *Server) Run() error {
	defer s.pool.CloseAll()
	reader := bufio.NewReaderSize(os.Stdin, 4*1024*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			s.handleLine(line)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func (s *Server) handleLine(line []byte) {
	var request rpcRequest
	if err := json.Unmarshal(line, &request); err != nil {
		writeError(nil, -32700, "JSON 解析失败: "+err.Error())
		return
	}
	if len(request.ID) == 0 {
		if request.Method == "exit" {
			os.Exit(0)
		}
		return
	}
	writeResponse(s.handleRequest(request))
}

func (s *Server) handleRequest(request rpcRequest) rpcResponse {
	switch request.Method {
	case "initialize":
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      request.ID,
			Result: map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]any{
					"tools": map[string]any{"listChanged": false},
				},
				"serverInfo": map[string]any{
					"name":    "ssh-mcp-go",
					"version": serverVersion,
				},
				"instructions": "使用 ssh_hosts 查看已有主机；ssh_find_host 按别名/名称/IP 尾段查找；ssh_add_host 记住新主机；ssh_connect 验证连接；ssh_exec 执行命令。别名示例：88.11、虚拟机88.11、node1。",
			},
		}
	case "ping":
		return rpcResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{}}
	case "shutdown":
		return rpcResponse{JSONRPC: "2.0", ID: request.ID, Result: nil}
	case "tools/list":
		return rpcResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{"tools": Tools}}
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return rpcResponse{JSONRPC: "2.0", ID: request.ID, Error: &rpcError{Code: -32602, Message: "tools/call 参数错误: " + err.Error()}}
		}
		result := s.call(params.Name, params.Arguments)
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      request.ID,
			Result: map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": result.Text},
				},
				"isError": result.IsError,
			},
		}
	default:
		return rpcResponse{JSONRPC: "2.0", ID: request.ID, Error: &rpcError{Code: -32601, Message: "未知方法: " + request.Method}}
	}
}

func writeResponse(response rpcResponse) {
	data, err := json.Marshal(response)
	if err != nil {
		writeError(response.ID, -32603, "响应序列化失败: "+err.Error())
		return
	}
	writeLine(data)
}

func writeError(id json.RawMessage, code int, message string) {
	response := rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: message},
	}
	data, err := json.Marshal(response)
	if err != nil {
		return
	}
	writeLine(data)
}

func writeLine(data []byte) {
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	_, _ = os.Stdout.Write(append(data, '\n'))
}
