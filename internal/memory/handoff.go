package memory

// Synthetic consultations reuse canonical records, transactions, source fences,
// artifacts and the privacy outbox. No provider or external action lives here.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const colConsultations = "consultations"
const colHandoffs = "agent_handoffs"

// Packages are data, never executable instructions or automatically verified facts.
type HandoffPackage struct {
	Goal                   string   `json:"goal" bson:"goal"`
	SelectedChoices        []string `json:"selected_user_choices" bson:"selected_user_choices"`
	Constraints            []string `json:"constraints" bson:"constraints"`
	CompletedWork          []string `json:"completed_work" bson:"completed_work"`
	RequiredOutput         []string `json:"required_output" bson:"required_output"`
	References             []Ref    `json:"references" bson:"references"`
	OpenQuestions          []string `json:"open_questions" bson:"open_questions"`
	ExpectedReturnContract []string `json:"expected_return_contract" bson:"expected_return_contract"`
	ScopedPermissions      []string `json:"scoped_permissions" bson:"scoped_permissions"`
	MaxBudget              int      `json:"max_budget" bson:"max_budget"`
	MaxHandoffDepth        int      `json:"max_handoff_depth" bson:"max_handoff_depth"`
	PrivacyScope           Scope    `json:"privacy_scope" bson:"privacy_scope"`
	Authority              string   `json:"authority" bson:"authority"`
}
type HandoffReturn struct {
	Status                string   `json:"status" bson:"status"`
	Summary               string   `json:"summary" bson:"summary"`
	Artifacts             []string `json:"artifacts" bson:"artifacts"`
	Decisions             []string `json:"decisions" bson:"decisions"`
	Assumptions           []string `json:"assumptions" bson:"assumptions"`
	OpenQuestions         []string `json:"open_questions" bson:"open_questions"`
	VerificationStatus    string   `json:"verification_status" bson:"verification_status"`
	Risks                 []string `json:"risks" bson:"risks"`
	SourceTraceReferences []Ref    `json:"source_trace_references" bson:"source_trace_references"`
	Recommendation        string   `json:"recommendation_to_parent" bson:"recommendation_to_parent"`
	Authority             string   `json:"authority" bson:"authority"`
}
type ConsultationAgent struct {
	LangsmithRunID string `json:"langsmith_run_id" bson:"langsmith_run_id"`
	InputEventID   string `json:"input_event_id" bson:"input_event_id"`
	OutputEventID  string `json:"output_event_id" bson:"output_event_id"`
	ID             string `json:"id" bson:"id"`
	AgentID        string `json:"agent_id" bson:"agent_id"`
	AgentRunID     string `json:"agent_run_id" bson:"agent_run_id"`
	ThreadID       string `json:"thread_id" bson:"thread_id"`
	TraceID        string `json:"trace_id" bson:"trace_id"`
	Status         string `json:"status" bson:"status"`
}
type SpecialistHandoff struct {
	ConsultationID   string `json:"consultation_id" bson:"consultation_id"`
	ParentAgentRunID string `json:"parent_agent_run_id" bson:"parent_agent_run_id"`
	ChildAgentRunID  string `json:"child_agent_run_id,omitempty" bson:"child_agent_run_id,omitempty"`
	FromAgentID      string `json:"from_agent_id" bson:"from_agent_id"`
	ToAgentID        string `json:"to_agent_id" bson:"to_agent_id"`
	HandoffType      string `json:"handoff_type" bson:"handoff_type"`

	ID                       string                `json:"id" bson:"id"`
	Status                   string                `json:"status" bson:"status"`
	Reason                   string                `json:"reason" bson:"reason"`
	ApprovalRequestID        string                `json:"approval_request_id" bson:"approval_request_id"`
	Package                  HandoffPackage        `json:"package" bson:"package"`
	ReturnPackage            *HandoffReturn        `json:"return_package,omitempty" bson:"return_package,omitempty"`
	EstimatedBudget          int                   `json:"estimated_budget" bson:"estimated_budget"`
	EstimatedDurationSeconds int                   `json:"estimated_duration_seconds" bson:"estimated_duration_seconds"`
	Depth                    int                   `json:"depth" bson:"depth"`
	ApprovedBy               string                `json:"approved_by,omitempty" bson:"approved_by,omitempty"`
	CapabilityResolution     *CapabilityResolution `json:"capability_resolution,omitempty" bson:"capability_resolution,omitempty"`
}
type ConsultationArtifact struct {
	Text          string `json:"text,omitempty" bson:"text,omitempty"`
	ID            string `json:"id" bson:"id"`
	SourceEventID string `json:"source_event_id" bson:"source_event_id"`
	Title         string `json:"title" bson:"title"`
	Kind          string `json:"kind" bson:"kind"`
	Status        string `json:"status" bson:"status"`
	Partial       bool   `json:"partial" bson:"partial"`
}
type ConsultationEvent struct {
	EventID     string    `json:"event_id" bson:"event_id"`
	Type        string    `json:"type" bson:"type"`
	At          time.Time `json:"at" bson:"at"`
	RequestHash string    `json:"-" bson:"request_hash"`
}
type Consultation struct {
	ID                    string                 `json:"id" bson:"id"`
	Revision              int64                  `json:"revision" bson:"revision"`
	Scope                 Scope                  `json:"scope" bson:"scope"`
	Status                string                 `json:"status" bson:"status"`
	Goal                  string                 `json:"goal" bson:"goal"`
	SourceEventID         string                 `json:"source_event_id" bson:"source_event_id"`
	TraceID               string                 `json:"trace_id,omitempty" bson:"trace_id,omitempty"`
	RootConsultationID    string                 `json:"root_consultation_id" bson:"root_consultation_id"`
	ParentConsultationID  string                 `json:"parent_consultation_id,omitempty" bson:"parent_consultation_id,omitempty"`
	SelectedOptions       []string               `json:"selected_options" bson:"selected_options"`
	Parent                ConsultationAgent      `json:"parent" bson:"parent"`
	Child                 *ConsultationAgent     `json:"child" bson:"child"`
	Handoff               *SpecialistHandoff     `json:"handoff" bson:"handoff"`
	Artifacts             []ConsultationArtifact `json:"artifacts" bson:"artifacts"`
	Events                []ConsultationEvent    `json:"events" bson:"events"`
	ClarificationQuestion string                 `json:"clarification_question,omitempty" bson:"clarification_question,omitempty"`
	ClarificationAnswer   string                 `json:"clarification_answer,omitempty" bson:"clarification_answer,omitempty"`
	Summary               string                 `json:"summary,omitempty" bson:"summary,omitempty"`
	Synthetic             bool                   `json:"synthetic" bson:"synthetic"`
}
type CreateConsultationInput struct {
	Scope          Scope  `json:"scope"`
	IdempotencyKey string `json:"idempotency_key"`
	Goal           string `json:"goal"`
	Synthetic      bool   `json:"synthetic"`
}
type ConsultationEventPayload struct {
	Product              string                 `json:"product,omitempty"`
	Question             string                 `json:"question,omitempty"`
	Answer               string                 `json:"answer,omitempty"`
	ArtifactID           string                 `json:"artifact_id,omitempty"`
	TraceID              string                 `json:"trace_id,omitempty"`
	Artifacts            []ConsultationArtifact `json:"artifacts,omitempty"`
	MaxHandoffDepth      int                    `json:"max_handoff_depth,omitempty"`
	MaxActiveSpecialists int                    `json:"max_active_specialists,omitempty"`
	EstimatedBudget      int                    `json:"estimated_budget,omitempty"`
	CapabilityResolution *CapabilityResolution  `json:"capability_resolution,omitempty"`
}
type ConsultationEventInput struct {
	Scope            Scope                    `json:"scope"`
	EventID          string                   `json:"event_id"`
	ExpectedRevision int64                    `json:"expected_revision"`
	Type             string                   `json:"type"`
	Payload          ConsultationEventPayload `json:"payload,omitempty"`
}

