package memory

// These tests cover the local authorizer and the shared endpoint policy.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

const callerToken = "caller-bearer"

func TestLocalStandaloneAuthorizerRequiresSeparateOwnerAndWorkerTokens(t *testing.T) {
	owner := sha256.Sum256([]byte("owner-token"))
	worker := sha256.Sum256([]byte("worker-token"))
	auth, err := NewLocalStandaloneAuthorizer(LocalStandaloneConfig{
		OwnerTokenSHA256: hex.EncodeToString(owner[:]), WorkerTokenSHA256: hex.EncodeToString(worker[:]),
	})
	if err != nil {
		t.Fatalf("NewLocalStandaloneAuthorizer: %v", err)
	}
	scope := Scope{WorkspaceID: "workspace", ProjectID: "project"}
	request := Authorization{Action: "search:memories", Scope: scope, Context: NewContextScope(scope, "search:memories", "internal")}
	for _, test := range []struct {
		token, principal, role string
	}{
		{"owner-token", "local-owner", roleHuman},
		{"worker-token", "local-worker", roleService},
	} {
		decision, err := auth.Authorize(context.Background(), test.token, request)
		if err != nil || !decision.Allowed || decision.PrincipalID != test.principal || decision.Role != test.role || !decision.ExternalTransfer || !decision.ContentSafe || decision.Scope != scope || decision.Context != request.Context {
			t.Fatalf("decision for %s = %#v, %v", test.principal, decision, err)
		}
	}
	if _, err := auth.Authorize(context.Background(), "wrong-token", request); !errors.Is(err, ErrDenied) {
		t.Fatalf("wrong token error = %v", err)
	}
	if _, err := NewLocalStandaloneAuthorizer(LocalStandaloneConfig{OwnerTokenSHA256: "bad", WorkerTokenSHA256: hex.EncodeToString(worker[:])}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad digest error = %v", err)
	}
	if _, err := NewLocalStandaloneAuthorizer(LocalStandaloneConfig{OwnerTokenSHA256: hex.EncodeToString(owner[:]), WorkerTokenSHA256: hex.EncodeToString(owner[:])}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("same digest error = %v", err)
	}
}

func TestConstructorURLPolicy(t *testing.T) {
	cases := []struct {
		url string
		ok  bool
	}{
		{"https://policy.example.com/v1/authorize", true},
		{"http://127.0.0.1:11434/v1/authorize", true},
		{"http://localhost:9999/v1/authorize", true},
		{"http://[::1]:9999/v1/authorize", true},
		{"http://policy.example.com/v1/authorize", false},
		{"http://10.0.0.5/v1/authorize", false},
		{"ftp://policy.example.com/v1", false},
		{"policy.example.com/v1/authorize", false},
		{"https:///v1/authorize", false},
		{"//policy.example.com/v1/authorize", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.url, func(t *testing.T) {
			err := validateEndpoint(testCase.url)
			if testCase.ok && err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if !testCase.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}
