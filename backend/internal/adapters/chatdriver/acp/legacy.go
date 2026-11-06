package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
)

// ACP 0.13.5 removed the legacy model selector from its generated API while
// deployed agents such as Kimi still advertise `models` and implement
// session/set_model. This narrow wire shim preserves that compatibility without
// downgrading or forking the SDK. Original frames still pass through the SDK;
// local barriers order updates and host receipts so replay backpressure reaches
// the provider socket instead of overflowing the SDK's notification queue.
type legacyACPTransport struct {
	writer *lockedWriteCloser
	output *io.PipeWriter
	nextID atomic.Uint64

	mu                    sync.Mutex
	pending               map[string]chan legacyACPResponse
	models                *legacySessionModelState
	reader                *io.PipeReader
	updateProcessed       <-chan string
	streamDone            <-chan struct{}
	toolCallToRPCID       map[string]string
	latestPermissionRPCID string
	pendingAnswers        map[string]map[string]string
}

type lockedWriteCloser struct {
	mu    sync.Mutex
	inner io.WriteCloser
}

func (w *lockedWriteCloser) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.inner.Write(data)
}

func (w *lockedWriteCloser) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.inner.Close()
}

type legacySessionModelState struct {
	CurrentModelID string            `json:"currentModelId"`
	Available      []legacyModelInfo `json:"availableModels"`
}

type legacyModelInfo struct {
	ModelID     string  `json:"modelId"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

type legacyACPResponse struct {
	err error
}

type legacyACPEnvelope struct {
	JSONRPC string               `json:"jsonrpc"`
	ID      *json.RawMessage     `json:"id,omitempty"`
	Method  string               `json:"method,omitempty"`
	Result  json.RawMessage      `json:"result,omitempty"`
	Error   *acpsdk.RequestError `json:"error,omitempty"`
}

const localNotificationBarrierMethod = "_ao/local_notification_barrier"

func newLegacyACPTransport(stdin io.WriteCloser, updateProcessed <-chan string, streamDone <-chan struct{}) (*legacyACPTransport, io.WriteCloser, io.Reader) {
	writer := &lockedWriteCloser{inner: stdin}
	sdkReader, sdkWriter := io.Pipe()
	transport := &legacyACPTransport{
		writer:          writer,
		reader:          sdkReader,
		output:          sdkWriter,
		pending:         make(map[string]chan legacyACPResponse),
		updateProcessed: updateProcessed,
		streamDone:      streamDone,
		toolCallToRPCID: make(map[string]string),
		pendingAnswers:  make(map[string]map[string]string),
	}
	rewriter := &permissionResponseRewriter{
		inner:     writer,
		transport: transport,
	}
	return transport, rewriter, sdkReader
}

func (t *legacyACPTransport) forward(source io.Reader) {
	destination := t.output
	reader := &extensionMethodReader{reader: bufio.NewReader(source), maxFrame: maxACPFrameSize}
	defer func() { _ = destination.Close() }()
	for {
		line, err := reader.readFrame()
		// Decode once for interception and SDK-compatible notification admission.
		var notification legacyACPEnvelope
		// Only successful temporary decodes are used; legacy wire semantics stay intact.
		decoded := jsonv2.Unmarshal(line, &notification, json.DefaultOptionsV1(), json.ReportErrorsWithLegacySemantics(false)) == nil
		var intercepted bool
		if decoded {
			intercepted = t.interceptEnvelope(notification, line)
		} else {
			// Legacy responses historically ignore unrelated malformed envelope fields.
			intercepted = t.intercept(line)
		}
		if len(line) > 0 && !intercepted {
			ackID := ""
			if decoded {
				// This method belongs only to this local transport, never the provider.
				if notification.Method == localNotificationBarrierMethod {
					_ = destination.CloseWithError(errors.New("provider used reserved ACP barrier method"))
					return
				}
				if notification.ID == nil {
					switch notification.Method {
					case acpsdk.ClientMethodSessionUpdate, persistenthost.ACPPromptResultMethod, persistenthost.ACPInteractionCommandMethod:
						ackID = fmt.Sprintf("ao-local-%d", t.nextID.Add(1))
					}
				}
			}
			if _, writeErr := destination.Write(line); writeErr != nil {
				_ = destination.CloseWithError(writeErr)
				return
			}
			if ackID != "" {
				// A separate SDK-ordered notification preserves original frame bytes
				// and the 10 MiB ceiling while waiting for the preceding callback.
				separator := ""
				if len(line) > 0 && line[len(line)-1] != '\n' {
					separator = "\n"
				}
				if _, writeErr := fmt.Fprintf(destination, "%s{\"jsonrpc\":\"2.0\",\"method\":%q,\"params\":%q}\n", separator, localNotificationBarrierMethod, ackID); writeErr != nil {
					_ = destination.CloseWithError(writeErr)
					return
				}
				select {
				case completed := <-t.updateProcessed:
					if completed != ackID {
						_ = destination.CloseWithError(errors.New("ACP notification acknowledgment mismatch"))
						return
					}
				case <-t.streamDone:
					return
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				_ = destination.Close()
			} else {
				_ = destination.CloseWithError(err)
			}
			return
		}
	}
}

func (t *legacyACPTransport) intercept(line []byte) bool {
	var envelope struct {
		ID     json.RawMessage      `json:"id"`
		Result json.RawMessage      `json:"result"`
		Error  *acpsdk.RequestError `json:"error"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return false
	}
	return t.interceptEnvelope(legacyACPEnvelope{ID: &envelope.ID, Result: envelope.Result, Error: envelope.Error}, line)
}

