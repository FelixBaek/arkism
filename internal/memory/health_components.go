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
	// The three below are set only when the request asks for models=1: each is one tiny real call.
	ServerModel string `json:"server_model,omitempty"`
	Embedder    string `json:"embedder,omitempty"`
	Reranker    string `json:"reranker,omitempty"`
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

// probeModels makes one minimal call to each configured model, so a missing or unreachable
// model is found before a capture or answer silently waits on it. "disabled" means not configured.
func (s *Service) probeModels(ctx context.Context, scope Scope, out *ComponentHealth) {
	out.ServerModel, out.Embedder, out.Reranker = "disabled", "disabled", "disabled"
	if s.Wiki != nil {
		_, _, err := s.modelCall(ctx, scope, healthProbeOperation, "Reply with the single word OK.", "ping", 8)
		out.ServerModel = healthState(err)
	}
	if s.Embedder != nil {
		_, err := s.Embedder.Embed(ctx, []string{"ping"}, "query")
		out.Embedder = healthState(err)
	}
	if s.Reranker != nil {
		_, err := s.Reranker.Rerank(ctx, "ping", []string{"ping"})
		out.Reranker = healthState(err)
	}
}

const healthProbeOperation = "health_probe"

func (s *Service) serveComponentHealth(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	models := q.Get("models") == "1"
	limit := 10 * time.Second
	if models {
		limit = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), limit)
	defer cancel()
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
	if models {
		s.probeModels(ctx, scope, &out)
	}
	writeJSON(w, http.StatusOK, out)
}