func handoffUUID() string {
	s := ID()
	return fmt.Sprintf("%s-%s-%s-%s-%s", s[:8], s[8:12], "4"+s[13:16], "a"+s[17:20], s[20:])
}
func consultationRecord(id string, scope Scope, actor string) Record {
	now := time.Now().UTC()
	return Record{ID: id, Scope: scope, Kind: "synthetic_consultation", Status: statusActive, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: actor, OriginProduct: "synthetic-handoff", TrustLevel: "untrusted", VerificationStatus: VerificationCandidate}
}
func (s *Service) consultationAuthorization(ctx context.Context, token, action string, scope Scope, id string) (Decision, error) {
	if !s.SyntheticHandoff {
		return Decision{}, ErrDenied
	}
	return s.authorize(ctx, token, action, scope, id, "")
}
func (s *Service) CreateConsultation(ctx context.Context, token string, in CreateConsultationInput) (Consultation, error) {
	d, err := s.consultationAuthorization(ctx, token, "write:consultations", in.Scope, "")
	if err != nil {
		return Consultation{}, err
	}
	if d.Role != "human" {
		return Consultation{}, ErrDenied
	}
	if !in.Synthetic || !validRunLabel(in.IdempotencyKey, 128) || strings.TrimSpace(in.Goal) == "" || len(in.Goal) > 2000 || containsSecret(in.Goal) {
		return Consultation{}, ErrInvalid
	}
	id := "consultation-" + ContentHash(structuredText(in.Scope)+d.PrincipalID+in.IdempotencyKey)
	requestHash := ContentHash(structuredText(in))
	var out Consultation
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		old, e := tx.Get(ctx, colConsultations, in.Scope, id)
		if e == nil {
			if old.RequestHash != requestHash {
				return ErrConflict
			}
			if e = consultationLive(ctx, tx, old); e != nil {
				return e
			}
			out = *old.Consultation
			return nil
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		source := consultationRecord(handoffUUID(), in.Scope, d.PrincipalID)
		source.Kind = "document"
		source.Text = in.Goal
		source.ContentHash = ContentHash(source.Text)
		source.SourceRef = "synthetic/consultation/" + id
		source.CollectedAt = &source.CreatedAt
		source.CreatedByType = d.Role
		if e = tx.Insert(ctx, "sources", source); e != nil {
			return e
		}
		out = Consultation{ID: id, Revision: 1, Scope: in.Scope, Status: "active", Goal: in.Goal, SourceEventID: source.ID, RootConsultationID: id, SelectedOptions: []string{}, Parent: ConsultationAgent{ID: id, AgentID: "design_consultant", LangsmithRunID: handoffUUID(), InputEventID: handoffUUID(), OutputEventID: handoffUUID(), AgentRunID: handoffUUID(), ThreadID: handoffUUID(), TraceID: handoffUUID(), Status: "active"}, Artifacts: []ConsultationArtifact{}, Events: []ConsultationEvent{}, Synthetic: true}
		r := consultationRecord(id, in.Scope, d.PrincipalID)
		r.RequestHash = requestHash
		r.SourceEventID = source.ID
		r.Sources = []SourceRef{{SourceID: source.ID, Locator: "original"}}
		r.TraceID = out.Parent.TraceID
		r.Consultation = &out
		if e = tx.Insert(ctx, colConsultations, r); e != nil {
			return e
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "create:consultations", id)
	})
	return out, err
}
func consultationLive(ctx context.Context, tx Tx, r Record) error {
	if !live(r) || r.Consultation == nil {
		return ErrNotFound
	}
	for _, ref := range r.Sources {
		source, e := tx.Get(ctx, "sources", r.Scope, ref.SourceID)
		if e != nil {
			return e
		}
		if !live(source) {
			return ErrNotFound
		}
	}
	return nil
}
func (s *Service) GetConsultation(ctx context.Context, token, id string, scope Scope) (Consultation, error) {
	_, e := s.consultationAuthorization(ctx, token, "read:consultations", scope, id)
	if e != nil {
		return Consultation{}, e
	}
	var out Consultation
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, e := tx.Get(ctx, colConsultations, scope, id)
		if e != nil {
			return e
		}
		if e = consultationLive(ctx, tx, r); e != nil {
			return e
		}
		out = *r.Consultation
		return nil
	})
	return out, e
}
func cloneConsultation(c *Consultation) Consultation {
	b, _ := json.Marshal(c)
	var out Consultation
	_ = json.Unmarshal(b, &out)
	for i := range c.Events {
		out.Events[i].RequestHash = c.Events[i].RequestHash
	}
	return out
}
func (s *Service) ConsultationEvent(ctx context.Context, token, id string, in ConsultationEventInput) (Consultation, error) {
	d, e := s.consultationAuthorization(ctx, token, "event:consultations", in.Scope, id)
	if e != nil {
		return Consultation{}, e
	}
	if !validRunLabel(in.EventID, 128) || in.ExpectedRevision < 1 || len(structuredText(in.Payload)) > 16000 || containsSecret(structuredText(in.Payload)) {
		return Consultation{}, ErrInvalid
	}
	human := slices.Contains([]string{"product_selected", "hire_proposed", "hire_approved", "hire_rejected", "clarification_answered", "artifact_selected", "artifact_approved", "handoff_returned", "integrated", "cancelled", "retried", "direct_continued", "direct_completed"}, in.Type)
	runtime := slices.Contains([]string{"clarification_requested", "artifacts_generated", "capability_resolved", "failed", "timed_out"}, in.Type)
	if !human && !runtime {
		return Consultation{}, ErrInvalid
	}
	if human && d.Role != "human" || runtime && d.Role != "service" && d.Role != "agent" {
		return Consultation{}, ErrDenied
	}
	if in.Type == "capability_resolved" && d.Role != "service" {
		return Consultation{}, ErrDenied
	}
	eventHash := ContentHash(structuredText(struct {
		Type    string
		Payload ConsultationEventPayload
	}{in.Type, in.Payload}))
	var out Consultation
	var writeArtifact handoffArtifactWriter
	transact := func() error {
		return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
			r, err := tx.Get(ctx, colConsultations, in.Scope, id)
			if err != nil {
				return err
			}
			if err = consultationLive(ctx, tx, r); err != nil {
				return err
			}
			c := cloneConsultation(r.Consultation)
			for _, event := range c.Events {
				if event.EventID == in.EventID {
					if event.RequestHash != eventHash {
						return ErrConflict
					}
					out = c
					return nil
				}
			}
			if c.ParentConsultationID != "" || r.Revision != in.ExpectedRevision || len(c.Events) >= 200 {
				return ErrConflict
			}
			// Fence the evidence before accepting any state write, including a delayed worker delivery.
			for _, ref := range r.Sources {
				if err = tx.FenceSource(ctx, in.Scope, ref.SourceID); err != nil {
					return err
				}
			}
			if err = s.applyConsultationEvent(ctx, tx, d, &r, &c, in, writeArtifact); err != nil {
				return err
			}
			c.Revision++
			c.Events = append(c.Events, ConsultationEvent{EventID: in.EventID, Type: in.Type, At: time.Now().UTC().Truncate(time.Millisecond), RequestHash: eventHash})
			r.Revision++
			r.UpdatedAt = time.Now().UTC()
			r.Consultation = &c
			if c.Handoff != nil {
				if err = syncConsultationHandoff(ctx, tx, in.Scope, d, &r, &c); err != nil {
					return err
				}
			}
			if c.Child != nil {
				if err = syncConsultationChild(ctx, tx, in.Scope, &r, &c); err != nil {
					return err
				}
			}
			if err = tx.Replace(ctx, colConsultations, r, in.ExpectedRevision); err != nil {
				return err
			}
			if err = audit(ctx, tx, in.Scope, d.PrincipalID, "handoff:"+in.Type, id); err != nil {
				return err
			}
			out = c
			return nil
		})
	}
	if in.Type == "artifacts_generated" {
		e = s.withHandoffArtifactWriter(ctx, func(writer handoffArtifactWriter) error { writeArtifact = writer; return transact() })
	} else {
		e = transact()
	}
	return out, e
}
func syncConsultationHandoff(ctx context.Context, tx Tx, scope Scope, d Decision, r *Record, c *Consultation) error {
	h, err := tx.Get(ctx, colHandoffs, scope, c.Handoff.ID)
	if errors.Is(err, ErrNotFound) {
		h = consultationRecord(c.Handoff.ID, scope, d.PrincipalID)
		h.Kind = "synthetic_handoff"
		h.Handoff = c.Handoff
		h.Sources = r.Sources
		h.SourceEventID = r.SourceEventID
		err = tx.Insert(ctx, colHandoffs, h)
	} else if err == nil {
		h.Revision++
		h.Handoff = c.Handoff
		h.Sources = r.Sources
		if c.Child != nil && c.Child.Status == "completed" {
			h.Relations = []Ref{{Collection: "execution_traces", ID: c.Child.TraceID, Revision: 1}}
		}
		h.UpdatedAt = r.UpdatedAt
		err = tx.Replace(ctx, colHandoffs, h, h.Revision-1)
	}
	return err
}
func syncConsultationChild(ctx context.Context, tx Tx, scope Scope, r *Record, c *Consultation) error {
	child, err := tx.Get(ctx, colConsultations, scope, c.Child.ID)
	if err != nil {
		return err
	}
	cc := cloneConsultation(child.Consultation)
	if c.Child.Status == "completed" {
		cc.TraceID = c.Child.TraceID
	}
	cc.Status = c.Child.Status
	cc.Revision++
	cc.Artifacts = c.Artifacts
	cc.Handoff = c.Handoff
	cc.ClarificationQuestion = c.ClarificationQuestion
	cc.ClarificationAnswer = c.ClarificationAnswer
	child.Consultation = &cc
	child.Revision++
	child.Sources = r.Sources
	if c.Child.Status == "completed" {
		child.Relations = []Ref{{Collection: "execution_traces", ID: c.Child.TraceID, Revision: 1}}
	}
	return tx.Replace(ctx, colConsultations, child, child.Revision-1)
}
func (s *Service) applyConsultationEvent(ctx context.Context, tx Tx, d Decision, r *Record, c *Consultation, in ConsultationEventInput, writeArtifact handoffArtifactWriter) error {
	p := in.Payload
	switch in.Type {
	case "product_selected":
		return consultationProductSelected(r, c, p)
	case "hire_proposed":
		return consultationHireProposed(ctx, tx, d, c, p)
	case "hire_approved", "hire_rejected":
		return consultationHireDecided(ctx, tx, d, r, c, in.Type)
	case "clarification_requested":
		return consultationClarificationRequested(c, p)
	case "clarification_answered":
		return consultationClarificationAnswered(c, p)
	case "artifacts_generated":
		return consultationArtifactsGenerated(ctx, tx, d, r, c, p, writeArtifact)
	case "capability_resolved":
		return s.applyCapabilityResolution(ctx, tx, r, c, p)
	case "artifact_selected", "artifact_approved":
		return consultationArtifactReviewed(c, in.Type, p.ArtifactID)
	case "handoff_returned":
		return consultationHandoffReturned(c)
	case "direct_completed":
		return consultationDirectCompleted(ctx, tx, r, c, p)
	case "integrated":
		return consultationIntegrated(ctx, tx, r, c, p)
	case "cancelled", "failed", "timed_out":
		return consultationStopped(c, in.Type)
	case "direct_continued":
		return consultationDirectContinued(r, c)
	case "retried":
		return consultationRetried(r, c)
	default:
		return ErrInvalid
	}
}
func consultationProductSelected(r *Record, c *Consultation, p ConsultationEventPayload) error {
	if c.Status != "active" || c.Child != nil && c.Child.Status != "cancelled" && c.Child.Status != "failed" || !slices.Contains([]string{"Printable Poster", "Digital Planner", "Wedding Template", "Sticker / Clipart Pack", "Social Media Template"}, p.Product) {
		return ErrConflict
	}
	if len(c.SelectedOptions) > 0 && c.SelectedOptions[0] != p.Product {
		rotateParentAttempt(r, c)
	}
	c.SelectedOptions = []string{p.Product}
	c.Parent.Status = "direct_mode"
	return nil
}
func consultationHireProposed(ctx context.Context, tx Tx, d Decision, c *Consultation, p ConsultationEventPayload) error {
	if c.Status != "active" || c.Child != nil || len(c.SelectedOptions) != 1 {
		return ErrConflict
	}
	if p.MaxHandoffDepth < 0 || p.MaxHandoffDepth > 2 || p.MaxActiveSpecialists < 0 || p.MaxActiveSpecialists > 1 || p.EstimatedBudget < 0 || p.EstimatedBudget > 100 {
		return ErrInvalid
	}
	if c.SelectedOptions[0] != "Printable Poster" {
		return ErrInvalid
	}
	c.Handoff = &SpecialistHandoff{ConsultationID: c.ID, ParentAgentRunID: c.Parent.AgentRunID, FromAgentID: c.Parent.AgentID, ToAgentID: "illustrator_expert", HandoffType: "create", ID: handoffUUID(), Status: "proposed", Reason: "포스터 콘셉트와 제작 명세를 구체화하기 위해 Illustrator Expert의 도움을 받습니다.", ApprovalRequestID: handoffUUID(), EstimatedBudget: 10, EstimatedDurationSeconds: 5, Depth: 1, Package: HandoffPackage{Goal: c.Goal, SelectedChoices: c.SelectedOptions, Constraints: []string{"synthetic data only", "no real model, image generation, upload or external action"}, CompletedWork: []string{"Printable Poster selected"}, RequiredOutput: []string{"poster_concepts", "image_prompts", "print_spec"}, References: []Ref{{Collection: "sources", ID: c.SourceEventID, Revision: 1}}, OpenQuestions: []string{"preferred visual style"}, ExpectedReturnContract: []string{"summary", "artifacts", "decisions", "assumptions", "open_questions", "verification_status", "risks", "source_trace_references", "recommendation_to_parent"}, ScopedPermissions: []string{"read_scoped_evidence", "create_synthetic_text_artifacts"}, MaxBudget: 100, MaxHandoffDepth: 2, PrivacyScope: c.Scope, Authority: "data_only_untrusted"}}
	if p.EstimatedBudget > 0 {
		c.Handoff.EstimatedBudget = p.EstimatedBudget
	}
	if p.MaxHandoffDepth > 0 {
		c.Handoff.Package.MaxHandoffDepth = p.MaxHandoffDepth
	}
	c.Status = "awaiting_hire_approval"
	c.Parent.Status = "awaiting_hire_approval"
	approval := consultationRecord(c.Handoff.ApprovalRequestID, c.Scope, d.PrincipalID)
	approval.Kind = "synthetic_handoff_approval"
	approval.Status = "proposed"
	approval.Target = &Ref{Collection: colHandoffs, ID: c.Handoff.ID, Revision: 1}
	if err := tx.Insert(ctx, "jobs", approval); err != nil {
		return err
	}
	return nil
}
func consultationHireDecided(ctx context.Context, tx Tx, d Decision, r *Record, c *Consultation, eventType string) error {
	if c.Status != "awaiting_hire_approval" || c.Handoff == nil || c.Handoff.Status != "proposed" || c.Child != nil {
		return ErrConflict
	}
	if c.Handoff.Depth > c.Handoff.Package.MaxHandoffDepth || c.Handoff.EstimatedBudget > c.Handoff.Package.MaxBudget {
		return ErrDenied
	}
	approval, err := tx.Get(ctx, "jobs", c.Scope, c.Handoff.ApprovalRequestID)
	if err != nil {
		return err
	}
	if approval.Status != "proposed" {
		return ErrConflict
	}
	approval.Revision++
	approval.ApprovedBy = d.PrincipalID
	approval.Status = "rejected"
	if eventType == "hire_rejected" {
		c.Handoff.Status = "rejected"
		c.Status = "active"
		c.Parent.Status = "direct_mode"
	} else {
		approval.Status = "approved"
		c.Handoff.Status = "active"
		c.Handoff.ApprovedBy = d.PrincipalID
		c.Status = "awaiting_return"
		c.Parent.Status = "waiting_for_return"
		c.Child = &ConsultationAgent{ID: handoffUUID(), AgentID: "illustrator_expert", LangsmithRunID: handoffUUID(), InputEventID: handoffUUID(), OutputEventID: handoffUUID(), AgentRunID: handoffUUID(), ThreadID: handoffUUID(), TraceID: handoffUUID(), Status: "active"}
		c.Handoff.ChildAgentRunID = c.Child.AgentRunID
		child := consultationRecord(c.Child.ID, c.Scope, d.PrincipalID)
		child.TraceID = c.Child.TraceID
		child.SourceEventID = c.SourceEventID
		child.Sources = r.Sources
		child.Consultation = &Consultation{ID: c.Child.ID, Revision: 1, Scope: c.Scope, Status: "active", Goal: c.Goal, SourceEventID: c.SourceEventID, RootConsultationID: c.ID, ParentConsultationID: c.ID, Parent: *c.Child, SelectedOptions: c.SelectedOptions, Artifacts: []ConsultationArtifact{}, Events: []ConsultationEvent{}, Synthetic: true}
		if err = tx.Insert(ctx, colConsultations, child); err != nil {
			return err
		}
	}
	if err = tx.Replace(ctx, "jobs", approval, approval.Revision-1); err != nil {
		return err
	}
	return nil
}
func consultationClarificationRequested(c *Consultation, p ConsultationEventPayload) error {
	if c.Child == nil || c.Child.Status != "active" || c.Handoff.Status != "active" || strings.TrimSpace(p.Question) == "" || len(p.Question) > 1000 {
		return ErrConflict
	}
	c.ClarificationQuestion = p.Question
	c.ClarificationAnswer = ""
	c.Child.Status = "waiting_for_user"
	c.Status = "awaiting_specialist_input"
	c.Handoff.Status = "awaiting_user"
	return nil
}
func consultationClarificationAnswered(c *Consultation, p ConsultationEventPayload) error {
	if c.Child == nil || c.Child.Status != "waiting_for_user" || strings.TrimSpace(p.Answer) == "" || len(p.Answer) > 1000 {
		return ErrConflict
	}
	c.ClarificationAnswer = p.Answer
	c.Child.Status = "active"
	c.Status = "awaiting_return"
	c.Handoff.Status = "active"
	return nil
}
func consultationArtifactsGenerated(ctx context.Context, tx Tx, d Decision, r *Record, c *Consultation, p ConsultationEventPayload, writeArtifact handoffArtifactWriter) error {
	if c.Child == nil || c.Child.Status != "active" || c.Handoff.Status != "active" || len(p.Artifacts) != 3 || p.TraceID != c.Child.TraceID {
		return ErrConflict
	}
	if err := handoffTrace(ctx, tx, c.Scope, *c.Child, p.TraceID, c.SourceEventID); err != nil {
		return err
	}

	if writeArtifact == nil || d.Role != "service" {
		return ErrDenied
	}
	source, err := tx.Get(ctx, "sources", c.Scope, c.SourceEventID)
	if err != nil {
		return err
	}
	if !live(source) {
		return ErrNotFound
	}
	seenKinds := map[string]bool{}
	seenDigests := map[string]bool{}
	for _, a := range p.Artifacts {
		if seenKinds[a.Kind] || seenDigests[ContentHash(a.Text)] || !validGeneratedArtifact(a) {
			return ErrInvalid
		}
		seenKinds[a.Kind] = true
		seenDigests[ContentHash(a.Text)] = true
		artifact, err := writeArtifact(ctx, tx, c.Scope, Ref{Collection: "sources", ID: source.ID, Revision: source.Revision}, a.Text, d)
		if err != nil {
			return err
		}
		a.ID = artifact.ID
		a.SourceEventID = source.ID
		a.Status = "draft"
		a.Partial = false
		a.Text = ""
		upsertConsultationArtifact(c, a)
	}

	c.TraceID = p.TraceID
	r.Relations = append(r.Relations, Ref{Collection: "execution_traces", ID: p.TraceID, Revision: 1})
	c.Child.Status = "completed"
	c.Handoff.Status = "awaiting_user"
	return nil
}
func validGeneratedArtifact(a ConsultationArtifact) bool {
	return slices.Contains([]string{"poster_concepts", "image_prompts", "print_spec"}, a.Kind) && len(a.Title) <= 200 && strings.TrimSpace(a.Title) != "" && a.ID == "" && a.SourceEventID == "" && strings.TrimSpace(a.Text) != "" && len(a.Text) <= 4000
}
func upsertConsultationArtifact(c *Consultation, a ConsultationArtifact) {
	replaced := false
	for i := range c.Artifacts {
		if c.Artifacts[i].ID == a.ID {
			c.Artifacts[i] = a
			replaced = true
		}
	}
	if !replaced {
		c.Artifacts = append(c.Artifacts, a)
	}
}
func consultationArtifactReviewed(c *Consultation, eventType, artifactID string) error {
	if c.Handoff == nil || c.Handoff.Status != "awaiting_user" || c.Child == nil || c.Child.Status != "completed" {
		return ErrConflict
	}
	found := false
	for i := range c.Artifacts {
		a := &c.Artifacts[i]
		if a.ID == artifactID {
			found = true
			if eventType == "artifact_selected" && a.Status == "draft" {
				a.Status = "selected"
			} else if eventType == "artifact_approved" && a.Status == "selected" {
				a.Status = "approved"
			} else {
				return ErrConflict
			}
		}
	}
	if !found {
		return ErrNotFound
	}
	return nil
}
func consultationHandoffReturned(c *Consultation) error {
	if c.Handoff == nil || c.Handoff.Status != "awaiting_user" || c.Child == nil || c.Child.Status != "completed" || len(c.Artifacts) == 0 {
		return ErrConflict
	}
	ids := []string{}
	for _, a := range c.Artifacts {
		if a.Status == "superseded" {
			continue
		}
		if a.Status != "approved" {
			return ErrConflict
		}
		ids = append(ids, a.ID)
	}
	c.Handoff.ReturnPackage = &HandoffReturn{Status: "returned", Summary: "합성 포스터 콘셉트, 이미지 프롬프트, 인쇄 명세를 전달합니다. 실제 이미지나 외부 상품은 생성하지 않았습니다.", Artifacts: ids, Decisions: []string{"user approved synthetic text artifacts"}, Assumptions: []string{"synthetic brief only"}, OpenQuestions: []string{"real asset quality and commercial suitability remain untested"}, VerificationStatus: "user_approved_synthetic_only", Risks: []string{"untrusted specialist result; no real image or market validation"}, SourceTraceReferences: []Ref{{Collection: "sources", ID: c.SourceEventID, Revision: 1}, {Collection: "execution_traces", ID: c.TraceID, Revision: 1}}, Recommendation: "Review before any real production work.", Authority: "data_only_untrusted"}
	c.Handoff.Status = "returned"
	c.Status = "integrating"
	c.Parent.Status = "integrating"
	return nil
}
func consultationDirectCompleted(ctx context.Context, tx Tx, r *Record, c *Consultation, p ConsultationEventPayload) error {
	if c.Status != "active" || c.Parent.Status != "direct_mode" {
		return ErrConflict
	}
	if err := handoffTrace(ctx, tx, c.Scope, c.Parent, p.TraceID, c.SourceEventID); err != nil {
		return err
	}
	c.Status = "completed"
	c.Parent.Status = "completed"
	c.TraceID = p.TraceID
	r.Relations = append(r.Relations, Ref{Collection: "execution_traces", ID: p.TraceID, Revision: 1})
	c.Summary = "직접 상담으로 합성 기획을 정리했습니다. 선택: " + strings.Join(c.SelectedOptions, ", ") + ". 실제 모델·이미지·상품은 생성하지 않았으며 결과는 미검증입니다."
	return nil
}
func consultationIntegrated(ctx context.Context, tx Tx, r *Record, c *Consultation, p ConsultationEventPayload) error {
	if c.Status != "integrating" || c.Handoff == nil || c.Handoff.ReturnPackage == nil {
		return ErrConflict
	}
	if err := handoffTrace(ctx, tx, c.Scope, c.Parent, p.TraceID, c.SourceEventID); err != nil {
		return err
	}
	c.TraceID = p.TraceID
	r.Relations = append(r.Relations, Ref{Collection: "execution_traces", ID: p.TraceID, Revision: 1})
	c.Status = "completed"
	c.Parent.Status = "completed"
	c.Summary = c.Handoff.ReturnPackage.Summary + " 사용자 승인된 합성 텍스트 결과이며 실제 모델·이미지·상업적 사용 검증은 별도입니다."
	return nil
}
func consultationStopped(c *Consultation, eventType string) error {
	if c.Status == "completed" || c.Status == "cancelled" || c.Status == "failed" {
		return ErrConflict
	}
	state := "cancelled"
	if eventType != "cancelled" {
		state = "failed"
	}
	c.Status = state
	c.Parent.Status = "recovery_available"
	if c.Handoff != nil {
		c.Handoff.Status = state
	}
	if c.Child != nil {
		c.Child.Status = state
	}
	for i := range c.Artifacts {
		c.Artifacts[i].Partial = true
	}
	return nil
}
func consultationDirectContinued(r *Record, c *Consultation) error {
	if c.Status != "cancelled" && c.Status != "failed" {
		return ErrConflict
	}
	rotateParentAttempt(r, c)
	c.Status = "active"
	c.Parent.Status = "direct_mode"
	return nil
}
func consultationRetried(r *Record, c *Consultation) error {
	if c.Status != "cancelled" && c.Status != "failed" {
		return ErrConflict
	}
	rotateParentAttempt(r, c)
	if c.Child == nil {
		c.Status = "active"
		c.Parent.Status = "direct_mode"
		c.Handoff = nil
	} else {
		// Retry resumes the approved child identity; never creates another run.
		c.Status = "awaiting_return"
		c.Parent.Status = "waiting_for_return"
		c.Child.Status = "active"
		c.Handoff.Status = "active"
		c.ClarificationQuestion = ""
		c.ClarificationAnswer = ""
		for i := range c.Artifacts {
			c.Artifacts[i].Status = "superseded"
		}
	}
	return nil
}

