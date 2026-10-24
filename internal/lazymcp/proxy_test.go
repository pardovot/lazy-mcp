package lazymcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func buildMockServer(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "mock_server")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	_, thisFile, _, _ := runtime.Caller(0)
	src := filepath.Join(filepath.Dir(thisFile), "testdata", "mock_server.go")
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to build mock server: %v", err)
	}
	return bin
}

func buildProxy(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "lazy-mcp")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "../..")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to build proxy: %v", err)
	}
	return bin
}

func request(id any, method string, params ...string) string {
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if len(params) > 0 {
		msg["params"] = json.RawMessage(params[0])
	}
	data, _ := json.Marshal(msg)
	return string(data)
}

func notification(method string) string {
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	return string(data)
}

const initParams = `{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"0.1.0"}}`

func runProxy(t *testing.T, proxyBin, serverBin, cacheDir string, messages []string, numResponses int) []map[string]any {
	t.Helper()
	input := strings.Join(messages, "\n") + "\n"

	cmd := exec.Command(proxyBin, "--", serverBin)
	cmd.Env = append(os.Environ(), fmt.Sprintf("LAZY_MCP_CACHE_DIR=%s", cacheDir))
	cmd.Stdin = strings.NewReader(input)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	scanner := bufio.NewScanner(stdout)
	var responses []map[string]any
	for i := 0; i < numResponses && scanner.Scan(); i++ {
		var resp map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response: %v\nraw: %s", err, scanner.Text())
		}
		responses = append(responses, resp)
	}

	io.Copy(io.Discard, stdout)
	cmd.Wait()
	return responses
}

func TestNoCachePath(t *testing.T) {
	proxyBin := buildProxy(t)
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()

	messages := []string{
		request(1, "initialize", initParams),
		notification("notifications/initialized"),
		request(2, "tools/list"),
		request(3, "tools/call", `{"name":"echo","arguments":{"text":"hi"}}`),
	}

	responses := runProxy(t, proxyBin, serverBin, cacheDir, messages, 3)

	if len(responses) < 3 {
		t.Fatalf("got %d responses, want 3", len(responses))
	}

	result := responses[0]["result"].(map[string]any)
	serverInfo := result["serverInfo"].(map[string]any)
	if serverInfo["name"] != "mock-server" {
		t.Fatalf("got server name %v", serverInfo["name"])
	}

	result = responses[1]["result"].(map[string]any)
	tools := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}

	result = responses[2]["result"].(map[string]any)
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if text != "echo: hi" {
		t.Fatalf("got %q, want %q", text, "echo: hi")
	}

	files, _ := filepath.Glob(filepath.Join(cacheDir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("got %d cache files, want 1", len(files))
	}
}

func TestCachedPath(t *testing.T) {
	proxyBin := buildProxy(t)
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()

	// First run: build cache
	messages := []string{
		request(1, "initialize", initParams),
		notification("notifications/initialized"),
		request(2, "tools/list"),
		request(3, "tools/call", `{"name":"echo","arguments":{"text":"x"}}`),
	}
	runProxy(t, proxyBin, serverBin, cacheDir, messages, 3)

	// Second run: only discovery
	messages = []string{
		request(10, "initialize", initParams),
		notification("notifications/initialized"),
		request(11, "tools/list"),
	}
	responses := runProxy(t, proxyBin, serverBin, cacheDir, messages, 2)

	if len(responses) < 2 {
		t.Fatalf("got %d responses, want 2", len(responses))
	}

	id := responses[0]["id"]
	if fmt.Sprintf("%v", id) != "10" {
		t.Fatalf("got id %v, want 10", id)
	}
}

func TestLiveTransition(t *testing.T) {
	proxyBin := buildProxy(t)
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()

	// First run: build cache
	messages := []string{
		request(1, "initialize", initParams),
		notification("notifications/initialized"),
		request(2, "tools/list"),
		request(3, "tools/call", `{"name":"echo","arguments":{"text":"x"}}`),
	}
	runProxy(t, proxyBin, serverBin, cacheDir, messages, 3)

	// Second run: cache then live
	messages = []string{
		request(10, "initialize", initParams),
		notification("notifications/initialized"),
		request(11, "tools/list"),
		request(12, "tools/call", `{"name":"echo","arguments":{"text":"live"}}`),
	}
	responses := runProxy(t, proxyBin, serverBin, cacheDir, messages, 3)

	if len(responses) < 3 {
		t.Fatalf("got %d responses, want 3", len(responses))
	}

	result := responses[2]["result"].(map[string]any)
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if text != "echo: live" {
		t.Fatalf("got %q, want %q", text, "echo: live")
	}
}

