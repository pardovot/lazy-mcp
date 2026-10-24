package lazymcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"sync"
)

const maxMessageSize = 10 << 20 // 10MB

type Proxy struct {
	command          []string
	cache            *Cache
	cachedData       map[string]json.RawMessage
	discoverRequest  *Message
	initializeParams *json.RawMessage
	cmd              *exec.Cmd
	serverIn         io.WriteCloser
	serverOut        *bufio.Scanner
	stdin            io.Reader
	stdout           io.Writer
}

func NewProxy(command []string, cache *Cache, stdin io.Reader, stdout io.Writer) *Proxy {
	return &Proxy{
		command:    command,
		cache:      cache,
		cachedData: cache.Load(),
		stdin:      stdin,
		stdout:     stdout,
	}
}

func (p *Proxy) Run() error {
	scanner := bufio.NewScanner(p.stdin)
	scanner.Buffer(make([]byte, maxMessageSize), maxMessageSize)

	if p.cachedData == nil {
		return p.runNoCache(scanner)
	}
	return p.runCached(scanner)
}

func (p *Proxy) runCached(scanner *bufio.Scanner) error {
	for scanner.Scan() {
		msg, err := ParseMessage(scanner.Bytes())
		if err != nil {
			continue
		}

		switch {
		case msg.Method == DiscoverMethod && msg.ID == nil:
			// swallow

		case msg.Method == DiscoverMethod:
			p.discoverRequest = msg
			if resp, ok := MakeResponseFromBody(json.RawMessage(*msg.ID), p.cachedData[DiscoverMethod]); ok {
				p.writeStdout(resp)
			} else {
				return p.goLive(msg, scanner)
			}

		case msg.Method == "initialize":
			p.initializeParams = msg.Params
			if result, ok := p.cachedData["initialize"]; ok {
				p.writeStdout(MakeResponse(json.RawMessage(*msg.ID), result))
			} else {
				return p.goLive(msg, scanner)
			}

		case msg.Method == "notifications/initialized":
			// swallow

		case IsDiscoveryMethod(msg.Method):
			if result, ok := p.cachedData[msg.Method]; ok {
				p.writeStdout(MakeResponse(json.RawMessage(*msg.ID), result))
			} else {
				return p.goLive(msg, scanner)
			}

		default:
			return p.goLive(msg, scanner)
		}
	}
	return scanner.Err()
}

func (p *Proxy) runNoCache(scanner *bufio.Scanner) error {
	newCache := make(map[string]json.RawMessage)

	for scanner.Scan() {
		msg, err := ParseMessage(scanner.Bytes())
		if err != nil {
			continue
		}

		if p.cmd == nil {
			if err := p.startServer(); err != nil {
				if msg.ID != nil {
					p.writeStdout(MakeErrorResponse(
						json.RawMessage(*msg.ID), -32603, err.Error()))
				}
				return nil
			}
		}

		switch {
		case msg.Method == DiscoverMethod && msg.ID == nil:
			p.writeServer(SerializeMessage(msg))

		case msg.Method == DiscoverMethod:
			p.discoverRequest = msg
			resp, err := p.forwardRequest(msg)
			if err != nil {
				return err
			}
			newCache[DiscoverMethod] = ResponseBody(resp)
			p.saveCache(newCache)

		case msg.Method == "initialize":
			p.initializeParams = msg.Params
			resp, err := p.forwardRequest(msg)
			if err != nil {
				return err
			}
			if resp.Result != nil {
				newCache["initialize"] = *resp.Result
			}
			p.saveCache(newCache)

		case msg.Method == "notifications/initialized":
			p.writeServer(SerializeMessage(msg))

		case IsDiscoveryMethod(msg.Method):
			resp, err := p.forwardRequest(msg)
			if err != nil {
				return err
			}
			if resp.Result != nil {
				newCache[msg.Method] = *resp.Result
			}
			p.saveCache(newCache)

		default:
			p.saveCache(newCache)
			p.writeServer(SerializeMessage(msg))
			return p.bidirectionalProxy(scanner)
		}
	}

	p.shutdownServer()
	return scanner.Err()
}

