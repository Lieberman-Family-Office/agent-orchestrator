package sessionmanager

import (
	"os"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/testenv"
)

func TestMain(m *testing.M) {
	// The setup probe must inspect the environment supplied to the real child.
	if os.Getenv("POSTCREATE_STORE_PROBE") != "1" {
		testenv.ClearSession()
	}
	os.Exit(m.Run())
}
