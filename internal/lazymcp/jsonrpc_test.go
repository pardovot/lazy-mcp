package lazymcp

import (
	"encoding/json"
	"testing"
)

func TestParseMessage(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		msg, err := ParseMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if msg.Method != "initialize" {
			t.Fatalf("got method %q, want %q", msg.Method, "initialize")
		}
	})

	t.Run("empty", func(t *testing.T) {
		_, err := ParseMessage([]byte(""))
		if err == nil {
			t.Fatal("expected error for empty input")
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		_, err := ParseMessage([]byte("not json"))
		if err == nil {
			t.Fatal("expected error for invalid json")
		}
	})

	t.Run("non-object", func(t *testing.T) {
		_, err := ParseMessage([]byte(`[1,2,3]`))
		if err == nil {
			t.Fatal("expected error for non-object json")
		}
	})
}

func TestMakeResponse(t *testing.T) {
	result := json.RawMessage(`{"tools":[]}`)
	data := MakeResponse(1, result)
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.ID == nil {
		t.Fatal("response must have id")
	}
	if msg.Result == nil {
		t.Fatal("response must have result")
	}
}

func TestMakeErrorResponse(t *testing.T) {
	data := MakeErrorResponse(1, -32603, "server error")
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Error == nil {
		t.Fatal("error response must have error field")
	}
	if msg.Error.Code != -32603 {
		t.Fatalf("got code %d, want -32603", msg.Error.Code)
	}
}

func TestIsDiscoveryMethod(t *testing.T) {
	discovery := []string{
		"initialize",
		"notifications/initialized",
		"tools/list",
		"resources/list",
		"resources/templates/list",
		"prompts/list",
	}
	for _, m := range discovery {
		if !IsDiscoveryMethod(m) {
			t.Errorf("%q should be discovery method", m)
		}
	}

	nonDiscovery := []string{"tools/call", "resources/read", "prompts/get", "ping"}
	for _, m := range nonDiscovery {
		if IsDiscoveryMethod(m) {
			t.Errorf("%q should NOT be discovery method", m)
		}
	}
}

func TestSerializeMessage(t *testing.T) {
	msg := Message{JSONRPC: "2.0", Method: "test"}
	data := SerializeMessage(&msg)
	if data[len(data)-1] != '\n' {
		t.Fatal("serialized message must end with newline")
	}
	newlines := 0
	for _, b := range data {
		if b == '\n' {
			newlines++
		}
	}
	if newlines != 1 {
		t.Fatalf("got %d newlines, want 1", newlines)
	}
}

func TestListMethodToCapability(t *testing.T) {
	m := ListMethodToCapability()

	expected := map[string]string{
		"tools/list":               "tools",
		"resources/list":           "resources",
		"resources/templates/list": "resources",
		"prompts/list":             "prompts",
	}
	if len(m) != len(expected) {
		t.Fatalf("got %d entries, want %d", len(m), len(expected))
	}
	for method, cap := range expected {
		if m[method] != cap {
			t.Errorf("%q: got %q, want %q", method, m[method], cap)
		}
	}
}

func TestMessageID(t *testing.T) {
	t.Run("nil id", func(t *testing.T) {
		msg := &Message{JSONRPC: "2.0", Method: "test"}
		if got := MessageID(msg); got != "" {
			t.Fatalf("got %q, want empty string", got)
		}
	})

	t.Run("numeric id", func(t *testing.T) {
		id := json.RawMessage(`1`)
		msg := &Message{JSONRPC: "2.0", ID: &id}
		if got := MessageID(msg); got != "1" {
			t.Fatalf("got %q, want %q", got, "1")
		}
	})

	t.Run("string id", func(t *testing.T) {
		id := json.RawMessage(`"abc"`)
		msg := &Message{JSONRPC: "2.0", ID: &id}
		if got := MessageID(msg); got != `"abc"` {
			t.Fatalf("got %q, want %q", got, `"abc"`)
		}
	})
}

func TestMakeRequest(t *testing.T) {
	t.Run("without params", func(t *testing.T) {
		msg := MakeRequest("req-1", "tools/list", nil)
		if msg.JSONRPC != "2.0" {
			t.Fatalf("got jsonrpc %q, want %q", msg.JSONRPC, "2.0")
		}
		if msg.Method != "tools/list" {
			t.Fatalf("got method %q, want %q", msg.Method, "tools/list")
		}
		if msg.ID == nil {
			t.Fatal("id must not be nil")
		}
		if string(*msg.ID) != `"req-1"` {
			t.Fatalf("got id %s, want %q", *msg.ID, `"req-1"`)
		}
		if msg.Params != nil {
			t.Fatalf("params should be nil, got %s", *msg.Params)
		}
	})

	t.Run("with params", func(t *testing.T) {
		params := json.RawMessage(`{"key":"value"}`)
		msg := MakeRequest("req-2", "initialize", &params)
		if msg.Params == nil {
			t.Fatal("params must not be nil")
		}
		if string(*msg.Params) != `{"key":"value"}` {
			t.Fatalf("got params %s, want %s", *msg.Params, `{"key":"value"}`)
		}
	})

	t.Run("roundtrip serialization", func(t *testing.T) {
		msg := MakeRequest("req-3", "tools/list", nil)
		data := SerializeMessage(msg)
		parsed, err := ParseMessage(data[:len(data)-1]) // strip newline
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Method != "tools/list" {
			t.Fatalf("got method %q after roundtrip", parsed.Method)
		}
	})
}

func TestParseMessageWhitespace(t *testing.T) {
	// Whitespace before JSON object should be handled
	msg, err := ParseMessage([]byte(`  {"jsonrpc":"2.0","id":1,"method":"test"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Method != "test" {
		t.Fatalf("got method %q, want %q", msg.Method, "test")
	}
}

func TestMakeResponseFromBody(t *testing.T) {
	data, ok := MakeResponseFromBody("x", json.RawMessage(`{"error":{"code":-32601,"message":"nope","data":{"a":1}}}`))
	if !ok {
		t.Fatal("body with error should be usable")
	}
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatal(err)
	}
	if string(*msg.ID) != `"x"` || msg.Error == nil || msg.Error.Code != -32601 || string(*msg.Error.Data) != `{"a":1}` {
		t.Fatalf("unexpected response %s", data)
	}

	for _, body := range []string{`{}`, `garbage`, `null`} {
		if _, ok := MakeResponseFromBody("x", json.RawMessage(body)); ok {
			t.Fatalf("body %s should not be usable", body)
		}
	}
}
