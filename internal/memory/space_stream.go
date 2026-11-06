package memory

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"time"
)

// Streamed space bundles (format version 2). The manifest line comes first,
// then one line per config, record or history row, then a trailer line
// {"end": {sha256, sections, skipped}} whose sha256 covers every line between
// the two. A cut transfer has no trailer and is refused. Writing pages the
// store; reading checks one line at a time; neither holds the bundle in memory.
// Version 1 bundles (sha256 and counts in the manifest) still import.

const (
	spaceBundleVersionV1 = 1
	// spaceStreamMaxBytes caps an uploaded (spooled) bundle; spaceLineMaxBytes one line of it.
	spaceStreamMaxBytes = 1 << 30
	spaceLineMaxBytes   = 4 << 20
	spaceHistoryPage    = 500
)

var errSpaceLineTooLarge = errors.New("space bundle line too large")

// SpaceBundleEnd is the trailer of a version 2 bundle.
type SpaceBundleEnd struct {
	SHA256   string         `json:"sha256"`
	Sections []SpaceSection `json:"sections"`
	Skipped  []SpaceSkip    `json:"skipped"`
}

// spaceLines hands bundle lines to yield in order and stops at its first error.
type spaceLines func(yield func(spaceBundleLine) error) error

func sliceLines(lines []spaceBundleLine) spaceLines {
	return func(yield func(spaceBundleLine) error) error {
		for _, l := range lines {
			if err := yield(l); err != nil {
				return err
			}
		}
		return nil
	}
}

// skip counts one line left out, merged per section and reason so a long stream keeps the report small.
func (r *SpaceReport) skip(section, reason string, n int) {
	for i := range r.Skipped {
		if r.Skipped[i].Section == section && r.Skipped[i].Reason == reason {
			r.Skipped[i].Count += n
			return
		}
	}
	r.Skipped = append(r.Skipped, SpaceSkip{Section: section, Reason: reason, Count: n})
}

// writeSpaceBundle streams the bundle of one space to w; it reads only that exact scope.
// d is the reader's decision: settings are the ones that role may see; history,
// knowledge files and attachments only a person gets.
func (s *Service) writeSpaceBundle(ctx context.Context, token string, d Decision, scope Scope, in SpaceExportInput, bundleID string, at time.Time, w io.Writer) (SpaceManifest, error) {
	m := SpaceManifest{Format: spaceBundleFormat, FormatVersion: spaceBundleVersion, BundleID: bundleID, Template: in.Template, Source: scope, CreatedAt: at, Sections: []SpaceSection{}, Skipped: []SpaceSkip{}}
	bw := bufio.NewWriter(w)
	if err := json.NewEncoder(bw).Encode(spaceBundleLine{Manifest: &m}); err != nil {
		return SpaceManifest{}, ErrUnavailable
	}
	h := sha256.New()
	out := io.MultiWriter(bw, h)
	sections, skipped, err := s.eachSpaceLine(ctx, token, d, scope, spaceSections(in), in.DocumentIDs, at, func(l spaceBundleLine) error {
		b, err := json.Marshal(l)
		switch {
		case err != nil:
			return ErrUnavailable
		case len(b) >= spaceLineMaxBytes:
			// A reader refuses a longer line, so it is left out and reported rather than written.
			return errSpaceLineTooLarge
		}
		if _, err = out.Write(append(b, '\n')); err != nil {
			return ErrUnavailable
		}
		return nil
	})
	if err != nil {
		return SpaceManifest{}, err
	}
	m.Sections, m.Skipped, m.SHA256 = sections, skipped, hex.EncodeToString(h.Sum(nil))
	if json.NewEncoder(bw).Encode(struct {
		End SpaceBundleEnd `json:"end"`
	}{SpaceBundleEnd{m.SHA256, m.Sections, m.Skipped}}) != nil || bw.Flush() != nil {
		return SpaceManifest{}, ErrUnavailable
	}
	return m, nil
}

// eachSpaceLine emits the lines of every section in order and returns the per-section counts and skips.
// docs, when set, narrows the documents section to those documents.
func (s *Service) eachSpaceLine(ctx context.Context, token string, d Decision, scope Scope, sections, docs []string, now time.Time, emit func(spaceBundleLine) error) ([]SpaceSection, []SpaceSkip, error) {
	counts := []SpaceSection{}
	report := SpaceReport{Skipped: []SpaceSkip{}}
	for _, name := range sections {
		n := 0
		count := func(l spaceBundleLine) error {
			err := emit(l)
			if errors.Is(err, errSpaceLineTooLarge) {
				report.skip(name, "too_large", 1)
				return nil
			}
			if err == nil {
				n++
			}
			return err
		}
		if err := s.emitSpaceSection(ctx, token, d, scope, name, docs, now, count, &report); err != nil {
			return nil, nil, err
		}
		counts = append(counts, SpaceSection{Name: name, Count: n})
	}
	return counts, report.Skipped, nil
}

