package acp

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func normalizeAdditionalDirectories(cwd string, directories []string, supported bool) ([]string, error) {
	if len(directories) == 0 {
		return nil, nil
	}
	if !supported {
		return nil, fmt.Errorf("ACP agent does not support additional workspace directories")
	}
	seen := map[string]struct{}{filepath.Clean(cwd): {}}
	out := make([]string, 0, len(directories))
	for _, directory := range directories {
		if !filepath.IsAbs(directory) {
			return nil, fmt.Errorf("additional workspace directory must be absolute, got %q", directory)
		}
		clean := filepath.Clean(directory)
		if _, duplicate := seen[clean]; duplicate {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	return out, nil
}

func normalizeMCPServers(configs []ports.ChatMCPServerConfig, caps acpsdk.McpCapabilities) ([]acpsdk.McpServer, error) {
	if len(configs) == 0 {
		return []acpsdk.McpServer{}, nil
	}
	if !caps.Acp && !caps.Http && !caps.Sse {
		return nil, fmt.Errorf("ACP agent does not support per-session MCP servers")
	}
	servers := make([]acpsdk.McpServer, 0, len(configs))
	for _, config := range configs {
		name := strings.TrimSpace(config.Name)
		if name == "" {
			return nil, fmt.Errorf("ACP MCP server name is required")
		}
		switch strings.ToLower(strings.TrimSpace(config.Type)) {
		case "", "stdio":
			if strings.TrimSpace(config.Command) == "" {
				return nil, fmt.Errorf("ACP MCP server %q requires a command", name)
			}
			servers = append(servers, acpsdk.McpServer{Stdio: &acpsdk.McpServerStdio{
				Name: name, Command: config.Command, Args: config.Args,
				Env: environmentVariables(config.Env),
			}})
		case "http":
			if !caps.Http {
				return nil, fmt.Errorf("ACP agent does not support HTTP MCP server %q", name)
			}
			if strings.TrimSpace(config.URL) == "" {
				return nil, fmt.Errorf("ACP MCP server %q requires a URL", name)
			}
			servers = append(servers, acpsdk.McpServer{Http: &acpsdk.McpServerHttpInline{
				Name: name, Type: "http", Url: config.URL, Headers: httpHeaders(config.Headers),
			}})
		case "sse":
			if !caps.Sse {
				return nil, fmt.Errorf("ACP agent does not support SSE MCP server %q", name)
			}
			if strings.TrimSpace(config.URL) == "" {
				return nil, fmt.Errorf("ACP MCP server %q requires a URL", name)
			}
			servers = append(servers, acpsdk.McpServer{Sse: &acpsdk.McpServerSseInline{
				Name: name, Type: "sse", Url: config.URL, Headers: httpHeaders(config.Headers),
			}})
		default:
			return nil, fmt.Errorf("unsupported ACP MCP transport %q for %q", config.Type, name)
		}
	}
	return servers, nil
}

func environmentVariables(values map[string]string) []acpsdk.EnvVariable {
	keys := sortedMapKeys(values)
	out := make([]acpsdk.EnvVariable, 0, len(keys))
	for _, key := range keys {
		out = append(out, acpsdk.EnvVariable{Name: key, Value: values[key]})
	}
	return out
}

func httpHeaders(values map[string]string) []acpsdk.HttpHeader {
	keys := sortedMapKeys(values)
	out := make([]acpsdk.HttpHeader, 0, len(keys))
	for _, key := range keys {
		out = append(out, acpsdk.HttpHeader{Name: key, Value: values[key]})
	}
	return out
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
