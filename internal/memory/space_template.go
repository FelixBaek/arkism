package memory

// Space templates beyond import: the JSON Schema of a template bundle line
// (GET /v1/spaces/template-schema) and the opt-in automatic apply of the
// server's default template (ARKISM_SPACE_TEMPLATE_AUTO=1).
//
// Automatic apply reuses the import path, so it follows the same rules: only a
// person's request starts it (directives and mental model definitions are
// human-written), the settings land only in a space that has none, and the run
// is an import job the person can read like any other. The first settings a
// person stores win over the template's: then only its definitions are added.
// No person chose these definitions for this space, so they arrive switched off:
// directives inactive and mental models refreshed only on request, until a person
// turns them on.

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"slices"
)

// SpaceTemplateSchema describes one line of a template bundle (JSON Lines).
func SpaceTemplateSchema() map[string]any {
	out := jsonSchemaOf(reflect.TypeOf(spaceBundleLine{}), false)
	out["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	out["title"] = "Arkism space template bundle line"
	out["description"] = "A template bundle is JSON Lines. The first line is {manifest} with format " + spaceBundleFormat +
		", format_version and template true; sha256 is the hex SHA-256 of every line after it. Each other line is {section, config} for section config, or {section, record} for the definition sections " +
		"directives (record.directive) and mental_models (record.mental_model). A template never carries memories."
	out["x-format"], out["x-format-version"], out["x-sections"] = spaceBundleFormat, spaceBundleVersion, append([]string{spaceSectionConfig}, spaceDefinitions...)
	return out
}

// autoApplySpaceTemplate imports the default template into scope once a person
// gave the space its first settings (definitionsOnly) or captured into a space
// without settings. Failures are logged; the person's own request still stands.
func (s *Service) autoApplySpaceTemplate(ctx context.Context, token string, d Decision, scope Scope, definitionsOnly bool) {
	t := s.SpaceTemplate
	if t == nil || !t.Auto || d.Role != roleHuman || !validScope(scope) {
		return
	}
	scope = spaceScope(scope)
	if _, done := t.applied.LoadOrStore(scope, true); done {
		return
	}
	manifest, lines, err := parseSpaceBundle(t.Bundle)
	if err != nil {
		return
	}
	dormantDefinitions(lines)
	isConfig := func(l spaceBundleLine) bool { return l.Section == spaceSectionConfig }
	if definitionsOnly {
		lines = slices.DeleteFunc(lines, isConfig)
	} else {
		if _, err := s.Repo.Get(ctx, colSpaceConfigs, scope, spaceConfigID); !errors.Is(err, ErrNotFound) {
			return
		}
		if !slices.ContainsFunc(lines, isConfig) { // stored settings mark the space as set up
			config := t.Config
			lines = append([]spaceBundleLine{{Section: spaceSectionConfig, Config: &config}}, lines...)
		}
	}
	job := SpaceJob{Kind: spaceJobImport, Template: true, BundleID: manifest.BundleID, Manifest: &manifest}
	if _, err = s.startSpaceJob(ctx, scope, d, job, func(ctx context.Context, job *SpaceJob) error {
		report, err := s.importSpaceLines(ctx, token, d, scope, manifest, sliceLines(lines), false)
		job.Report = &report
		return err
	}); err != nil {
		t.applied.Delete(scope)
		slog.Warn("space template not applied", "workspace", scope.WorkspaceID, "project", scope.ProjectID, "error", captureErrorCode(err))
	}
}

// dormantDefinitions switches the template's directives off and its mental models to
// manual refresh, so nothing the template defines acts or spends model budget before
// a person enables it in this space.
func dormantDefinitions(lines []spaceBundleLine) {
	for _, l := range lines {
		switch {
		case l.Record == nil:
		case l.Section == colDirectives:
			l.Record.Status = "inactive"
		case l.Section == colMentalModels && l.Record.MentalModel != nil:
			r := &l.Record.MentalModel.Refresh
			r.Mode, r.Cron, r.AfterConsolidation = mmRefreshManual, "", false
		}
	}
}

// capturedAny reports whether a capture stored at least one candidate.
func capturedAny(out CaptureResult) bool {
	return slices.ContainsFunc(out.Items, func(i CaptureItem) bool { return i.Status == "candidate" })
}