func (s *Service) emitSpaceSection(ctx context.Context, token string, d Decision, scope Scope, name string, docs []string, now time.Time, emit func(spaceBundleLine) error, report *SpaceReport) error {
	repo := s.Repo
	switch {
	case name == spaceSectionConfig:
		return emitSpaceConfig(ctx, repo, scope, d.Role, emit)
	case (slices.Contains(spaceHistory, name) || slices.Contains(spaceFiles, name) || name == spaceSectionDocuments) && d.Role != roleHuman:
		report.skip(name, "not_human", 1)
		return nil
	case slices.Contains(spaceFiles, name):
		return s.emitSpaceFiles(ctx, token, d, scope, name, emit, report)
	case name == spaceSectionDocuments:
		return s.emitSpaceDocuments(ctx, d, scope, docs, emit, report)
	case name == colLLMRequests:
		return emitLLMRequests(ctx, repo, scope, emit)
	case name == "audit_events":
		return eachCatalogRecord(ctx, repo, scope, name, func(r Record) error {
			v := viewAuditEvent(r)
			return emit(spaceBundleLine{Section: name, Audit: &v})
		})
	case !slices.Contains(spaceExportable, name):
		report.skip(name, "not_exportable", 1)
		return nil
	case !slices.Contains(Collections, name):
		report.skip(name, "unknown_collection", 1)
		return nil
	}
	return eachCatalogRecord(ctx, repo, scope, name, func(r Record) error {
		if why := spaceRecordSkip(name, r, now); why != "" {
			report.skip(name, why, 1)
			return nil
		}
		return emit(spaceBundleLine{Section: name, Record: &SpaceBundleRecord{ID: r.ID, Kind: r.Kind, Status: r.Status,
			Approved: name == colMemories, Text: r.Text, Summary: r.Summary, Keywords: r.Keywords, Tags: r.Tags,
			MemoryContext: copyMemoryContext(r.MemoryContext), Directive: r.Directive, MentalModel: mentalModelDefinition(r), CreatedAt: r.CreatedAt}})
	})
}

func emitSpaceConfig(ctx context.Context, repo Repository, scope Scope, role string, emit func(spaceBundleLine) error) error {
	rec, err := spaceConfigDoc(ctx, repo, scope)
	if errors.Is(err, ErrNotFound) || err == nil && rec.SpaceConfig == nil {
		return nil
	}
	if err != nil {
		return err
	}
	config := shownSpaceConfig(*rec.SpaceConfig, role)
	return emit(spaceBundleLine{Section: spaceSectionConfig, Config: &config})
}

// emitLLMRequests pages the model-call traces newest first. Prompt and answer
// text never leave in a bundle: sizes, hashes, tokens and outcome do.
func emitLLMRequests(ctx context.Context, repo Repository, scope Scope, emit func(spaceBundleLine) error) error {
	store, ok := repo.(scopedRowStore)
	if !ok {
		return ErrUnavailable
	}
	before := ""
	for {
		if ctx.Err() != nil {
			return ErrUnavailable
		}
		rows, err := store.ListScopedRows(ctx, scopedRowQuery{Collection: colLLMRequests, Kind: llmRequestKind, Scope: scope, Before: before, Limit: spaceHistoryPage})
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.LLMRequest == nil {
				continue
			}
			req := *r.LLMRequest
			req.Prompt, req.Response = "", ""
			if err = emit(spaceBundleLine{Section: colLLMRequests, LLMRequest: &LLMRequestView{ID: r.ID, Scope: r.Scope, CreatedAt: r.CreatedAt, Request: req}}); err != nil {
				return err
			}
		}
		if len(rows) < spaceHistoryPage {
			return nil
		}
		before = rows[len(rows)-1].ID
	}
}