func TestBadCommand(t *testing.T) {
	proxyBin := buildProxy(t)
	cacheDir := t.TempDir()

	input := request(1, "initialize", initParams) + "\n"
	cmd := exec.Command(proxyBin, "--", "nonexistent-command-xyz")
	cmd.Env = append(os.Environ(), fmt.Sprintf("LAZY_MCP_CACHE_DIR=%s", cacheDir))
	cmd.Stdin = strings.NewReader(input)
	out, _ := cmd.Output()

	var resp map[string]any
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("failed to parse: %v\nraw: %s", err, out)
	}
	if resp["error"] == nil {
		t.Fatal("expected error response")
	}
}

// --- In-process integration tests ---

// parseResponses extracts JSON-RPC responses from proxy stdout output.
func parseResponses(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	var responses []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var resp map[string]any
		if err := json.Unmarshal(line, &resp); err != nil {
			t.Fatalf("failed to parse response: %v\nraw: %s", err, line)
		}
		responses = append(responses, resp)
	}
	return responses
}

func runProxyInProcess(t *testing.T, serverBin string, cacheDir string, messages []string) ([]map[string]any, error) {
	t.Helper()
	t.Setenv("LAZY_MCP_CACHE_DIR", cacheDir)

	input := strings.Join(messages, "\n") + "\n"
	stdin := strings.NewReader(input)
	var stdout bytes.Buffer

	cache := NewCache([]string{serverBin})
	proxy := NewProxy([]string{serverBin}, cache, stdin, &stdout)
	err := proxy.Run()
	return parseResponses(t, stdout.Bytes()), err
}

