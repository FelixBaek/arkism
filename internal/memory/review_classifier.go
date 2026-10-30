package memory

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"math/rand"
	"os"
	"slices"
	"sort"
	"time"
)

// ReviewClassifier is a logistic regression over a candidate's embedding and kind, trained on this
// install's own approve/reject history. It only orders the review queue; the person still decides.
// The file holds weights derived from private memories, so it stays with the install and is never shipped.
type ReviewClassifier struct {
	Scope     Scope              `json:"scope"`
	Space     string             `json:"space"`
	Bias      float64            `json:"bias"`
	Weights   []float64          `json:"weights"`
	Kinds     map[string]float64 `json:"kinds"`
	Examples  int                `json:"examples"`
	Rejected  int                `json:"rejected"`
	TrainedAt time.Time          `json:"trained_at"`
}

type reviewExample struct {
	vec      []float64
	kind     string
	rejected bool
}

// ReviewClassifierReport is the cross-validated check printed after training.
type ReviewClassifierReport struct {
	Examples       int     `json:"examples"`
	Rejected       int     `json:"rejected"`
	AUC            float64 `json:"auc"`
	RejectsFound   int     `json:"rejects_found"`
	FalseRejects   int     `json:"false_rejects"`
	MinimumReached bool    `json:"minimum_reached"`
}

const reviewClassifierMinRejected = 20

func LoadReviewClassifier(path string) (*ReviewClassifier, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	const maximum = 4 << 20
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maximum {
		return nil, ErrInvalid
	}

	var c ReviewClassifier
	if json.Unmarshal(data, &c) != nil || c.Space == "" || len(c.Weights) == 0 || len(c.Weights) > 65536 || !validScope(c.Scope) {
		return nil, ErrInvalid
	}
	return &c, nil
}

// RejectScore is the probability, 0-1, that the person would reject a candidate like this one.
func (c *ReviewClassifier) RejectScore(vec []float64, kind string) float64 {
	z := c.Bias + c.Kinds[kind]
	for i := range min(len(vec), len(c.Weights)) {
		z += c.Weights[i] * vec[i]
	}
	return 1 / (1 + math.Exp(-z))
}

// trainReviewClassifier runs class-weighted, L2-regularized gradient descent: rejections are rare,
// so each one counts as much as the approvals combined.
func trainReviewClassifier(ex []reviewExample, space string) *ReviewClassifier {
	c := &ReviewClassifier{Space: space, Kinds: map[string]float64{}, Examples: len(ex)}
	if len(ex) == 0 {
		return c
	}
	c.Weights = make([]float64, len(ex[0].vec))
	for _, e := range ex {
		if e.rejected {
			c.Rejected++
		}
		c.Kinds[e.kind] += 0
	}
	if c.Rejected == 0 || c.Rejected == len(ex) {
		return c
	}
	n := float64(len(ex))
	posW, negW := n/(2*float64(c.Rejected)), n/(2*float64(len(ex)-c.Rejected))
	const rate, lambda = 0.5, 1e-2
	gw, gk := make([]float64, len(c.Weights)), map[string]float64{}
	for range 3000 {
		clear(gw)
		clear(gk)
		gb := 0.0
		for _, e := range ex {
			y, w := 0.0, negW
			if e.rejected {
				y, w = 1, posW
			}
			g := w * (c.RejectScore(e.vec, e.kind) - y)
			for i, v := range e.vec[:min(len(e.vec), len(gw))] {
				gw[i] += g * v
			}
			gk[e.kind] += g
			gb += g
		}
		for i := range c.Weights {
			c.Weights[i] -= rate * (gw[i]/n + lambda*c.Weights[i])
		}
		for k := range c.Kinds {
			c.Kinds[k] -= rate * (gk[k]/n + lambda*c.Kinds[k])
		}
		c.Bias -= rate * gb / n
	}
	return c
}

// crossValidate trains on four fifths and scores the held-out fifth, five times.
func crossValidate(ex []reviewExample) ReviewClassifierReport {
	rep := ReviewClassifierReport{Examples: len(ex)}
	order := rand.New(rand.NewSource(1)).Perm(len(ex)) // math/rand: fixed seed keeps the folds reproducible; nothing secret
	scores := make([]float64, len(ex))
	for fold := range 5 {
		var train []reviewExample
		for pos, i := range order {
			if pos%5 != fold {
				train = append(train, ex[i])
			}
		}
		m := trainReviewClassifier(train, "")
		for pos, i := range order {
			if pos%5 == fold {
				scores[i] = m.RejectScore(ex[i].vec, ex[i].kind)
			}
		}
	}
	var pos, neg []float64
	for i, e := range ex {
		if e.rejected {
			rep.Rejected++
			pos = append(pos, scores[i])
			if scores[i] >= 0.5 {
				rep.RejectsFound++
			}
		} else {
			neg = append(neg, scores[i])
			if scores[i] >= 0.5 {
				rep.FalseRejects++
			}
		}
	}
	rep.AUC = auc(pos, neg)
	rep.MinimumReached = rep.Rejected >= reviewClassifierMinRejected
	return rep
}

