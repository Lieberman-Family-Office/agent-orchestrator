//go:build !windows

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func isolatedClaudeRouter(t *testing.T, hooks map[string]any, allowBash bool) (*daemon, string, string) {
	t.Helper()
	base := os.Getenv("ANTHROPIC_BASE_URL")
	endpoint, err := url.Parse(base)
	if err != nil || endpoint.Scheme != "http" || endpoint.Host != "127.0.0.1:20128" || endpoint.User != nil || os.Getenv("ANTHROPIC_AUTH_TOKEN") == "" {
		t.Fatal("isolated model validation requires local 9router and an inherited router token")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Join(home, ".ao", "data"), "isolated-guidance-validation-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	config := filepath.Join(root, "claude-config")
	if err := os.Mkdir(config, 0o700); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"CLAUDE_CONFIG_DIR": config, "ANTHROPIC_BASE_URL": base,
		"CLAUDE_CODE_USE_BEDROCK": "0", "CLAUDE_CODE_USE_VERTEX": "0", "CLAUDE_CODE_USE_FOUNDRY": "0",
		"CLAUDE_CODE_DISABLE_AUTO_MEMORY": "1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
		"ANTHROPIC_DEFAULT_OPUS_MODEL": "cc/claude-opus-5-5(max)",
	}
	for key, value := range env {
		t.Setenv(key, value)
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	permissions := map[string]any{"defaultMode": "bypassPermissions"}
	if !allowBash {
		permissions["deny"] = []string{"Bash"}
	}
	settings, err := json.Marshal(map[string]any{
		"model": "cc/claude-opus-5-5(max)", "env": env,
		"disableAllHooks": hooks == nil, "hooks": hooks, "permissions": permissions,
		"skipDangerousModePermissionPrompt": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "settings.json"), settings, 0o600); err != nil {
		t.Fatal(err)
	}
	d := startDaemon(t, root)
	project := seedProject(t, d, "claude-router-guidance")
	d.mustCall("PUT", "/projects/"+project+"/config", http.StatusOK, map[string]any{"config": map[string]any{
		"agentConfig": map[string]any{"permissions": "bypass-permissions"}, "env": env,
	}}, nil)
	return d, project, config
}

// Opt-in: actual daemon, SQLite, detached ACP host and Claude model; isolated data.
func TestClaudeWorkerUpdateKeepsQuestionAndDeliversAnswerOnce(t *testing.T) {
	if os.Getenv("AO_LIVE_CLAUDE_QUESTION") != "1" {
		t.Skip("set AO_LIVE_CLAUDE_QUESTION=1 with the candidate ACP runtime")
	}
	d, project, config := isolatedClaudeRouter(t, nil, false)
	// Use the native provider's configured model; AO receives no explicit model override.
	session := spawn(t, d, map[string]any{"projectId": project, "kind": "worker", "harness": "claude-code", "mode": "chat", "prompt": "This is an isolated acceptance test. Use only AskUserQuestion. Ask 'Which color?' with Blue and Red options. Do not invent an answer. Independent worker results can be acknowledged before this question is answered. After receiving the color answer, acknowledge it in plain text and end the turn. Do not run tools or submit reports after acknowledging it."}).Session.ID
	question := func(s snapshot) (activity, bool) {
		for _, a := range s.Activities {
			if a.Kind == "user_input" && a.Status == "pending" {
				return a, true
			}
		}
		return activity{}, false
	}
	before := d.awaitConversation(session, 2*time.Minute, "the unanswered Claude question", func(s snapshot) bool { _, ok := question(s); return ok })
	form, _ := question(before)
	const workerToken = "DAEMON_WORKER_54_PROOF_93ae"
	d.mustCall("POST", "/sessions/"+session+"/conversation/steer-or-send", http.StatusAccepted, map[string]any{"text": "Independent worker result: " + workerToken + ". Report this exact token. This is not a color answer or a request to stop.", "senderSessionId": "worker-54", "clientMessageId": "worker-question-proof"}, nil)
	after := d.awaitConversation(session, 2*time.Minute, "worker processing while the form stays unanswered", func(s snapshot) bool {
		return strings.Contains(s.assistantText(), workerToken) && len(s.Turns) > 0 && terminal(s.Turns[len(s.Turns)-1].State)
	})
	retained, ok := question(after)
	if !ok || retained.RequestID != form.RequestID {
		t.Fatalf("worker removed the unanswered form: %s", describe(after))
	}
	var detail struct {
		PersistentQuestion bool `json:"persistentQuestion"`
		ProviderPending    bool `json:"providerPending"`
	}
	if err := json.Unmarshal(retained.DetailJSON, &detail); err != nil || !detail.PersistentQuestion || detail.ProviderPending {
		t.Fatalf("question was not durably detached: %s %v", retained.DetailJSON, err)
	}
	if strings.Contains(after.assistantText(), "The user doesn't want to take this action right now. STOP") {
		t.Fatal("automatic worker delivery generated a false user STOP")
	}
	resolvePath := "/sessions/" + session + "/conversation/inputs/" + url.PathEscape(form.RequestID) + "/resolve"
	answer := map[string]any{"action": "accept", "content": map[string]any{"question_0": "Blue"}}
	status, err := d.call("POST", resolvePath, answer, nil)
	if err != nil || (status != http.StatusNoContent && status != http.StatusConflict) {
		t.Fatalf("answer acceptance status=%d error=%v", status, err)
	}
	// ACP starts a new turn asynchronously. A 409 keeps the form pending until
	// the accepted continuation completes; retry the identical answer to recover it.
	continuation := d.awaitConversation(session, 2*time.Minute, "the completed answer continuation", func(s snapshot) bool {
		return len(s.Turns) > len(after.Turns) && s.Turns[len(s.Turns)-1].State == "completed"
	})
	answerTurnID := continuation.Turns[len(continuation.Turns)-1].ID
	d.mustCall("POST", resolvePath, http.StatusNoContent, answer, nil)
	answered := d.awaitConversation(session, 2*time.Minute, "the delayed human answer", func(s snapshot) bool {
		for _, a := range s.Activities {
			if a.RequestID == form.RequestID && a.Status == "resolved" {
				return len(s.Turns) > len(after.Turns) && s.Turns[len(s.Turns)-1].State == "completed"
			}
		}
		return false
	})
	countAnswers := func(s snapshot) int {
		n := 0
		for _, m := range s.Messages {
			if m.Role == "user" && m.Origin == "human" && strings.Contains(m.Text, "Response to an earlier unanswered question") {
				n++
			}
		}
		return n
	}
	answerText, answerPrompt := "", ""
	for _, m := range answered.Messages {
		if m.TurnID != answerTurnID {
			continue
		}
		if m.Role == "assistant" {
			answerText += m.Text
		} else if m.Role == "user" && m.Origin == "human" {
			answerPrompt = m.Text
		}
	}
	if countAnswers(answered) != 1 || !strings.Contains(answerText, "Blue") || !strings.Contains(answerPrompt, "Response to an earlier unanswered question") {
		t.Fatalf("completed answer turn lacks its own acknowledgement or duplicated input: %s", describe(answered))
	}
	countNativeAnswers := func() int {
		t.Helper()
		files, err := filepath.Glob(filepath.Join(config, "projects", "*", "*.jsonl"))
		if err != nil || len(files) == 0 {
			t.Fatalf("answer native transcript unavailable: files=%d error=%v", len(files), err)
		}
		inputs := make(map[string]bool)
		for _, file := range files {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(string(data), "\n") {
				if strings.TrimSpace(line) == "" {
					continue
				}
				var event struct {
					Type    string `json:"type"`
					UUID    string `json:"uuid"`
					Message struct {
						Content json.RawMessage `json:"content"`
					} `json:"message"`
				}
				if err := json.Unmarshal([]byte(line), &event); err != nil {
					t.Fatal(err)
				}
				if event.Type != "user" {
					continue
				}
				var text string
				if json.Unmarshal(event.Message.Content, &text) != nil {
					var blocks []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					}
					if json.Unmarshal(event.Message.Content, &blocks) != nil {
						continue
					}
					for _, block := range blocks {
						if block.Type == "text" {
							text += block.Text
						}
					}
				}
				if text == answerPrompt {
					if event.UUID == "" {
						t.Fatal("native answer input lacked an identity")
					}
					inputs[event.UUID] = true
				}
			}
		}
		return len(inputs)
	}
	if got := countNativeAnswers(); got != 1 {
		t.Fatalf("native answer inputs=%d, want one", got)
	}
	d.mustCall("POST", resolvePath, http.StatusNoContent, answer, nil)
	if repeated := d.conversation(session); countAnswers(repeated) != 1 || len(repeated.Turns) != len(answered.Turns) || countNativeAnswers() != 1 {
		t.Fatalf("repeated answer opened another AO or native delivery: %s", describe(repeated))
	}
	t.Logf("PASS full daemon/SQLite/ACP/model: worker processed; question %s retained; delayed human answer delivered once", form.RequestID)
}

