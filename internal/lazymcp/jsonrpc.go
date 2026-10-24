package lazymcp

import (
	"encoding/json"
	"errors"
	"fmt"
)

type Message struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  *json.RawMessage `json:"params,omitempty"`
	Result  *json.RawMessage `json:"result,omitempty"`
	Error   *RPCError        `json:"error,omitempty"`
}

type RPCError struct {
	Code    int              `json:"code"`
	Message string           `json:"message"`
	Data    *json.RawMessage `json:"data,omitempty"`
}

// DiscoverMethod is the probe a client sends before initialize. Servers that
// don't support it answer with an error, so its whole response is cached.
const DiscoverMethod = "server/discover"

type responseBody struct {
	Result *json.RawMessage `json:"result,omitempty"`
	Error  *RPCError        `json:"error,omitempty"`
}

var discoveryMethods = map[string]bool{
	DiscoverMethod:              true,
	"initialize":                true,
	"notifications/initialized": true,
	"tools/list":                true,
	"resources/list":            true,
	"resources/templates/list":  true,
	"prompts/list":              true,
}

var listMethodToCapability = map[string]string{
	"tools/list":               "tools",
	"resources/list":           "resources",
	"resources/templates/list": "resources",
	"prompts/list":             "prompts",
}

func ParseMessage(data []byte) (*Message, error) {
	if len(data) == 0 {
		return nil, errors.New("empty message")
	}
	for _, b := range data {
		if b == ' ' || b == '\t' || b == '\n' || b == '\r' {
			continue
		}
		if b != '{' {
			return nil, errors.New("message must be a JSON object")
		}
		break
	}
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func SerializeMessage(msg *Message) []byte {
	data, _ := json.Marshal(msg)
	return append(data, '\n')
}

func MakeResponse(id any, result json.RawMessage) []byte {
	idBytes, _ := json.Marshal(id)
	rawID := json.RawMessage(idBytes)
	msg := Message{
		JSONRPC: "2.0",
		ID:      &rawID,
		Result:  &result,
	}
	return SerializeMessage(&msg)
}

// ResponseBody returns the result or error of resp without the envelope.
func ResponseBody(resp *Message) json.RawMessage {
	data, _ := json.Marshal(responseBody{Result: resp.Result, Error: resp.Error})
	return data
}

// MakeResponseFromBody builds a response from a body returned by ResponseBody.
func MakeResponseFromBody(id any, body json.RawMessage) []byte {
	var b responseBody
	json.Unmarshal(body, &b)
	idBytes, _ := json.Marshal(id)
	rawID := json.RawMessage(idBytes)
	msg := Message{
		JSONRPC: "2.0",
		ID:      &rawID,
		Result:  b.Result,
		Error:   b.Error,
	}
	return SerializeMessage(&msg)
}

func MakeErrorResponse(id any, code int, message string) []byte {
	idBytes, _ := json.Marshal(id)
	rawID := json.RawMessage(idBytes)
	msg := Message{
		JSONRPC: "2.0",
		ID:      &rawID,
		Error:   &RPCError{Code: code, Message: message},
	}
	return SerializeMessage(&msg)
}

func IsDiscoveryMethod(method string) bool {
	return discoveryMethods[method]
}

func ListMethodToCapability() map[string]string {
	return listMethodToCapability
}

func MessageID(msg *Message) string {
	if msg.ID == nil {
		return ""
	}
	return string(*msg.ID)
}

func MakeRequest(id string, method string, params *json.RawMessage) *Message {
	rawID := json.RawMessage(fmt.Sprintf("%q", id))
	msg := &Message{
		JSONRPC: "2.0",
		ID:      &rawID,
		Method:  method,
	}
	if params != nil {
		msg.Params = params
	}
	return msg
}