func auc(pos, neg []float64) float64 {
	if len(pos) == 0 || len(neg) == 0 {
		return 0
	}
	sort.Float64s(neg)
	sum := 0.0
	for _, p := range pos {
		lo, _ := slices.BinarySearch(neg, p)
		hi := lo
		for hi < len(neg) && neg[hi] == p {
			hi++
		}
		sum += float64(lo) + float64(hi-lo)/2
	}
	return sum / float64(len(pos)*len(neg))
}

// TrainReviewClassifier reads every decided candidate in scope, drops decisions made by excluded
// principals (the AI reviewer: it should learn the person, not itself), embeds the texts and trains.
func (s *Service) TrainReviewClassifier(ctx context.Context, token string, scope Scope, exclude []string) (*ReviewClassifier, ReviewClassifierReport, error) {
	if s.Embedder == nil {
		return nil, ReviewClassifierReport{}, ErrUnavailable
	}
	actor, err := s.authorize(ctx, token, reviewAction, scope, "", "")
	if err != nil {
		return nil, ReviewClassifierReport{}, err
	}
	ctx = bindRequestActor(ctx, actor)
	rows, err := s.reviewTrainingRows(ctx, token, scope, exclude)
	if err != nil {
		return nil, ReviewClassifierReport{}, err
	}
	var ex []reviewExample
	for start := 0; start < len(rows); start += 32 {
		batch := rows[start:min(start+32, len(rows))]
		if err := s.validateReviewTrainingBatch(ctx, token, scope, actor, batch); err != nil {
			return nil, ReviewClassifierReport{}, err
		}
		texts := make([]string, len(batch))
		for i, r := range batch {
			texts[i] = r.Text
		}
		vecs, err := s.embedReviewTexts(ctx, scope, texts)
		if err != nil || len(vecs) != len(batch) {
			return nil, ReviewClassifierReport{}, ErrUnavailable
		}
		for i, r := range batch {
			ex = append(ex, reviewExample{vec: vecs[i].Vector, kind: r.Kind, rejected: r.Status == "rejected"})
		}
	}
	rep := crossValidate(ex)
	c := trainReviewClassifier(ex, s.Embedder.Space())
	c.Scope = scope
	c.TrainedAt = time.Now().UTC()
	return c, rep, nil
}

// Collection may take several pages. Re-read before each external batch so
// revocation, deletion or content replacement during collection cannot be sent.
func (s *Service) validateReviewTrainingBatch(ctx context.Context, token string, scope Scope, actor Decision, rows []Record) error {
	for _, row := range rows {
		current, err := s.Repo.Get(ctx, "memory_candidates", scope, row.ID)
		if err != nil {
			return err
		}
		if current.Revision != row.Revision || current.Text != row.Text || current.Status != row.Status || current.Scope != scope || current.PrivacyQuarantined || current.ContentErased || current.Sensitivity == "restricted" {
			return ErrConflict
		}
	}
	if !s.reviewTransferAllowed(ctx, token, scope, actor, rows) {
		return ErrDenied
	}
	return nil
}