func (t *legacyACPTransport) interceptEnvelope(envelope legacyACPEnvelope, line []byte) bool {
	if envelope.Method == "session/request_permission" && envelope.ID != nil && len(*envelope.ID) > 0 {
		var req struct {
			Params struct {
				ToolCall struct {
					ToolCallID string `json:"toolCallId"`
				} `json:"toolCall"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &req) == nil {
			rawID := string(bytes.TrimSpace(*envelope.ID))
			t.mu.Lock()
			if req.Params.ToolCall.ToolCallID != "" {
				t.toolCallToRPCID[req.Params.ToolCall.ToolCallID] = rawID
			}
			t.latestPermissionRPCID = rawID
			t.mu.Unlock()
		}
	}

	var requestID string
	if envelope.ID != nil && len(*envelope.ID) > 0 && json.Unmarshal(*envelope.ID, &requestID) == nil &&
		strings.HasPrefix(requestID, "ao-legacy-") {
		t.mu.Lock()
		pending := t.pending[requestID]
		delete(t.pending, requestID)
		t.mu.Unlock()
		if pending != nil {
			var responseErr error
			if envelope.Error != nil {
				responseErr = envelope.Error
			}
			pending <- legacyACPResponse{err: responseErr}
		}
		return true
	}

	if len(envelope.Result) > 0 {
		var setup struct {
			Models *legacySessionModelState `json:"models"`
		}
		if json.Unmarshal(envelope.Result, &setup) == nil && setup.Models != nil {
			t.mu.Lock()
			state := *setup.Models
			state.Available = append([]legacyModelInfo(nil), setup.Models.Available...)
			t.models = &state
			t.mu.Unlock()
		}
	}
	return false
}

func (t *legacyACPTransport) modelState() *legacySessionModelState {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.models == nil {
		return nil
	}
	state := *t.models
	state.Available = append([]legacyModelInfo(nil), t.models.Available...)
	return &state
}

func (t *legacyACPTransport) setModel(ctx context.Context, sessionID, modelID string) error {
	requestID := fmt.Sprintf("ao-legacy-%d", t.nextID.Add(1))
	response := make(chan legacyACPResponse, 1)
	t.mu.Lock()
	t.pending[requestID] = response
	t.mu.Unlock()

	request := map[string]any{
		"jsonrpc": "2.0",
		"id":      requestID,
		"method":  "session/set_model",
		"params": map[string]string{
			"sessionId": sessionID,
			"modelId":   modelID,
		},
	}
	encoded, err := json.Marshal(request)
	if err == nil {
		encoded = append(encoded, '\n')
		_, err = t.writer.Write(encoded)
	}
	if err != nil {
		t.removePending(requestID)
		return err
	}

	select {
	case result := <-response:
		return result.err
	case <-ctx.Done():
		t.removePending(requestID)
		return ctx.Err()
	}
}

func (t *legacyACPTransport) removePending(requestID string) {
	t.mu.Lock()
	delete(t.pending, requestID)
	t.mu.Unlock()
}

func (t *legacyACPTransport) registerAnswersForToolCall(toolCallID string, answers map[string]string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	rpcID := t.toolCallToRPCID[toolCallID]
	if rpcID == "" {
		rpcID = t.latestPermissionRPCID
	}
	if rpcID != "" {
		t.pendingAnswers[rpcID] = answers
	}
}

func (t *legacyACPTransport) hasPendingAnswers() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.pendingAnswers) > 0
}

func (t *legacyACPTransport) rewriteResponse(data []byte) []byte {
	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Result map[string]any  `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || len(envelope.ID) == 0 || envelope.Result == nil {
		return data
	}

	rpcID := string(bytes.TrimSpace(envelope.ID))
	t.mu.Lock()
	answers, ok := t.pendingAnswers[rpcID]
	if ok {
		delete(t.pendingAnswers, rpcID)
	}
	t.mu.Unlock()

	if !ok || len(answers) == 0 {
		return data
	}

	var rawMsg map[string]any
	if err := json.Unmarshal(data, &rawMsg); err != nil {
		return data
	}
	envelope.Result["answers"] = answers
	rawMsg["result"] = envelope.Result
	modifiedLine, err := json.Marshal(rawMsg)
	if err != nil {
		return data
	}
	return append(modifiedLine, '\n')
}

type permissionResponseRewriter struct {
	inner     *lockedWriteCloser
	transport *legacyACPTransport
}

func (r *permissionResponseRewriter) Write(data []byte) (int, error) {
	originalLength := len(data)
	if r.transport != nil && r.transport.hasPendingAnswers() {
		data = r.transport.rewriteResponse(data)
	}
	n, err := r.inner.Write(data)
	if err != nil {
		return 0, err
	}
	if n != len(data) {
		return 0, io.ErrShortWrite
	}
	return originalLength, nil
}

func (r *permissionResponseRewriter) Close() error {
	return r.inner.Close()
}
