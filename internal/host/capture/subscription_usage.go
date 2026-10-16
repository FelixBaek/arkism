package capture

// Subscription usage (ST-620, FR-012): every `claude -p` call capture makes runs on the user's Claude
// subscription. Its usage is recorded on the server outside the API model budget (record only, no
// reservation). Recording uses only the harness service credential, never the owner token; without
// ARKISM_HARNESS_TOKEN_FILE nothing is sent.

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

var subscriptionClient = &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 10 * time.Second}

// llmOperation labels a call for the ledger by the prompt it ran.
func llmOperation(system string) string {
	if system == sessionSystem {
		return "session_processor"
	}
	return "capture"
}

// subscriptionScope is ARKISM_MODEL_BUDGET_SCOPE (it must equal the server budget policy scope), else the capture scope.
func subscriptionScope() any {
	if raw := os.Getenv("ARKISM_MODEL_BUDGET_SCOPE"); raw != "" {
		if scope, err := loadObj([]byte(raw)); err == nil {
			return scope
		}
	}
	return capScope()
}

// recordSubscriptionUsage is best effort: a failed record never fails the call it describes. Tests replace it.
var recordSubscriptionUsage = func(operation, requested string, result map[string]any) {
	usage, file := host.CLIUsage(result, requested), os.Getenv("ARKISM_HARNESS_TOKEN_FILE")
	if usage == nil || file == "" {
		return
	}
	token, err := os.ReadFile(file)
	if err != nil {
		return
	}
	id := make([]byte, 16)
	rand.Read(id)
	body := host.Obj{{Key: "scope", Value: subscriptionScope()}, {Key: "operation_id", Value: "cli-" + hex.EncodeToString(id)},
		{Key: "operation", Value: operation}, {Key: "usage", Value: usage}}
	if err = postSubscriptionUsage(strings.TrimSpace(string(token)), body); err != nil {
		metric(kv("event", "subscription_usage_unrecorded"), kv("operation", operation))
	}
}

func postSubscriptionUsage(token string, body host.Obj) error {
	raw, err := host.Dumps(body, host.DumpOpts{})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, host.BaseURL+"/v1/model-budget/subscription-usage", strings.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	_, err = host.DoJSON(subscriptionClient, req)
	return err
}