// scoreCandidates fills RejectScore on a page of pending candidates and puts the likeliest rejects
// first. It only advises: any embedding problem leaves the page as it was.
func (s *Service) scoreCandidates(ctx context.Context, token string, scope Scope, actor Decision, rows []Record) {
	c := s.ReviewClassifier
	if c == nil || s.Embedder == nil || s.Embedder.Space() != c.Space || c.Scope != scope || len(rows) == 0 || !actor.ExternalTransfer || !actor.ContentSafe {
		return
	}
	if !s.reviewTransferAllowed(ctx, token, scope, actor, rows) {
		return
	}
	keys := make([]reviewScoreKey, len(rows))
	model := ContentHash(structuredText(c))
	for i, r := range rows {
		if r.Scope != scope || r.PrivacyQuarantined || r.ContentErased || r.Sensitivity == "restricted" || !safeTransfer(r.Text) {
			return
		}
		keys[i] = reviewScoreKey{Scope: scope, Principal: actor.PrincipalID, Role: actor.Role, Model: model, Space: c.Space, ID: r.ID, Kind: r.Kind, Content: ContentHash(r.Text), Revision: r.Revision}
	}
	plan := s.reviewScores.plan(keys, time.Now())
	if plan == nil {
		return
	}
	defer s.reviewScores.complete(plan.owned, nil)
	computed := s.computeReviewScores(ctx, scope, c, rows, plan)
	s.reviewScores.complete(plan.owned, computed)
	scores, ok := plan.results(ctx, computed)
	if !ok {
		return
	}
	for i, score := range scores {
		value := score
		rows[i].RejectScore = &value
	}
	sort.SliceStable(rows, func(i, j int) bool { return *rows[i].RejectScore > *rows[j].RejectScore })
}
func (s *Service) computeReviewScores(ctx context.Context, scope Scope, c *ReviewClassifier, rows []Record, plan *reviewScorePlan) map[reviewScoreKey]float64 {
	if len(plan.owned) == 0 {
		return nil
	}
	texts := []string{}
	keys := []reviewScoreKey{}
	for i, key := range plan.keys {
		if _, ok := plan.owned[key]; ok {
			texts = append(texts, rows[i].Text)
			keys = append(keys, key)
		}
	}
	items, err := s.embedReviewTexts(ctx, scope, texts)
	if err != nil || len(items) != len(keys) {
		return nil
	}

	scores := map[reviewScoreKey]float64{}
	for i, key := range keys {
		vector := items[i].Vector
		if len(vector) != len(c.Weights) {
			return nil
		}
		score := c.RejectScore(vector, key.Kind)
		if math.IsNaN(score) || math.IsInf(score, 0) {
			return nil
		}
		scores[key] = score
	}
	return scores
}

// External permission is destination-dependent. A successful internal review
// check alone does not establish permission to send even one candidate out.
func (s *Service) reviewTransferAllowed(ctx context.Context, token string, scope Scope, actor Decision, rows []Record) bool {
	transfer := func(id, text string) bool {
		d, err := s.authorizeAt(ctx, token, reviewAction, scope, id, text, "external:embedding")
		return err == nil && d.PrincipalID == actor.PrincipalID && d.Role == actor.Role && d.Scope == actor.Scope && d.ExternalTransfer && d.ContentSafe
	}
	if !transfer("", "") {
		return false
	}
	for _, row := range rows {
		if row.Scope != scope || row.PrivacyQuarantined || row.ContentErased || row.Sensitivity == "restricted" || !safeTransfer(row.Text) || !transfer(row.ID, structuredText(row)) {
			return false
		}
	}
	return true
}

func (s *Service) reviewTrainingRows(ctx context.Context, token string, scope Scope, exclude []string) ([]Record, error) {
	actor, err := s.authorize(ctx, token, reviewAction, scope, "", "")
	if err != nil {
		return nil, err
	}
	ctx = bindRequestActor(ctx, actor)
	rows := []Record{}
	for _, status := range []string{"approved", "rejected"} {
		in := ReviewInput{omitAdvice: true, Scope: scope, Status: status, Limit: 100}
		for {
			page, err := s.ListCandidates(ctx, token, in)
			if err != nil {
				return nil, err
			}
			eligible := slices.DeleteFunc(page.Items, func(r Record) bool {
				by := r.ApprovedBy
				if r.Review != nil && r.Review.By != "" {
					by = r.Review.By
				}
				return slices.Contains(exclude, by) || r.Text == "" || r.ContentErased || r.PrivacyQuarantined || r.Sensitivity == "restricted" || !safeTransfer(r.Text)
			})
			if !s.reviewTransferAllowed(ctx, token, scope, actor, eligible) {
				return nil, ErrDenied
			}
			rows = append(rows, eligible...)
			if page.NextCursor == "" {
				break
			}
			if page.NextCursor == in.Cursor {
				return nil, ErrUnavailable
			}
			in.Cursor = page.NextCursor
		}
	}
	return rows, nil
}

func (s *Service) embedReviewTexts(ctx context.Context, scope Scope, texts []string) ([]Embedding, error) {
	reserved, err := s.beginProviderCall(ctx, scope, "embedding_review", embeddingProvider(s.Embedder), s.Embedder.Space())
	if err != nil {
		return nil, err
	}
	batch, err := embedMeasured(ctx, s.Embedder, texts, "document")
	if err != nil || len(batch.Items) != len(texts) {
		_ = s.finishProviderCall(ctx, reserved, ProviderUsage{Provider: embeddingProvider(s.Embedder), Model: s.Embedder.Space(), Requests: 1}, false)
		return nil, ErrUnavailable
	}
	if err := s.finishProviderCall(ctx, reserved, batch.Usage, true); err != nil {
		return nil, err
	}
	return batch.Items, nil
}