func TestInProcess_NoCachePath(t *testing.T) {
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()

	messages := []string{
		request(1, "initialize", initParams),
		notification("notifications/initialized"),
		request(2, "tools/list"),
		request(3, "tools/call", `{"name":"echo","arguments":{"text":"hello"}}`),
	}

	responses, err := runProxyInProcess(t, serverBin, cacheDir, messages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(responses) < 3 {
		t.Fatalf("got %d responses, want at least 3", len(responses))
	}

	// Verify initialize response
	result := responses[0]["result"].(map[string]any)
	serverInfo := result["serverInfo"].(map[string]any)
	if serverInfo["name"] != "mock-server" {
		t.Fatalf("got server name %v, want mock-server", serverInfo["name"])
	}

	// Verify tools/list response
	result = responses[1]["result"].(map[string]any)
	tools := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}

	// Verify tools/call response
	result = responses[2]["result"].(map[string]any)
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if text != "echo: hello" {
		t.Fatalf("got %q, want %q", text, "echo: hello")
	}

	// Verify cache was created
	files, _ := filepath.Glob(filepath.Join(cacheDir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("got %d cache files, want 1", len(files))
	}
}

func TestInProcess_CachedPath(t *testing.T) {
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()

	// First run: build cache
	messages := []string{
		request(1, "initialize", initParams),
		notification("notifications/initialized"),
		request(2, "tools/list"),
		request(3, "tools/call", `{"name":"echo","arguments":{"text":"x"}}`),
	}
	_, err := runProxyInProcess(t, serverBin, cacheDir, messages)
	if err != nil {
		t.Fatalf("first run error: %v", err)
	}

	// Second run: only discovery - should all come from cache
	messages = []string{
		request(10, "initialize", initParams),
		notification("notifications/initialized"),
		request(11, "tools/list"),
	}
	responses, err := runProxyInProcess(t, serverBin, cacheDir, messages)
	if err != nil {
		t.Fatalf("second run error: %v", err)
	}

	if len(responses) != 2 {
		t.Fatalf("got %d responses, want 2", len(responses))
	}

	// Verify IDs match the second run requests
	if fmt.Sprintf("%v", responses[0]["id"]) != "10" {
		t.Fatalf("got id %v, want 10", responses[0]["id"])
	}
	if fmt.Sprintf("%v", responses[1]["id"]) != "11" {
		t.Fatalf("got id %v, want 11", responses[1]["id"])
	}
}

func TestInProcess_LiveTransition(t *testing.T) {
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()

	// First run: build cache
	messages := []string{
		request(1, "initialize", initParams),
		notification("notifications/initialized"),
		request(2, "tools/list"),
		request(3, "tools/call", `{"name":"echo","arguments":{"text":"x"}}`),
	}
	_, err := runProxyInProcess(t, serverBin, cacheDir, messages)
	if err != nil {
		t.Fatalf("first run error: %v", err)
	}

	// Second run: cache then live
	messages = []string{
		request(10, "initialize", initParams),
		notification("notifications/initialized"),
		request(11, "tools/list"),
		request(12, "tools/call", `{"name":"echo","arguments":{"text":"live"}}`),
	}
	responses, err := runProxyInProcess(t, serverBin, cacheDir, messages)
	if err != nil {
		t.Fatalf("second run error: %v", err)
	}

	if len(responses) < 3 {
		t.Fatalf("got %d responses, want at least 3", len(responses))
	}

	// First two from cache, third from live server
	result := responses[2]["result"].(map[string]any)
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if text != "echo: live" {
		t.Fatalf("got %q, want %q", text, "echo: live")
	}
}

func TestInProcess_BadCommand(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("LAZY_MCP_CACHE_DIR", cacheDir)

	input := request(1, "initialize", initParams) + "\n"
	stdin := strings.NewReader(input)
	var stdout bytes.Buffer

	cache := NewCache([]string{"nonexistent-command-xyz"})
	proxy := NewProxy([]string{"nonexistent-command-xyz"}, cache, stdin, &stdout)
	err := proxy.Run()
	if err != nil {
		t.Fatalf("expected nil error (error written to stdout), got: %v", err)
	}

	responses := parseResponses(t, stdout.Bytes())
	if len(responses) != 1 {
		t.Fatalf("got %d responses, want 1", len(responses))
	}
	if responses[0]["error"] == nil {
		t.Fatal("expected error response")
	}
}

func TestInProcess_BadCommandNotification(t *testing.T) {
	// When server fails to start on a notification (no ID), no error response is written
	cacheDir := t.TempDir()
	t.Setenv("LAZY_MCP_CACHE_DIR", cacheDir)

	input := notification("notifications/initialized") + "\n"
	stdin := strings.NewReader(input)
	var stdout bytes.Buffer

	cache := NewCache([]string{"nonexistent-command-xyz"})
	proxy := NewProxy([]string{"nonexistent-command-xyz"}, cache, stdin, &stdout)
	err := proxy.Run()
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	if stdout.Len() != 0 {
		t.Fatalf("expected no output for notification error, got: %s", stdout.String())
	}
}

func TestInProcess_InvalidMessagesSkipped(t *testing.T) {
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()

	messages := []string{
		"not valid json",
		"[1,2,3]",
		"",
		request(1, "initialize", initParams),
		notification("notifications/initialized"),
		request(2, "tools/list"),
		request(3, "tools/call", `{"name":"echo","arguments":{"text":"ok"}}`),
	}

	responses, err := runProxyInProcess(t, serverBin, cacheDir, messages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(responses) < 3 {
		t.Fatalf("got %d responses, want at least 3", len(responses))
	}

	result := responses[2]["result"].(map[string]any)
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if text != "echo: ok" {
		t.Fatalf("got %q, want %q", text, "echo: ok")
	}
}

func TestInProcess_NoCacheOnlyDiscovery(t *testing.T) {
	// When only discovery requests come in no-cache mode, cache should still be saved
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()

	messages := []string{
		request(1, "initialize", initParams),
		notification("notifications/initialized"),
		request(2, "tools/list"),
	}

	responses, err := runProxyInProcess(t, serverBin, cacheDir, messages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(responses) != 2 {
		t.Fatalf("got %d responses, want 2", len(responses))
	}

	// Verify cache was saved
	files, _ := filepath.Glob(filepath.Join(cacheDir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("got %d cache files, want 1", len(files))
	}
}

func TestInProcess_CachedMissingMethodGoesLive(t *testing.T) {
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()

	// Manually create a partial cache (only initialize, no tools/list)
	t.Setenv("LAZY_MCP_CACHE_DIR", cacheDir)
	cache := NewCache([]string{serverBin})
	partialCache := map[string]json.RawMessage{
		"initialize": json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"mock-server","version":"0.1.0"}}`),
	}
	if err := cache.Save(partialCache); err != nil {
		t.Fatal(err)
	}

	// Request tools/list which is not in cache - should trigger goLive
	messages := []string{
		request(1, "initialize", initParams),
		notification("notifications/initialized"),
		request(2, "tools/list"),
	}

	responses, err := runProxyInProcess(t, serverBin, cacheDir, messages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(responses) < 2 {
		t.Fatalf("got %d responses, want at least 2", len(responses))
	}

	// The first response is from cache (initialize)
	if fmt.Sprintf("%v", responses[0]["id"]) != "1" {
		t.Fatalf("first response id: got %v, want 1", responses[0]["id"])
	}

	// tools/list should come from the live server via goLive
	found := false
	for _, resp := range responses {
		if fmt.Sprintf("%v", resp["id"]) == "2" {
			result := resp["result"].(map[string]any)
			tools := result["tools"].([]any)
			if len(tools) != 1 {
				t.Fatalf("got %d tools, want 1", len(tools))
			}
			found = true
		}
	}
	if !found {
		t.Fatal("tools/list response not found")
	}
}

func TestInProcess_GoLiveWithBadCommand(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("LAZY_MCP_CACHE_DIR", cacheDir)

	// Create cache so we enter cached path
	cache := NewCache([]string{"nonexistent-command-xyz"})
	partialCache := map[string]json.RawMessage{
		"initialize": json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"mock","version":"0.1.0"}}`),
	}
	if err := cache.Save(partialCache); err != nil {
		t.Fatal(err)
	}

	// Send non-discovery request to trigger goLive with bad command
	messages := []string{
		request(1, "initialize", initParams),
		notification("notifications/initialized"),
		request(2, "tools/call", `{"name":"echo","arguments":{"text":"x"}}`),
	}

	input := strings.Join(messages, "\n") + "\n"
	stdin := strings.NewReader(input)
	var stdout bytes.Buffer

	cache = NewCache([]string{"nonexistent-command-xyz"})
	proxy := NewProxy([]string{"nonexistent-command-xyz"}, cache, stdin, &stdout)
	err := proxy.Run()
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	responses := parseResponses(t, stdout.Bytes())
	// Should have: cached initialize response + error for tools/call
	hasInit := false
	hasError := false
	for _, resp := range responses {
		if fmt.Sprintf("%v", resp["id"]) == "1" && resp["result"] != nil {
			hasInit = true
		}
		if fmt.Sprintf("%v", resp["id"]) == "2" && resp["error"] != nil {
			hasError = true
		}
	}
	if !hasInit {
		t.Fatal("expected cached initialize response")
	}
	if !hasError {
		t.Fatal("expected error response for tools/call")
	}
}

func TestInProcess_GoLiveWithNotification(t *testing.T) {
	// goLive triggered by a notification (no ID) - should not write error on bad command
	cacheDir := t.TempDir()
	t.Setenv("LAZY_MCP_CACHE_DIR", cacheDir)

	cache := NewCache([]string{"nonexistent-command-xyz"})
	partialCache := map[string]json.RawMessage{
		"initialize":   json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"mock","version":"0.1.0"}}`),
		"tools/list":   json.RawMessage(`{"tools":[]}`),
		"prompts/list": json.RawMessage(`{"prompts":[]}`),
	}
	if err := cache.Save(partialCache); err != nil {
		t.Fatal(err)
	}

	// Trigger goLive with a non-discovery notification (no ID)
	input := strings.Join([]string{
		request(1, "initialize", initParams),
		notification("notifications/initialized"),
		notification("some/notification"),
	}, "\n") + "\n"
	stdin := strings.NewReader(input)
	var stdout bytes.Buffer

	cache = NewCache([]string{"nonexistent-command-xyz"})
	proxy := NewProxy([]string{"nonexistent-command-xyz"}, cache, stdin, &stdout)
	err := proxy.Run()
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	responses := parseResponses(t, stdout.Bytes())
	// Should only have cached initialize response, no error for notification
	if len(responses) != 1 {
		t.Fatalf("got %d responses, want 1 (only cached initialize)", len(responses))
	}
}

func TestInProcess_ResourcesAndPrompts(t *testing.T) {
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()

	// First run: request all discovery methods
	messages := []string{
		request(1, "initialize", initParams),
		notification("notifications/initialized"),
		request(2, "tools/list"),
		request(3, "resources/list"),
		request(4, "resources/templates/list"),
		request(5, "prompts/list"),
		request(6, "tools/call", `{"name":"echo","arguments":{"text":"done"}}`),
	}

	responses, err := runProxyInProcess(t, serverBin, cacheDir, messages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(responses) < 5 {
		t.Fatalf("got %d responses, want at least 5", len(responses))
	}

	// Verify cache contains all discovery methods
	t.Setenv("LAZY_MCP_CACHE_DIR", cacheDir)
	cache := NewCache([]string{serverBin})
	cached := cache.Load()
	if cached == nil {
		t.Fatal("cache should not be nil")
	}

	expectedKeys := []string{"initialize", "tools/list"}
	for _, key := range expectedKeys {
		if _, ok := cached[key]; !ok {
			t.Fatalf("cache missing key %q", key)
		}
	}
}

func TestInProcess_CachedAllDiscoveryMethods(t *testing.T) {
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()

	// Build a full cache
	messages := []string{
		request(1, "initialize", initParams),
		notification("notifications/initialized"),
		request(2, "tools/list"),
		request(3, "resources/list"),
		request(4, "prompts/list"),
		request(5, "tools/call", `{"name":"echo","arguments":{"text":"x"}}`),
	}
	_, err := runProxyInProcess(t, serverBin, cacheDir, messages)
	if err != nil {
		t.Fatalf("first run error: %v", err)
	}

	// Second run: all discovery from cache
	messages = []string{
		request(10, "initialize", initParams),
		notification("notifications/initialized"),
		request(11, "tools/list"),
		request(12, "resources/list"),
		request(13, "prompts/list"),
	}
	responses, err := runProxyInProcess(t, serverBin, cacheDir, messages)
	if err != nil {
		t.Fatalf("second run error: %v", err)
	}

	if len(responses) != 4 {
		t.Fatalf("got %d responses, want 4", len(responses))
	}

	// All should have correct IDs
	expectedIDs := []string{"10", "11", "12", "13"}
	for i, eid := range expectedIDs {
		if fmt.Sprintf("%v", responses[i]["id"]) != eid {
			t.Fatalf("response %d: got id %v, want %s", i, responses[i]["id"], eid)
		}
	}
}

func TestInProcess_NonDiscoveryTriggersCacheSaveAndBidirectional(t *testing.T) {
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()

	// Send initialize + tools/call (skip tools/list) - tools/call should trigger cache save
	messages := []string{
		request(1, "initialize", initParams),
		notification("notifications/initialized"),
		request(2, "tools/call", `{"name":"echo","arguments":{"text":"direct"}}`),
	}

	responses, err := runProxyInProcess(t, serverBin, cacheDir, messages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should get initialize response + tools/call response
	if len(responses) < 2 {
		t.Fatalf("got %d responses, want at least 2", len(responses))
	}

	// Verify cache was saved with just initialize
	t.Setenv("LAZY_MCP_CACHE_DIR", cacheDir)
	cache := NewCache([]string{serverBin})
	cached := cache.Load()
	if cached == nil {
		t.Fatal("cache should have been saved")
	}
	if _, ok := cached["initialize"]; !ok {
		t.Fatal("cache should contain initialize")
	}
}

func TestInProcess_GoLiveReInitializes(t *testing.T) {
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()

	// Build cache from first run
	messages := []string{
		request(1, "initialize", initParams),
		notification("notifications/initialized"),
		request(2, "tools/list"),
		request(3, "tools/call", `{"name":"echo","arguments":{"text":"x"}}`),
	}
	_, err := runProxyInProcess(t, serverBin, cacheDir, messages)
	if err != nil {
		t.Fatalf("first run error: %v", err)
	}

	// Second run: go live - should re-initialize the server
	messages = []string{
		request(10, "initialize", initParams),
		notification("notifications/initialized"),
		request(11, "tools/call", `{"name":"echo","arguments":{"text":"re-init"}}`),
	}
	responses, err := runProxyInProcess(t, serverBin, cacheDir, messages)
	if err != nil {
		t.Fatalf("second run error: %v", err)
	}

	// Find the tools/call response
	found := false
	for _, resp := range responses {
		if resp["result"] != nil {
			result := resp["result"].(map[string]any)
			if content, ok := result["content"]; ok {
				items := content.([]any)
				text := items[0].(map[string]any)["text"].(string)
				if text == "echo: re-init" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("expected tools/call response with 'echo: re-init'")
	}
}

func buildNoisyServer(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "noisy_server")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	_, thisFile, _, _ := runtime.Caller(0)
	src := filepath.Join(filepath.Dir(thisFile), "testdata", "noisy_server.go")
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to build noisy server: %v", err)
	}
	return bin
}

func TestInProcess_ServerSendsUnsolicitedNotifications(t *testing.T) {
	serverBin := buildNoisyServer(t)
	cacheDir := t.TempDir()

	messages := []string{
		request(1, "initialize", initParams),
		notification("notifications/initialized"),
		request(2, "tools/list"),
		request(3, "tools/call", `{"name":"echo","arguments":{"text":"noisy"}}`),
	}

	responses, err := runProxyInProcess(t, serverBin, cacheDir, messages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have: notification + initialize response + notification + tools/list response + tools/call response
	// The unsolicited notifications should be forwarded to stdout
	hasInitResponse := false
	hasToolsListResponse := false
	hasToolsCallResponse := false
	hasNotification := false

	for _, resp := range responses {
		if resp["method"] != nil {
			hasNotification = true
			continue
		}
		id := fmt.Sprintf("%v", resp["id"])
		switch id {
		case "1":
			hasInitResponse = true
		case "2":
			hasToolsListResponse = true
		case "3":
			hasToolsCallResponse = true
			result := resp["result"].(map[string]any)
			content := result["content"].([]any)
			text := content[0].(map[string]any)["text"].(string)
			if text != "echo: noisy" {
				t.Fatalf("got %q, want %q", text, "echo: noisy")
			}
		}
	}

	if !hasInitResponse {
		t.Fatal("missing initialize response")
	}
	if !hasToolsListResponse {
		t.Fatal("missing tools/list response")
	}
	if !hasToolsCallResponse {
		t.Fatal("missing tools/call response")
	}
	if !hasNotification {
		t.Fatal("expected forwarded unsolicited notifications")
	}
}

func TestInProcess_GoLiveWithNoisyServer(t *testing.T) {
	noisyBin := buildNoisyServer(t)
	cacheDir := t.TempDir()

	// First run: build cache with noisy server
	messages := []string{
		request(1, "initialize", initParams),
		notification("notifications/initialized"),
		request(2, "tools/list"),
		request(3, "tools/call", `{"name":"echo","arguments":{"text":"x"}}`),
	}
	_, err := runProxyInProcess(t, noisyBin, cacheDir, messages)
	if err != nil {
		t.Fatalf("first run error: %v", err)
	}

	// Second run: goLive re-initialization with noisy server (sends notifications during re-init)
	messages = []string{
		request(10, "initialize", initParams),
		notification("notifications/initialized"),
		request(11, "tools/call", `{"name":"echo","arguments":{"text":"re-init-noisy"}}`),
	}
	responses, err := runProxyInProcess(t, noisyBin, cacheDir, messages)
	if err != nil {
		t.Fatalf("second run error: %v", err)
	}

	// Should include the tools/call response
	found := false
	for _, resp := range responses {
		if resp["result"] != nil {
			result := resp["result"].(map[string]any)
			if content, ok := result["content"]; ok {
				items := content.([]any)
				text := items[0].(map[string]any)["text"].(string)
				if text == "echo: re-init-noisy" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("expected tools/call response with 'echo: re-init-noisy'")
	}
}

func TestInProcess_EagerCacheSave(t *testing.T) {
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()
	t.Setenv("LAZY_MCP_CACHE_DIR", cacheDir)

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	cache := NewCache([]string{serverBin})
	proxy := NewProxy([]string{serverBin}, cache, stdinR, stdoutW)

	done := make(chan error, 1)
	go func() {
		done <- proxy.Run()
		stdoutW.Close()
	}()

	stdoutScanner := bufio.NewScanner(stdoutR)

	// Send initialize and read its response.
	fmt.Fprintln(stdinW, request(1, "initialize", initParams))
	if !stdoutScanner.Scan() {
		t.Fatal("expected initialize response")
	}

	// Writing to the pipe synchronizes with the proxy's scanner.Scan(),
	// which happens after saveCache. So by the time this write returns,
	// initialize's cache save is guaranteed complete.
	fmt.Fprintln(stdinW, notification("notifications/initialized"))

	// No concurrent writes to the cache file at this point —
	// the proxy is processing notifications/initialized which doesn't save.
	cached := cache.Load()
	if cached == nil {
		t.Fatal("cache should have been saved eagerly after initialize")
	}
	if _, ok := cached["initialize"]; !ok {
		t.Fatal("cache should contain initialize")
	}

	// Send tools/list and let the proxy finish.
	fmt.Fprintln(stdinW, request(2, "tools/list"))
	if !stdoutScanner.Scan() {
		t.Fatal("expected tools/list response")
	}
	stdinW.Close()
	<-done

	// After proxy exits, tools/list save is guaranteed complete.
	cached = cache.Load()
	if _, ok := cached["tools/list"]; !ok {
		t.Fatal("cache should contain tools/list after eager save")
	}
}

func TestInProcess_InvalidMessagesSkippedInCachedMode(t *testing.T) {
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()

	// Build cache
	messages := []string{
		request(1, "initialize", initParams),
		notification("notifications/initialized"),
		request(2, "tools/list"),
		request(3, "tools/call", `{"name":"echo","arguments":{"text":"x"}}`),
	}
	_, err := runProxyInProcess(t, serverBin, cacheDir, messages)
	if err != nil {
		t.Fatalf("first run error: %v", err)
	}

	// Second run with invalid messages interspersed
	messages = []string{
		"garbage",
		request(10, "initialize", initParams),
		"[not,an,object]",
		notification("notifications/initialized"),
		"",
		request(11, "tools/list"),
	}
	responses, err := runProxyInProcess(t, serverBin, cacheDir, messages)
	if err != nil {
		t.Fatalf("second run error: %v", err)
	}

	if len(responses) != 2 {
		t.Fatalf("got %d responses, want 2", len(responses))
	}
}

const discoverParams = `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}`

func discoverProbeSession(idBase int) []string {
	return []string{
		request(fmt.Sprintf("probe-%d", idBase), "server/discover", discoverParams),
		request(idBase, "initialize", initParams),
		notification("notifications/initialized"),
		request(idBase+1, "tools/list"),
		request(idBase+2, "prompts/list"),
		request(idBase+3, "resources/list"),
	}
}

func TestInProcess_DiscoverProbeServedFromCache(t *testing.T) {
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()

	if _, err := runProxyInProcess(t, serverBin, cacheDir, discoverProbeSession(1)); err != nil {
		t.Fatalf("first run error: %v", err)
	}

	cached := NewCache([]string{serverBin}).Load()
	for _, key := range []string{"server/discover", "initialize", "tools/list", "prompts/list", "resources/list"} {
		if _, ok := cached[key]; !ok {
			t.Fatalf("cache should contain %s, got %v", key, cached)
		}
	}

	if err := os.Remove(serverBin); err != nil {
		t.Fatal(err)
	}

	responses, err := runProxyInProcess(t, serverBin, cacheDir, discoverProbeSession(10))
	if err != nil {
		t.Fatalf("second run error: %v", err)
	}
	if len(responses) != 5 {
		t.Fatalf("got %d responses, want 5: %v", len(responses), responses)
	}

	probe := responses[0]
	if probe["id"] != "probe-10" {
		t.Fatalf("probe response id: got %v, want probe-10", probe["id"])
	}
	probeErr, ok := probe["error"].(map[string]any)
	if !ok || probeErr["code"] != float64(-32601) {
		t.Fatalf("probe response should replay the cached -32601 error, got %v", probe)
	}
	if _, ok := probe["result"]; ok {
		t.Fatalf("probe response should not have a result, got %v", probe)
	}

	for i, resp := range responses[1:] {
		wantID := fmt.Sprintf("%d", 10+i)
		if fmt.Sprintf("%v", resp["id"]) != wantID {
			t.Fatalf("response %d id: got %v, want %s", i+1, resp["id"], wantID)
		}
		if resp["result"] == nil {
			t.Fatalf("response %d should come from cache, got %v", i+1, resp)
		}
	}
}

func TestInProcess_GoLiveReplaysDiscoverProbe(t *testing.T) {
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()

	// Cache built by a client that doesn't send the probe.
	messages := []string{
		request(1, "initialize", initParams),
		notification("notifications/initialized"),
		request(2, "tools/list"),
	}
	if _, err := runProxyInProcess(t, serverBin, cacheDir, messages); err != nil {
		t.Fatalf("first run error: %v", err)
	}

	messages = append(discoverProbeSession(10),
		request(5, "tools/call", `{"name":"echo","arguments":{"text":"live"}}`))
	responses, err := runProxyInProcess(t, serverBin, cacheDir, messages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, resp := range responses {
		if fmt.Sprintf("%v", resp["id"]) == "5" {
			text := resp["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]
			if text != "echo: live" {
				t.Fatalf("got %q, want %q", text, "echo: live")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("tools/call response not found")
	}

	cached := NewCache([]string{serverBin}).Load()
	for _, key := range []string{"server/discover", "initialize", "tools/list"} {
		if _, ok := cached[key]; !ok {
			t.Fatalf("cache should contain %s after going live, got %v", key, cached)
		}
	}
}

func TestInProcess_GoLiveWithoutInitializeKeepsCache(t *testing.T) {
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()

	if _, err := runProxyInProcess(t, serverBin, cacheDir, discoverProbeSession(1)); err != nil {
		t.Fatalf("first run error: %v", err)
	}

	messages := []string{
		request(5, "tools/call", `{"name":"echo","arguments":{"text":"no-init"}}`),
	}
	if _, err := runProxyInProcess(t, serverBin, cacheDir, messages); err != nil {
		t.Fatalf("second run error: %v", err)
	}

	cached := NewCache([]string{serverBin}).Load()
	for _, key := range []string{"server/discover", "initialize", "tools/list"} {
		if _, ok := cached[key]; !ok {
			t.Fatalf("cache should still contain %s, got %v", key, cached)
		}
	}
}

func TestInProcess_NoCacheNonDiscoveryFirstSavesNothing(t *testing.T) {
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()

	messages := []string{
		request(1, "tools/call", `{"name":"echo","arguments":{"text":"first"}}`),
	}
	if _, err := runProxyInProcess(t, serverBin, cacheDir, messages); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	files, _ := filepath.Glob(filepath.Join(cacheDir, "*.json"))
	if len(files) != 0 {
		t.Fatalf("got %d cache files, want 0", len(files))
	}
}

func TestInProcess_GoLiveWithoutProbeKeepsCachedProbe(t *testing.T) {
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()

	if _, err := runProxyInProcess(t, serverBin, cacheDir, discoverProbeSession(1)); err != nil {
		t.Fatalf("first run error: %v", err)
	}

	messages := []string{
		request(10, "initialize", initParams),
		notification("notifications/initialized"),
		request(11, "tools/call", `{"name":"echo","arguments":{"text":"no-probe"}}`),
	}
	if _, err := runProxyInProcess(t, serverBin, cacheDir, messages); err != nil {
		t.Fatalf("second run error: %v", err)
	}

	cached := NewCache([]string{serverBin}).Load()
	if _, ok := cached["server/discover"]; !ok {
		t.Fatalf("cache should still contain server/discover, got %v", cached)
	}
}

func TestInProcess_GoLiveOnProbeSendsItOnce(t *testing.T) {
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()

	messages := []string{
		request(1, "initialize", initParams),
		notification("notifications/initialized"),
		request(2, "tools/list"),
	}
	if _, err := runProxyInProcess(t, serverBin, cacheDir, messages); err != nil {
		t.Fatalf("first run error: %v", err)
	}

	responses, err := runProxyInProcess(t, serverBin, cacheDir, discoverProbeSession(10))
	if err != nil {
		t.Fatalf("second run error: %v", err)
	}

	var probe map[string]any
	for _, resp := range responses {
		if resp["id"] == "probe-10" {
			probe = resp
		}
	}
	if probe == nil {
		t.Fatalf("probe response not found in %v", responses)
	}
	data := probe["error"].(map[string]any)["data"].(map[string]any)
	if data["discoverCalls"] != float64(1) {
		t.Fatalf("server should see server/discover once, got %v calls", data["discoverCalls"])
	}
}

func TestInProcess_ProbeWithoutIDInCachedMode(t *testing.T) {
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()

	if _, err := runProxyInProcess(t, serverBin, cacheDir, discoverProbeSession(1)); err != nil {
		t.Fatalf("first run error: %v", err)
	}

	messages := []string{
		notification("server/discover"),
		request(10, "initialize", initParams),
		request(11, "tools/list"),
	}
	responses, err := runProxyInProcess(t, serverBin, cacheDir, messages)
	if err != nil {
		t.Fatalf("second run error: %v", err)
	}
	if len(responses) != 2 {
		t.Fatalf("got %d responses, want 2: %v", len(responses), responses)
	}
}

func TestInProcess_ProbeWithoutIDInNoCacheMode(t *testing.T) {
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()

	messages := []string{
		notification("server/discover"),
		request(10, "initialize", initParams),
		request(11, "tools/list"),
	}
	responses, err := runProxyInProcess(t, serverBin, cacheDir, messages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(responses) != 2 {
		t.Fatalf("got %d responses, want 2: %v", len(responses), responses)
	}
}

func TestInProcess_EmptyCachedProbeGoesLive(t *testing.T) {
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()

	if _, err := runProxyInProcess(t, serverBin, cacheDir, discoverProbeSession(1)); err != nil {
		t.Fatalf("first run error: %v", err)
	}
	cache := NewCache([]string{serverBin})
	cached := cache.Load()
	cached["server/discover"] = json.RawMessage(`{}`)
	if err := cache.Save(cached); err != nil {
		t.Fatal(err)
	}

	responses, err := runProxyInProcess(t, serverBin, cacheDir, discoverProbeSession(10))
	if err != nil {
		t.Fatalf("second run error: %v", err)
	}
	if responses[0]["id"] != "probe-10" || responses[0]["error"] == nil {
		t.Fatalf("probe should get the live server's error, got %v", responses[0])
	}
}

func TestInProcess_GoLiveOnProbeWithoutParamsCachesIt(t *testing.T) {
	serverBin := buildMockServer(t)
	cacheDir := t.TempDir()

	messages := []string{
		request(1, "initialize", initParams),
		notification("notifications/initialized"),
		request(2, "tools/list"),
	}
	if _, err := runProxyInProcess(t, serverBin, cacheDir, messages); err != nil {
		t.Fatalf("first run error: %v", err)
	}

	messages = []string{
		request("probe", "server/discover"),
		request(10, "initialize", initParams),
	}
	if _, err := runProxyInProcess(t, serverBin, cacheDir, messages); err != nil {
		t.Fatalf("second run error: %v", err)
	}

	cached := NewCache([]string{serverBin}).Load()
	if _, ok := cached["server/discover"]; !ok {
		t.Fatalf("cache should contain server/discover, got %v", cached)
	}
}
