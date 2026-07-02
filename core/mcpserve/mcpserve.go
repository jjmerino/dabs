// Package mcpserve serves the dabash MCP tool over stdio (newline-delimited
// JSON-RPC 2.0, per the Model Context Protocol). One tool is exposed:
// dabash(command, cwd?) — "dumb user bash" — which executes a shell command
// in whatever box the injected exec function is bound (curried) to. The
// package knows nothing about sandboxes or drivers: callers inject exec.
package mcpserve

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// Exec runs a shell command line (optionally in cwd) and returns its
// combined output; a non-nil error marks the tool result as an error.
type Exec func(command, cwd string) (string, error)

const toolDescription = "Run a shell command inside YOUR machine. " +
	"This is your only capability; there is no other filesystem or host."

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve answers MCP requests from in on out until EOF. Every dabash call is
// delegated to exec.
func Serve(in io.Reader, out io.Writer, exec Exec) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	enc := json.NewEncoder(out)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			continue // not a JSON-RPC message; nothing to answer
		}
		if req.ID == nil {
			continue // notification (e.g. notifications/initialized) — no reply
		}
		if err := enc.Encode(handle(req, exec)); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func handle(req request, exec Exec) response {
	resp := response{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "dabash", "version": "0.1.0"},
		}
	case "tools/list":
		resp.Result = map[string]any{"tools": []map[string]any{{
			"name":        "dabash",
			"description": toolDescription,
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{"type": "string", "description": "shell command line"},
					"cwd":     map[string]any{"type": "string", "description": "directory to run in (optional)"},
				},
				"required": []string{"command"},
			},
		}}}
	case "tools/call":
		resp.Result = call(req.Params, exec)
	default:
		resp.Error = &rpcError{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)}
	}
	return resp
}

func call(rawParams json.RawMessage, exec Exec) map[string]any {
	var p struct {
		Name      string `json:"name"`
		Arguments struct {
			Command string `json:"command"`
			Cwd     string `json:"cwd"`
		} `json:"arguments"`
	}
	toolResult := func(text string, isErr bool) map[string]any {
		return map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
			"isError": isErr,
		}
	}
	if err := json.Unmarshal(rawParams, &p); err != nil {
		return toolResult(fmt.Sprintf("bad params: %v", err), true)
	}
	if p.Name != "dabash" {
		return toolResult(fmt.Sprintf("unknown tool %q", p.Name), true)
	}
	if p.Arguments.Command == "" {
		return toolResult("missing required argument: command", true)
	}
	out, err := exec(p.Arguments.Command, p.Arguments.Cwd)
	if err != nil {
		return toolResult(fmt.Sprintf("%s\n%v", out, err), true)
	}
	return toolResult(out, false)
}