func handoffTrace(ctx context.Context, tx Tx, scope Scope, agent ConsultationAgent, traceID, sourceID string) error {
	if traceID == "" || traceID != agent.TraceID {
		return ErrConflict
	}
	trace, err := tx.Get(ctx, "execution_traces", scope, traceID)
	if err != nil {
		return err
	}
	if !live(trace) {
		return ErrNotFound
	}
	if !slices.ContainsFunc(trace.Relations, func(ref Ref) bool { return ref.Collection == "agent_runs" && ref.ID == agent.AgentRunID }) || !slices.ContainsFunc(trace.Sources, func(ref SourceRef) bool { return ref.SourceID == sourceID }) {
		return ErrConflict
	}
	return nil
}

type handoffArtifactWriter func(context.Context, Tx, Scope, Ref, string, Decision) (Record, error)

// A recovered final synthesis is a new parent attempt; immutable completed
// runtime receipts cannot be reused with a changed direct/returned brief.
func rotateParentAttempt(r *Record, c *Consultation) {
	r.Relations = append(r.Relations, Ref{Collection: "execution_traces", ID: c.Parent.TraceID, Revision: 1}, Ref{Collection: "agent_runs", ID: c.Parent.AgentRunID, Revision: 1})
	c.Parent.AgentRunID = handoffUUID()
	c.Parent.ThreadID = handoffUUID()
	c.Parent.TraceID = handoffUUID()
	c.Parent.LangsmithRunID = handoffUUID()
	c.Parent.InputEventID = handoffUUID()
	c.Parent.OutputEventID = handoffUUID()
}