func (p *Proxy) goLive(triggeringMsg *Message, scanner *bufio.Scanner) error {
	if err := p.startServer(); err != nil {
		if triggeringMsg.ID != nil {
			p.writeStdout(MakeErrorResponse(
				json.RawMessage(*triggeringMsg.ID), -32603, err.Error()))
		}
		return nil
	}

	// Entries that can't be re-fetched below keep their cached values.
	newCache := maps.Clone(p.cachedData)
	if newCache == nil {
		newCache = make(map[string]json.RawMessage)
	}

	if p.discoverRequest != nil {
		discoverReq := MakeRequest("lazy-mcp-discover", DiscoverMethod, p.discoverRequest.Params)
		p.writeServer(SerializeMessage(discoverReq))
		discoverResp, err := p.readResponse(`"lazy-mcp-discover"`)
		if err != nil {
			return err
		}
		newCache[DiscoverMethod] = ResponseBody(discoverResp)
	}

	if p.initializeParams != nil {
		initReq := MakeRequest("lazy-mcp-init", "initialize", p.initializeParams)
		p.writeServer(SerializeMessage(initReq))
		initResp, err := p.readResponse(`"lazy-mcp-init"`)
		if err != nil {
			return err
		}
		if initResp.Result != nil {
			newCache["initialize"] = *initResp.Result
		}

		notif := &Message{JSONRPC: "2.0", Method: "notifications/initialized"}
		p.writeServer(SerializeMessage(notif))

		var initResult struct {
			Capabilities map[string]json.RawMessage `json:"capabilities"`
		}
		if initResp.Result != nil {
			json.Unmarshal(*initResp.Result, &initResult)
		}

		for method, capKey := range ListMethodToCapability() {
			if _, ok := initResult.Capabilities[capKey]; ok {
				reqID := fmt.Sprintf("lazy-mcp-%s", method)
				req := MakeRequest(reqID, method, nil)
				p.writeServer(SerializeMessage(req))
				resp, err := p.readResponse(fmt.Sprintf("%q", reqID))
				if err != nil {
					return err
				}
				if resp.Result != nil {
					newCache[method] = *resp.Result
				}
			}
		}
	}

	p.saveCache(newCache)

	// A triggering probe is answered from its replay above instead of being sent again.
	answered := false
	if triggeringMsg == p.discoverRequest {
		var resp []byte
		resp, answered = MakeResponseFromBody(json.RawMessage(*triggeringMsg.ID), newCache[DiscoverMethod])
		if answered {
			p.writeStdout(resp)
		}
	}
	if !answered {
		p.writeServer(SerializeMessage(triggeringMsg))
	}

	return p.bidirectionalProxy(scanner)
}

func (p *Proxy) startServer() error {
	p.cmd = exec.Command(p.command[0], p.command[1:]...)
	var err error
	p.serverIn, err = p.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := p.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	p.cmd.Stderr = os.Stderr
	p.serverOut = bufio.NewScanner(stdout)
	p.serverOut.Buffer(make([]byte, maxMessageSize), maxMessageSize)

	if err := p.cmd.Start(); err != nil {
		p.cmd = nil
		return fmt.Errorf("command not found: %s", p.command[0])
	}
	return nil
}

func (p *Proxy) shutdownServer() {
	if p.cmd != nil && p.cmd.Process != nil {
		p.serverIn.Close()
		p.cmd.Wait()
	}
}

func (p *Proxy) forwardRequest(msg *Message) (*Message, error) {
	p.writeServer(SerializeMessage(msg))
	resp, err := p.readResponse(string(*msg.ID))
	if err != nil {
		return nil, err
	}
	p.writeStdout(SerializeMessage(resp))
	return resp, nil
}

func (p *Proxy) readResponse(requestID string) (*Message, error) {
	for p.serverOut.Scan() {
		msg, err := ParseMessage(p.serverOut.Bytes())
		if err != nil {
			continue
		}
		if msg.ID != nil && string(*msg.ID) == requestID {
			return msg, nil
		}
		p.writeStdout(p.serverOut.Bytes())
		p.writeStdout([]byte("\n"))
	}
	if err := p.serverOut.Err(); err != nil {
		return nil, fmt.Errorf("server closed: %w", err)
	}
	return nil, fmt.Errorf("server closed unexpectedly")
}

func (p *Proxy) saveCache(data map[string]json.RawMessage) {
	if len(data) == 0 {
		return
	}
	p.cache.Save(data)
}

func (p *Proxy) writeServer(data []byte) {
	p.serverIn.Write(data)
}

func (p *Proxy) writeStdout(data []byte) {
	p.stdout.Write(data)
}

func (p *Proxy) bidirectionalProxy(scanner *bufio.Scanner) error {
	var wg sync.WaitGroup

	// stdin → server
	wg.Add(1)
	go func() {
		defer wg.Done()
		for scanner.Scan() {
			p.serverIn.Write(scanner.Bytes())
			p.serverIn.Write([]byte("\n"))
		}
		p.serverIn.Close()
	}()

	// server → stdout
	for p.serverOut.Scan() {
		p.stdout.Write(p.serverOut.Bytes())
		p.stdout.Write([]byte("\n"))
	}

	wg.Wait()
	p.cmd.Wait()
	return nil
}
