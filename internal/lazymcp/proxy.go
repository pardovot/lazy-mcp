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

// Scanner buffers start small and grow up to maxMessageSize, so an idle proxy stays light.
const initialBufferSize = 64 << 10 // 64KB

type Proxy struct {
	command          []string
	cache            *Cache
	cachedData       map[string]json.RawMessage
	discoverRequest  *Message
	listenRequest    *Message
	initializeParams *json.RawMessage
	listParams       map[string]*json.RawMessage
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
		listParams: make(map[string]*json.RawMessage),
		stdin:      stdin,
		stdout:     stdout,
	}
}

func (p *Proxy) Run() error {
	scanner := bufio.NewScanner(p.stdin)
	scanner.Buffer(make([]byte, initialBufferSize), maxMessageSize)

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

		case msg.Method == ListenMethod && msg.ID != nil:
			p.listenRequest = msg
			if ack, ok := MakeListenAck(json.RawMessage(*msg.ID), p.cachedData[ListenMethod]); ok {
				p.writeStdout(ack)
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
			p.listParams[msg.Method] = msg.Params
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

		case msg.Method == ListenMethod && msg.ID != nil:
			p.listenRequest = msg
			p.writeServer(SerializeMessage(msg))
			ack, err := p.readListenAck(string(*msg.ID), true)
			if err != nil {
				return err
			}
			if ack != nil {
				newCache[ListenMethod] = *ack
			}
			p.saveCache(newCache)

		case msg.Method == "notifications/initialized":
			p.writeServer(SerializeMessage(msg))

		case IsDiscoveryMethod(msg.Method):
			p.listParams[msg.Method] = msg.Params
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

	// A stateless server has no initialize, so its capabilities come from server/discover.
	var capabilities map[string]json.RawMessage
	stateless := false

	if p.discoverRequest != nil {
		discoverReq := MakeRequest("lazy-mcp-discover", DiscoverMethod, p.discoverRequest.Params)
		p.writeServer(SerializeMessage(discoverReq))
		discoverResp, err := p.readResponse(`"lazy-mcp-discover"`)
		if err != nil {
			return err
		}
		newCache[DiscoverMethod] = ResponseBody(discoverResp)
		if p.initializeParams == nil && discoverResp.Result != nil {
			stateless = true
			capabilities = ParseCapabilities(discoverResp.Result)
		}
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

		capabilities = ParseCapabilities(initResp.Result)
	}

	for method, capKey := range ListMethodToCapability() {
		if _, ok := capabilities[capKey]; !ok {
			continue
		}
		// Stateless requests must carry the protocol version in _meta, as the client's own requests do.
		params := p.listParams[method]
		if params == nil && stateless {
			params = p.discoverRequest.Params
		}
		reqID := fmt.Sprintf("lazy-mcp-%s", method)
		req := MakeRequest(reqID, method, params)
		p.writeServer(SerializeMessage(req))
		resp, err := p.readResponse(fmt.Sprintf("%q", reqID))
		if err != nil {
			return err
		}
		if resp.Result != nil {
			newCache[method] = *resp.Result
		}
	}

	// The held listen stream reaches the server before the triggering message. If its ack
	// was already served from cache, the server's ack is consumed instead of sent twice.
	if p.listenRequest != nil {
		p.writeServer(SerializeMessage(p.listenRequest))
		ack, err := p.readListenAck(string(*p.listenRequest.ID), triggeringMsg == p.listenRequest)
		if err != nil {
			return err
		}
		if ack != nil {
			newCache[ListenMethod] = *ack
		}
	}

	p.saveCache(newCache)

	// A triggering listen request was sent above, and a triggering probe is answered from its replay.
	answered := triggeringMsg == p.listenRequest
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
	p.serverOut.Buffer(make([]byte, initialBufferSize), maxMessageSize)

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

// readListenAck forwards server output until the server acknowledges the listen request,
// returning the ack params, or rejects it with a response, returning nil.
func (p *Proxy) readListenAck(requestID string, forwardAck bool) (*json.RawMessage, error) {
	for p.serverOut.Scan() {
		msg, err := ParseMessage(p.serverOut.Bytes())
		if err != nil {
			continue
		}
		isAck := msg.Method == ListenAckMethod && msg.Params != nil
		if forwardAck || !isAck {
			p.writeStdout(p.serverOut.Bytes())
			p.writeStdout([]byte("\n"))
		}
		if isAck {
			return msg.Params, nil
		}
		if msg.ID != nil && string(*msg.ID) == requestID {
			return nil, nil
		}
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
