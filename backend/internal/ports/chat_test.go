package ports

import (
	"errors"
	"reflect"
	"testing"
)

func TestChatProviderFailureNormalizesProviderCopy(t *testing.T) {
	for _, tc := range []struct {
		name, title, detail, wantMessage string
	}{
		{"title and detail", " Access denied ", " Choose a plan. ", "Access denied\n\nChoose a plan."},
		{"detail only", "", "Connection closed", "Connection closed"},
		{"duplicate detail", "Quota reached", " Quota reached ", "Quota reached"},
		{"empty", "", "", "Provider error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failure := NewChatProviderFailure(tc.title, tc.detail, ErrChatAuthRequired)
			if failure.Error() != tc.wantMessage {
				t.Fatalf("failure = %#v / %q", failure, failure.Error())
			}
			if !errors.Is(failure, ErrChatAuthRequired) || errors.Is(NewChatProviderFailure(tc.title, tc.detail, nil), ErrChatAuthRequired) {
				t.Fatal("authentication must follow the typed cause, not provider prose")
			}
		})
	}
}

func TestPersistentQuestionBindsEveryCustomAnswer(t *testing.T) {
	question := map[string]any{"type": "string", "oneOf": []any{map[string]any{"const": "native"}}}
	custom := map[string]any{"type": "string", "_meta": map[string]any{"_askUserQuestionCustomAnswer": map[string]any{"isCustomAnswer": true, "questionId": "question_0"}}}
	for _, tc := range []struct {
		name       string
		properties map[string]any
		persistent bool
	}{
		{"complete", map[string]any{"question_0": question, "question_0_custom": custom}, true},
		{"orphan custom", map[string]any{"question_0_custom": custom}, false},
		{"wrong binding", map[string]any{"question_1": question, "question_1_custom": custom}, false},
		{"unmarked custom", map[string]any{"question_0": question, "question_0_custom": map[string]any{"type": "string"}}, false},
		{"extra consent field", map[string]any{"question_0": question, "question_0_custom": custom, "permission": map[string]any{"type": "boolean"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := ChatInputRequest{Mode: ChatInputModeForm, Schema: map[string]any{"type": "object", "properties": tc.properties}}
			if got := request.PersistentQuestion(); got != tc.persistent {
				t.Fatalf("persistent=%v want=%v", got, tc.persistent)
			}
		})
	}
	request := ChatInputRequest{Mode: ChatInputModeForm, Schema: map[string]any{"type": "object", "properties": map[string]any{"question_0": question, "question_0_custom": custom}}}
	for _, tc := range []struct {
		name    string
		content map[string]any
		valid   bool
	}{
		{"offered", map[string]any{"question_0": "native"}, true},
		{"custom", map[string]any{"question_0_custom": "my answer"}, true},
		{"empty", map[string]any{}, false},
		{"blank custom", map[string]any{"question_0_custom": " "}, false},
		{"unoffered", map[string]any{"question_0": "invented"}, false},
		{"unknown custom", map[string]any{"question_0": "native", "other_custom": "answer"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := request.ValidateQuestionResponse(ChatInputResponse{Action: ChatInputActionAccept, Content: tc.content})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
	request.Mode = ChatInputModeURL
	if request.PersistentQuestion() {
		t.Fatal("URL consent classified as a persistent question")
	}
}

func TestMissingCapabilitiesForPermissions(t *testing.T) {
	caps := ChatCapabilities{
		ChatCapabilityStreaming: true,
		ChatCapabilityInterrupt: true,
		ChatCapabilityResume:    true,
	}

	if got := MissingCapabilitiesForPermissions(caps, PermissionModeDefault); !reflect.DeepEqual(
		got, []ChatCapability{ChatCapabilityApprovals},
	) {
		t.Fatalf("default missing = %v, want approvals", got)
	}
	if got := MissingCapabilitiesForPermissions(caps, PermissionModeBypassPermissions); len(got) != 0 {
		t.Fatalf("bypass missing = %v, want none", got)
	}

	delete(caps, ChatCapabilityInterrupt)
	if got := MissingCapabilitiesForPermissions(caps, PermissionModeBypassPermissions); !reflect.DeepEqual(
		got, []ChatCapability{ChatCapabilityInterrupt},
	) {
		t.Fatalf("bypass missing = %v, want interrupt", got)
	}
}