// readSpaceBundle checks a bundle line by line (manifest, strict line shapes,
// sha256, trailer) and hands each body line to each. It applies nothing:
// a caller that writes reads the bundle twice, first to verify, then to apply.
func readSpaceBundle(r io.Reader, each func(spaceBundleLine) error) (SpaceManifest, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), spaceLineMaxBytes)
	var first spaceBundleLine
	if !sc.Scan() || json.Unmarshal(sc.Bytes(), &first) != nil || first.Manifest == nil {
		return SpaceManifest{}, ErrInvalid
	}
	m := *first.Manifest
	if m.Format != spaceBundleFormat || m.FormatVersion != spaceBundleVersion && m.FormatVersion != spaceBundleVersionV1 || !validRunLabel(m.BundleID, 64) {
		return SpaceManifest{}, ErrInvalid
	}
	h := sha256.New()
	var end *SpaceBundleEnd
	for n := 0; sc.Scan(); n++ {
		line, err := decodeSpaceLine(sc.Bytes())
		switch {
		case err != nil || end != nil || line.Manifest != nil || m.FormatVersion == spaceBundleVersionV1 && (line.End != nil || n >= spaceBundleMaxRecords):
			return SpaceManifest{}, ErrInvalid
		case line.End != nil:
			end = line.End
			continue
		}
		h.Write(sc.Bytes())
		h.Write([]byte{'\n'})
		if each != nil {
			if err = each(line); err != nil {
				return SpaceManifest{}, err
			}
		}
	}
	return m, finishSpaceRead(sc.Err(), &m, end, hex.EncodeToString(h.Sum(nil)))
}

func decodeSpaceLine(raw []byte) (spaceBundleLine, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var line spaceBundleLine
	if dec.Decode(&line) != nil || dec.More() {
		return line, ErrInvalid
	}
	return line, nil
}

func finishSpaceRead(readErr error, m *SpaceManifest, end *SpaceBundleEnd, sum string) error {
	if readErr != nil {
		return ErrInvalid
	}
	if m.FormatVersion == spaceBundleVersionV1 {
		if m.SHA256 != sum {
			return ErrInvalid
		}
		return nil
	}
	if end == nil || end.SHA256 != sum {
		return ErrInvalid
	}
	m.SHA256, m.Sections, m.Skipped = end.SHA256, []SpaceSection{}, []SpaceSkip{}
	m.Sections, m.Skipped = append(m.Sections, end.Sections...), append(m.Skipped, end.Skipped...)
	return nil
}

// parseSpaceBundle reads a small bundle held as a string (a template or a JSON import body).
func parseSpaceBundle(bundle string) (SpaceManifest, []spaceBundleLine, error) {
	if len(bundle) > spaceBundleMaxBytes {
		return SpaceManifest{}, nil, ErrInvalid
	}
	lines := []spaceBundleLine{}
	m, err := readSpaceBundle(strings.NewReader(bundle), func(l spaceBundleLine) error { lines = append(lines, l); return nil })
	if err != nil {
		return SpaceManifest{}, nil, err
	}
	return m, lines, nil
}

// spaceSource is a bundle that can be read more than once; done releases it.
type spaceSource struct {
	open func() (io.ReadCloser, error)
	done func()
}

func stringSpaceSource(bundle string) spaceSource {
	return spaceSource{open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(bundle)), nil }, done: func() {}}
}

