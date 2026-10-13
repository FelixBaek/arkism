package memory

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Image prompts: the operator's library of prompts, variables, sample images
// and scripts (a ComfyUI workflow) for images and slides. One record per
// prompt; archiving hides it from the list without deleting it.
const colPrompts = "prompts"

var promptKinds = []string{"image", "slide"}

// promptEngines is the model the prompt is written for. comfyui_api and codex
// are older values kept so existing records still save.
var promptEngines = []string{"gemini", "gpt", "claude", "grok", "comfyui", "other", "comfyui_api", "codex"}

// Sample or reference images ride inside the record as small data URLs; the
// desktop shrinks them before sending.
// shortcut: images live in the record and every list returns them; move them to the blob store if the library grows past a few hundred.
const maxPromptImages, maxPromptImageBytes = 4, 512 << 10

var promptImage = regexp.MustCompile(`^data:image/(?:png|jpeg|webp|gif);base64,[A-Za-z0-9+/]+={0,2}$`)

// maxPromptBody fits four images plus the longest prompt and script.
const maxPromptBody = maxPromptImages*maxPromptImageBytes + 256<<10

type ImagePrompt struct {
	Title      string   `json:"title"`
	Kind       string   `json:"kind"`
	Engine     string   `json:"engine"`
	Prompt     string   `json:"prompt"`
	Negative   string   `json:"negative,omitempty"`
	Size       string   `json:"size,omitempty"`
	Count      int      `json:"count,omitempty"`
	Seed       string   `json:"seed,omitempty"`
	ScriptName string   `json:"script_name,omitempty"`
	Script     string   `json:"script,omitempty"`
	Tags       []string `json:"tags,omitempty"` // 카테고리
	Experts    []string `json:"experts,omitempty"`
	Images     []string `json:"images,omitempty"`
}

type PromptInput struct {
	Scope  Scope       `json:"scope"`
	Prompt ImagePrompt `json:"prompt"`
}

type PromptUpdate struct {
	Scope            Scope        `json:"scope"`
	ExpectedRevision int64        `json:"expected_revision"`
	Prompt           *ImagePrompt `json:"prompt,omitempty"`
	Archive          bool         `json:"archive,omitempty"`
}

type PromptPage struct {
	Items []Record `json:"items"`
}

func shortList(items []string, n, size int) bool {
	if len(items) > n {
		return false
	}
	for _, s := range items {
		if strings.TrimSpace(s) == "" || utf8.RuneCountInString(s) > size {
			return false
		}
	}
	return true
}

func validImagePrompt(p ImagePrompt) bool {
	title := utf8.RuneCountInString(strings.TrimSpace(p.Title))
	return title > 0 && title <= 120 && slices.Contains(promptKinds, p.Kind) && slices.Contains(promptEngines, p.Engine) &&
		strings.TrimSpace(p.Prompt) != "" && utf8.RuneCountInString(p.Prompt) <= 8000 && utf8.RuneCountInString(p.Negative) <= 2000 &&
		len(p.Size) <= 32 && p.Count >= 0 && p.Count <= 16 && len(p.Seed) <= 32 && utf8.RuneCountInString(p.ScriptName) <= 128 &&
		len(p.Script) <= 60000 && shortList(p.Tags, 16, 40) && shortList(p.Experts, 16, 60) && validPromptImages(p.Images)
}

func validPromptImages(images []string) bool {
	if len(images) > maxPromptImages {
		return false
	}
	for _, s := range images {
		if len(s) > maxPromptImageBytes || !promptImage.MatchString(s) {
			return false
		}
	}
	return true
}

// promptSecrets scans everything but the images: base64 can spell a key
// prefix like AIza by chance.
func promptSecrets(p ImagePrompt) error {
	p.Images = nil
	return noSecrets(p)
}

func (s *Service) ListPrompts(ctx context.Context, token string, scope Scope) (PromptPage, error) {
	if !validScope(scope) {
		return PromptPage{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colPrompts, scope, "", ""); err != nil {
		return PromptPage{}, err
	}
	// shortcut: one page of 500; page with a cursor if the library outgrows it.
	rows, err := s.Repo.ListRecords(ctx, scope, colPrompts, 500)
	if err != nil {
		return PromptPage{}, err
	}
	out := PromptPage{Items: []Record{}}
	for _, r := range rows {
		if r.Status == statusActive && r.ImagePrompt != nil {
			out.Items = append(out.Items, r)
		}
	}
	return out, nil
}

func (s *Service) CreatePrompt(ctx context.Context, token string, in PromptInput) (Record, error) {
	if !validScope(in.Scope) || !validImagePrompt(in.Prompt) {
		return Record{}, ErrInvalid
	}
	if err := promptSecrets(in.Prompt); err != nil {
		return Record{}, err
	}
	d, err := s.authorize(ctx, token, "write:"+colPrompts, in.Scope, "", "")
	if err != nil {
		return Record{}, err
	}
	if d.Role != roleHuman {
		return Record{}, ErrDenied
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	p := in.Prompt
	r := Record{ID: ID(), Scope: in.Scope, Kind: colPrompts, Status: statusActive, Revision: 1, Text: p.Title, ImagePrompt: &p,
		CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if err := tx.Insert(ctx, colPrompts, r); err != nil {
			return err
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "create:"+colPrompts, r.ID)
	})
	return r, err
}

// UpdatePrompt replaces the prompt's fields, or archives it.
func (s *Service) UpdatePrompt(ctx context.Context, token, id string, in PromptUpdate) (Record, error) {
	if !validScope(in.Scope) || !validRunLabel(id, 256) || in.ExpectedRevision < 1 || (in.Prompt == nil) == !in.Archive || in.Prompt != nil && !validImagePrompt(*in.Prompt) {
		return Record{}, ErrInvalid
	}
	if in.Prompt != nil {
		if err := promptSecrets(*in.Prompt); err != nil {
			return Record{}, err
		}
	}
	d, err := s.authorize(ctx, token, "write:"+colPrompts, in.Scope, id, "")
	if err != nil {
		return Record{}, err
	}
	if d.Role != roleHuman {
		return Record{}, ErrDenied
	}
	var out Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, err := tx.Get(ctx, colPrompts, in.Scope, id)
		if err != nil {
			return err
		}
		if r.Revision != in.ExpectedRevision || r.Status != statusActive {
			return ErrConflict
		}
		prev := r.Revision
		if in.Archive {
			r.Status = "archived"
		} else {
			p := *in.Prompt
			r.ImagePrompt, r.Text = &p, p.Title
		}
		r.Revision++
		r.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
		if err := tx.Replace(ctx, colPrompts, r, prev); err != nil {
			return err
		}
		out = r
		action := "update:" + colPrompts
		if in.Archive {
			action = "archive:" + colPrompts
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, action, id)
	})
	return out, err
}
