//go:build !windows

package e2e

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in: actual daemon, SQLite, detached ACP host and Claude model; isolated data.
func TestClaudeWorkerUpdateKeepsQuestionAndDeliversAnswerOnce(t *testing.T) {
	if os.Getenv("AO_LIVE_CLAUDE_QUESTION") != "1" {
		t.Skip("set AO_LIVE_CLAUDE_QUESTION=1 with the candidate ACP runtime")
	}
	config := filepath.Join(t.TempDir(), "claude-config")
	if err := os.Mkdir(config, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "settings.json"), []byte(`{"model":"haiku","disableAllHooks":true,"permissions":{"deny":["Bash"]},"env":{"CLAUDE_CODE_USE_BEDROCK":"1","AWS_REGION":"us-east-1","CLAUDE_CODE_DISABLE_AUTO_MEMORY":"1","ANTHROPIC_DEFAULT_HAIKU_MODEL":"us.anthropic.claude-haiku-4-5-20251001-v1:0"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "1")
	t.Setenv("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "1")
	t.Setenv("ANTHROPIC_DEFAULT_HAIKU_MODEL", "us.anthropic.claude-haiku-4-5-20251001-v1:0")
	data := t.TempDir()
	d := startDaemon(t, data)
	project := seedProject(t, d, "claude-worker-question")
	d.mustCall("PUT", "/projects/"+project+"/config", http.StatusOK, map[string]any{"config": map[string]any{
		"agentConfig": map[string]any{"permissions": "default"},
		"env":         map[string]string{"CLAUDE_CONFIG_DIR": config, "CLAUDE_CODE_USE_BEDROCK": "1", "AWS_REGION": "us-east-1"},
	}}, nil)
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
	d.awaitConversation(session, 2*time.Minute, "the accepted answer continuation", func(s snapshot) bool {
		return len(s.Turns) > len(after.Turns) && terminal(s.Turns[len(s.Turns)-1].State)
	})
	d.mustCall("POST", resolvePath, http.StatusNoContent, answer, nil)
	answered := d.awaitConversation(session, 2*time.Minute, "the delayed human answer", func(s snapshot) bool {
		for _, a := range s.Activities {
			if a.RequestID == form.RequestID && a.Status == "resolved" {
				return len(s.Turns) > len(after.Turns) && terminal(s.Turns[len(s.Turns)-1].State)
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
	if countAnswers(answered) != 1 || !strings.Contains(answered.assistantText(), "Blue") {
		t.Fatalf("delayed answer missing or repeated: %s", describe(answered))
	}
	d.mustCall("POST", resolvePath, http.StatusNoContent, answer, nil)
	if repeated := d.conversation(session); countAnswers(repeated) != 1 || len(repeated.Turns) != len(answered.Turns) {
		t.Fatalf("repeated answer opened another turn: %s", describe(repeated))
	}
	t.Logf("PASS full daemon/SQLite/ACP/model: worker processed; question %s retained; delayed human answer delivered once", form.RequestID)
}