// spoolSpaceBundle copies an uploaded bundle to a private temp file, so memory
// stays bounded and the bundle can be verified before any line is applied.
func spoolSpaceBundle(body io.Reader) (spaceSource, error) {
	f, err := os.CreateTemp("", "arkism-space-*.jsonl")
	if err != nil {
		return spaceSource{}, ErrUnavailable
	}
	name := f.Name()
	n, err := io.Copy(f, io.LimitReader(body, spaceStreamMaxBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		_ = os.Remove(name)
		return spaceSource{}, ErrInvalid
	case n > spaceStreamMaxBytes:
		_ = os.Remove(name)
		return spaceSource{}, errBlobTooLarge
	}
	return spaceSource{open: func() (io.ReadCloser, error) { return os.Open(name) }, done: func() { _ = os.Remove(name) }}, nil
}

// verify reads the whole source once and returns its manifest.
func (src spaceSource) verify() (SpaceManifest, error) {
	rc, err := src.open()
	if err != nil {
		return SpaceManifest{}, ErrUnavailable
	}
	defer rc.Close()
	return readSpaceBundle(rc, nil)
}

func (src spaceSource) lines() spaceLines {
	return func(yield func(spaceBundleLine) error) error {
		rc, err := src.open()
		if err != nil {
			return ErrUnavailable
		}
		defer rc.Close()
		_, err = readSpaceBundle(rc, yield)
		return err
	}
}

// spaceImporter applies bundle lines to one space. Memory-like records go
// through Capture in chunks of spaceImportChunk claims, each chunk one event
// ("space-import:<bundle>:<n>"), so a re-import stores nothing twice.
type spaceImporter struct {
	s        *Service
	token    string
	d        Decision
	scope    Scope
	m        SpaceManifest
	approved bool
	// template keeps only settings and definitions; history accepts history rows (a clone inside the server).
	template, history bool
	conflict          string // document conflict policy (validDocConflict)
	claims            []CaptureClaim
	origin            []bool
	chunk             int
	report            SpaceReport
	// piece is a split attachment still waiting for its next part.
	piece *spacePiece
}

func (s *Service) importSpaceLines(ctx context.Context, token string, d Decision, scope Scope, m SpaceManifest, lines spaceLines, approved bool) (SpaceReport, error) {
	return s.runSpaceImport(ctx, &spaceImporter{s: s, token: token, d: d, scope: scope, m: m, approved: approved}, lines)
}

func (s *Service) runSpaceImport(ctx context.Context, imp *spaceImporter, lines spaceLines) (SpaceReport, error) {
	imp.report.Skipped = []SpaceSkip{}
	err := lines(func(l spaceBundleLine) error { return imp.add(ctx, l) })
	imp.dropPiece(spaceSectionAttachments)
	if err == nil {
		err = imp.flush(ctx)
	}
	return imp.report, err
}

func (imp *spaceImporter) add(ctx context.Context, line spaceBundleLine) error {
	defs := slices.Contains(spaceDefinitions, line.Section) && slices.Contains(Collections, line.Section)
	switch {
	case line.Section == spaceSectionConfig && line.Config != nil:
		imp.report.Config = imp.s.importSpaceConfig(ctx, imp.d, imp.scope, *line.Config)
	case imp.template && !(defs && line.Record != nil):
		imp.report.skip(line.Section, "template_only", 1)
	case line.Record != nil && slices.Contains(spaceCaptured, line.Section):
		imp.claims = append(imp.claims, spaceClaim(imp.m.BundleID, line.Section, *line.Record))
		imp.origin = append(imp.origin, line.Record.Approved)
		if len(imp.claims) >= spaceImportChunk {
			return imp.flush(ctx)
		}
	case line.Record != nil && defs:
		imp.s.importSpaceDefinition(ctx, imp.token, imp.d.Role, imp.scope, line, &imp.report)
	case line.File != nil && slices.Contains(spaceFiles, line.Section):
		imp.addFile(ctx, line.Section, *line.File)
	case line.Document != nil && line.Section == spaceSectionDocuments:
		imp.addDocument(ctx, *line.Document)
	case (line.Audit != nil || line.LLMRequest != nil) && slices.Contains(spaceHistory, line.Section):
		imp.addHistory(ctx, line)
	default:
		imp.report.skip(line.Section, "unknown_section", 1)
	}
	return nil
}

func (imp *spaceImporter) flush(ctx context.Context) error {
	if len(imp.claims) == 0 {
		return nil
	}
	err := imp.s.importSpaceChunk(ctx, imp.token, imp.scope, imp.m, imp.chunk, imp.claims, imp.origin, imp.approved, &imp.report)
	imp.chunk++
	imp.claims, imp.origin = imp.claims[:0], imp.origin[:0]
	return err
}

// addHistory copies an audit row or model-call trace of a cloned space. An
// uploaded bundle's history is never written: its rows would be forgeable,
// so it is an archive to read, not to replay.
func (imp *spaceImporter) addHistory(ctx context.Context, line spaceBundleLine) {
	if !imp.history || imp.d.Role != roleHuman {
		imp.report.skip(line.Section, "history_archive_only", 1)
		return
	}
	now := time.Now().UTC()
	r := Record{ID: ID(), Scope: imp.scope, Status: "active", Revision: 1, UpdatedAt: now, CreatedBy: imp.d.PrincipalID, CreatedByType: imp.d.Role}
	if a := line.Audit; a != nil {
		r.Kind, r.CreatedAt, r.Target = "cloned:"+a.Action, a.CreatedAt, a.Target
		r.Audit = &AuditMetadata{RequestID: a.RequestID, Decision: a.Decision, ReasonCode: a.ReasonCode, Reason: "cloned from " + a.Actor}
	} else {
		req := line.LLMRequest.Request
		r.ID, r.Kind, r.Status, r.CreatedAt, r.LLMRequest = timeOrderedID(line.LLMRequest.CreatedAt.UnixMilli()), llmRequestKind, req.Status, line.LLMRequest.CreatedAt, &req
	}
	if err := imp.s.Repo.Insert(ctx, line.Section, r); err != nil {
		imp.report.skip(line.Section, captureErrorCode(err), 1)
		return
	}
	imp.report.Imported++
}
