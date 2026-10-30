//go:build ignore

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type message struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  *json.RawMessage `json:"params,omitempty"`
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	discoverCalls := 0
	// A stateless server answers server/discover, accepts subscriptions/listen and
	// rejects list requests that lack the protocol version in _meta.
	stateless := os.Getenv("MOCK_STATELESS") == "1"

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg message
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}

		if stateless && strings.HasSuffix(msg.Method, "/list") && !hasProtocolVersion(msg.Params) {
			respondError(msg.ID, -32022, "missing protocol version", nil)
			continue
		}

		switch msg.Method {
		case "server/discover":
			discoverCalls++
			if msg.ID == nil {
				continue
			}
			if !stateless {
				respondError(msg.ID, -32601, "Method server/discover not found",
					map[string]any{"discoverCalls": discoverCalls})
				continue
			}
			respond(msg.ID, map[string]any{
				"supportedVersions": []string{"2026-07-28"},
				"capabilities": map[string]any{
					"tools":     map[string]any{"listChanged": true},
					"resources": map[string]any{},
					"prompts":   map[string]any{},
				},
				"discoverCalls": discoverCalls,
			})
		case "subscriptions/listen":
			notify("notifications/subscriptions/acknowledged", map[string]any{
				"_meta":         map[string]any{"io.modelcontextprotocol/subscriptionId": msg.ID},
				"notifications": map[string]any{"toolsListChanged": true},
			})
		case "initialize":
			respond(msg.ID, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]any{
					"tools":     map[string]any{},
					"resources": map[string]any{},
					"prompts":   map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    "mock-server",
					"version": "0.1.0",
				},
			})
		case "notifications/initialized":
			// no response
		case "tools/list":
			respond(msg.ID, map[string]any{
				"tools": []any{
					map[string]any{
						"name":        "echo",
						"description": "Echo tool",
						"inputSchema": map[string]any{
							"type":       "object",
							"properties": map[string]any{"text": map[string]any{"type": "string"}},
						},
					},
				},
			})
		case "resources/list":
			respond(msg.ID, map[string]any{"resources": []any{}})
		case "resources/templates/list":
			respond(msg.ID, map[string]any{"resourceTemplates": []any{}})
		case "prompts/list":
			respond(msg.ID, map[string]any{"prompts": []any{}})
		case "tools/call":
			var params struct {
				Arguments struct {
					Text string `json:"text"`
				} `json:"arguments"`
			}
			if msg.Params != nil {
				json.Unmarshal(*msg.Params, &params)
			}
			respond(msg.ID, map[string]any{
				"content": []any{
					map[string]any{"type": "text", "text": fmt.Sprintf("echo: %s", params.Arguments.Text)},
				},
			})
		default:
			if msg.ID != nil {
				respondError(msg.ID, -32601, fmt.Sprintf("Method %s not found", msg.Method), nil)
			}
		}
	}
}

func hasProtocolVersion(params *json.RawMessage) bool {
	if params == nil {
		return false
	}
	var body struct {
		Meta map[string]any `json:"_meta"`
	}
	json.Unmarshal(*params, &body)
	_, ok := body.Meta["io.modelcontextprotocol/protocolVersion"]
	return ok
}

func notify(method string, params any) {
	out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	fmt.Fprintf(os.Stdout, "%s\n", out)
}

func respondError(id *json.RawMessage, code int, message string, data any) {
	rpcErr := map[string]any{"code": code, "message": message}
	if data != nil {
		rpcErr["data"] = data
	}
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   rpcErr,
	}
	out, _ := json.Marshal(resp)
	fmt.Fprintf(os.Stdout, "%s\n", out)
}

func respond(id *json.RawMessage, result any) {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	}
	data, _ := json.Marshal(resp)
	fmt.Fprintf(os.Stdout, "%s\n", data)
}
