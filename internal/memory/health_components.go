package memory

import (
	"context"
	"net/http"
	"time"
)

// ComponentHealth contains only fixed status codes, never provider errors,
// paths, row values, connection strings or credentials.
type ComponentHealth struct {
	Database  string `json:"database"`
	Schema    string `json:"schema"`
	Search    string `json:"search"`
	WriteGate string `json:"write_gate"`
	Blobs     string `json:"blobs"`
}

type componentHealthStore interface {
	componentHealth(context.Context) ComponentHealth
}

func healthState(err error) string {
	if err != nil {
		return "unavailable"
	}
	return "ready"
}

func (p *Postgres) componentHealth(ctx context.Context) ComponentHealth {
	out := ComponentHealth{Database: "unavailable", Schema: "unavailable", Search: "unavailable", WriteGate: "unavailable"}
	var one int
	if p == nil || p.pool == nil || p.q().QueryRow(ctx, "SELECT 1").Scan(&one) != nil {
		return out
	}
	out.Database = "ready"
	out.Schema = healthState(checkSchemaHistory(ctx, p.q(), schemaMigrations(), true))
	out.Search = healthState(p.checkSearchSchema(ctx))
	gate, err := p.recoveryWriteGate(ctx, "")
	out.WriteGate = healthState(err)
	if err == nil && gate.Gate.State != "open" {
		out.WriteGate = "paused"
	}
	return out
}

func (s *Service) serveComponentHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	q := r.URL.Query()
	scope := Scope{WorkspaceID: q.Get("workspace_id"), ProjectID: q.Get("project_id"), UserID: q.Get("user_id"), AgentID: q.Get("agent_id")}
	d, err := s.authorize(ctx, bearer(r), "ops:metrics", scope, "", "")
	if err != nil {
		respond(w, nil, err)
		return
	}
	if d.Role != "human" && d.Role != "service" {
		respond(w, nil, ErrDenied)
		return
	}
	out := ComponentHealth{Database: "unsupported", Schema: "unsupported", Search: "unavailable", WriteGate: "unsupported", Blobs: "disabled"}
	if s.Repo != nil {
		if p, ok := s.Repo.(componentHealthStore); ok {
			out = p.componentHealth(ctx)
		} else {
			out.Search = healthState(s.Repo.SearchReady(ctx))
		}
	}
	// The same optional readiness identity is used for blob probes, after the
	// caller's operator permission has been checked. Never return that identity.
	out.Blobs = "disabled"
	if s.BlobDirectory != "" {
		blobCtx := ctx
		if s.ReadinessToken != "" {
			blobCtx = ContextWithPostgresToken(ctx, s.ReadinessToken)
		}
		out.Blobs = healthState(s.CheckBlobStorage(blobCtx))
	}
	writeJSON(w, http.StatusOK, out)
}