func TestClaudeHumanGuidanceDoesNotCancelToolAndStopsPendingGuidance(t *testing.T) {
	if os.Getenv("AO_LIVE_CLAUDE_QUESTION") != "1" {
		t.Skip("set AO_LIVE_CLAUDE_QUESTION=1 with the candidate ACP runtime")
	}
	barrier := t.TempDir()
	ready, release := filepath.Join(barrier, "ready"), filepath.Join(barrier, "release")
	hook := filepath.Join(barrier, "hold-hook.py")
	hookSource := fmt.Sprintf("import json,pathlib,time\nready=pathlib.Path(%q)\nrelease=pathlib.Path(%q)\nready.write_text('ready')\ndeadline=time.monotonic()+60\nwhile not release.exists():\n if time.monotonic()>deadline: raise SystemExit('isolated hook barrier timed out')\n time.sleep(0.02)\nprint(json.dumps({'continue':True}))\n", ready, release)
	if err := os.WriteFile(hook, []byte(hookSource), 0o600); err != nil {
		t.Fatal(err)
	}
	hooks := map[string]any{"PreToolUse": []map[string]any{{"matcher": "Bash", "hooks": []map[string]any{{"type": "command", "command": "python3 '" + hook + "'", "timeout": 90}}}}}
	d, project, config := isolatedClaudeRouter(t, hooks, true)
	const toolToken = "GUIDANCE_TOOL_FINISHED_08da"
	const humanToken = "HUMAN_NEXT_ACK_29bf"
	session := spawn(t, d, map[string]any{"projectId": project, "kind": "worker", "harness": "claude-code", "mode": "chat", "prompt": "Isolated test: call Bash exactly once with command 'printf " + toolToken + "'. Then state its output and acknowledge any later ordinary guidance token. Do not use other tools or reports. End after acknowledging."}).Session.ID
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("native Bash hook did not reach barrier: %s", describe(d.conversation(session)))
		}
		time.Sleep(50 * time.Millisecond)
	}
	d.mustCall("POST", "/sessions/"+session+"/conversation/steer", http.StatusAccepted, map[string]any{"text": "Ordinary human guidance: report exactly " + humanToken + " after the running tool finishes. This is not a stop request.", "clientMessageId": "human-next-hook-proof"}, nil)
	if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	completed := d.awaitConversation(session, 2*time.Minute, "human guidance without hook cancellation", func(s snapshot) bool {
		return len(s.Turns) > 0 && s.Turns[0].State == "completed" && strings.Contains(s.assistantText(), humanToken)
	})
	toolCompleted := false
	for _, a := range completed.Activities {
		var detail struct {
			Output string `json:"output"`
		}
		if a.Kind == "command" && a.Status == "completed" && json.Unmarshal(a.DetailJSON, &detail) == nil && strings.Contains(detail.Output, toolToken) {
			toolCompleted = true
		}
	}
	if !toolCompleted || !strings.Contains(completed.assistantText(), toolToken) {
		t.Fatalf("guidance preempted the running tool or lost its real output: %s", describe(completed))
	}
	checkNative := func(forbidden string) {
		t.Helper()
		files, err := filepath.Glob(filepath.Join(config, "projects", "*", "*.jsonl"))
		if err != nil || len(files) == 0 {
			t.Fatalf("native transcript unavailable: files=%d error=%v", len(files), err)
		}
		for _, file := range files {
			bytes, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(bytes), forbidden) {
				t.Fatalf("native transcript contained forbidden result %q", forbidden)
			}
		}
	}
	checkNative("The user doesn't want to take this action right now. STOP")
	const pendingToken = "MUST_NOT_EXECUTE_AFTER_STOP_7dc2"
	stopTurn, _ := send(t, d, session, "Use only AskUserQuestion. Ask 'Which shape?' with Circle and Square. Do not choose a shape. After the answer acknowledge any later guidance token. Do not report or use other tools.", "open-stop-question")
	question := d.awaitConversation(session, 2*time.Minute, "an unanswered question before human guidance and Stop", func(s snapshot) bool {
		for _, a := range s.Activities {
			if a.Kind == "user_input" && a.Status == "pending" && a.TurnID == stopTurn {
				return true
			}
		}
		return false
	})
	d.mustCall("POST", "/sessions/"+session+"/conversation/steer", http.StatusAccepted, map[string]any{"text": "Ordinary human guidance: report " + pendingToken + " after the answer.", "clientMessageId": "human-next-stopped-proof"}, nil)
	d.mustCall("POST", "/sessions/"+session+"/conversation/interrupt", http.StatusNoContent, nil, nil)
	stopped := d.awaitConversation(session, 2*time.Minute, "explicit Stop with pending native human guidance", func(s snapshot) bool {
		for _, turn := range s.Turns {
			if turn.ID == stopTurn {
				return turn.State == "interrupted" || turn.State == "cancelled"
			}
		}
		return false
	})
	if strings.Contains(stopped.assistantText(), pendingToken) {
		t.Fatalf("pending guidance executed after Stop: %s", describe(stopped))
	}
	contextTurn, _ := send(t, d, session, "/context", "echo-less-after-stop")
	d.awaitConversation(session, 2*time.Minute, "an echo-less native command after Stop without orphan debt", func(s snapshot) bool {
		for _, turn := range s.Turns {
			if turn.ID == contextTurn {
				return turn.State == "completed"
			}
		}
		return false
	})
	const laterToken = "NEW_PROMPT_AFTER_STOP_b1f4"
	laterTurn, _ := send(t, d, session, "Reply with exactly "+laterToken+". Do not call tools or process earlier cancelled guidance.", "fresh-after-stop")
	final := d.awaitConversation(session, 2*time.Minute, "a fresh prompt after Stop without surviving guidance", func(s snapshot) bool {
		for _, turn := range s.Turns {
			if turn.ID == laterTurn {
				return turn.State == "completed" && strings.Contains(s.assistantText(), laterToken)
			}
		}
		return false
	})
	if strings.Contains(final.assistantText(), pendingToken) {
		t.Fatalf("cancelled human guidance reappeared on the next prompt: %s", describe(final))
	}
	checkNative(pendingToken)
	t.Logf("PASS real9router daemon/SQLite/native: human guidance completed the running hook/tool, Stop prevented pending guidance, later prompt completed; session=%s, question_activities=%d", session, len(question.Activities))
}
