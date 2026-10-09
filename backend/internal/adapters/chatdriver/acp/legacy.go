package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	acpsdk "github.com/coder/acp-go-sdk"
)

// ACP 0.13.5 removed the legacy model selector from its generated API while
// deployed agents such as Kimi still advertise `models` and implement
// session/set_model. This narrow wire shim preserves that compatibility without
// downgrading or forking the SDK: ordinary SDK traffic is forwarded unchanged,
// and only AO-owned string request ids are intercepted.
type legacyACPTransport struct {
	writer *lockedWriteCloser
	nextID atomic.Uint64

	mu              sync.Mutex
	pending         map[string]chan legacyACPResponse
	models          *legacySessionModelState
	reader          *io.PipeReader
	updateProcessed <-chan string
	streamDone      <-chan struct{}
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

const localNotificationIDKey = "ao.localNotificationID"

func newLegacyACPTransport(stdin io.WriteCloser, stdout io.Reader, updateProcessed <-chan string, streamDone <-chan struct{}) (*legacyACPTransport, io.WriteCloser, io.Reader) {
	writer := &lockedWriteCloser{inner: stdin}
	transport := &legacyACPTransport{
		writer:          writer,
		pending:         make(map[string]chan legacyACPResponse),
		updateProcessed: updateProcessed,
		streamDone:      streamDone,
	}
	sdkReader, sdkWriter := io.Pipe()
	transport.reader = sdkReader
	go transport.forward(stdout, sdkWriter)
	return transport, writer, sdkReader
}

func (t *legacyACPTransport) forward(source io.Reader, destination *io.PipeWriter) {
	reader := bufio.NewReader(source)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && !t.intercept(line) {
			// Match the SDK's complete envelope decode before expecting its callback.
			var notification struct {
				JSONRPC string               `json:"jsonrpc"`
				ID      *json.RawMessage     `json:"id,omitempty"`
				Method  string               `json:"method,omitempty"`
				Params  json.RawMessage      `json:"params,omitempty"`
				Result  json.RawMessage      `json:"result,omitempty"`
				Error   *acpsdk.RequestError `json:"error,omitempty"`
			}
			ackID := ""
			if json.Unmarshal(line, &notification) == nil && notification.Method == acpsdk.ClientMethodSessionUpdate {
				var update acpsdk.SessionNotification
				if json.Unmarshal(notification.Params, &update) == nil && update.Validate() == nil {
					if update.Meta == nil {
						update.Meta = make(map[string]any)
					}
					// Provider metadata cannot claim a local completion token.
					delete(update.Meta, localNotificationIDKey)
					if notification.ID == nil {
						ackID = fmt.Sprintf("ao-local-%d", t.nextID.Add(1))
						update.Meta[localNotificationIDKey] = ackID
					}
					encodedParams, encodeErr := json.Marshal(update)
					if encodeErr != nil {
						_ = destination.CloseWithError(encodeErr)
						return
					}
					notification.Params = encodedParams
					encodedFrame, encodeErr := json.Marshal(notification)
					if encodeErr != nil {
						_ = destination.CloseWithError(encodeErr)
						return
					}
					line = encodedFrame
					line = append(line, '\n')
				}
			}
			if _, writeErr := destination.Write(line); writeErr != nil {
				_ = destination.CloseWithError(writeErr)
				return
			}
			if ackID != "" {
				// One exact completed notification backpressures journal replay.
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

	var requestID string
	if len(envelope.ID) > 0 && json.Unmarshal(envelope.ID, &requestID) == nil &&
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
