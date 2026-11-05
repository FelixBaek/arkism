package memory

// Markdown export: every servable reviewed page as <folder>/<slug>.md with a
// front matter block, packed as tar (default) or zip from the standard
// library. Drafts are never exported. The archive is built in memory and
// refused past wikiExportMaxBytes of page text.

import (
	"context"
	"encoding/json"
	"path"
	"sort"
	"strings"
	"time"
)

const wikiExportMaxBytes = 32 << 20

type WikiExport struct {
	Data        []byte
	ContentType string
	Filename    string
}

// ExportWiki packs the scope's servable pages; format is "tar" or "zip".
func (s *Service) ExportWiki(ctx context.Context, token string, scope Scope, format string) (WikiExport, error) {
	if format == "" {
		format = "tar"
	}
	if format != "tar" && format != "zip" {
		return WikiExport{}, ErrInvalid
	}
	if _, err := s.wikiAuthorize(ctx, token, "read:"+colWikiPages, scope, "", ""); err != nil {
		return WikiExport{}, err
	}
	pages, _, err := s.wikiServableGraph(ctx, scope)
	if err != nil {
		return WikiExport{}, err
	}
	files := make([]archiveFile, 0, len(pages))
	total := 0
	for _, r := range pages {
		f := wikiMarkdownFile(*r)
		if total += len(f.body); total > wikiExportMaxBytes {
			return WikiExport{}, errBlobTooLarge
		}
		files = append(files, f)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	pack, contentType := tarArchive, "application/x-tar"
	if format == "zip" {
		pack, contentType = zipArchive, "application/zip"
	}
	data, err := pack(files)
	if err != nil {
		return WikiExport{}, err
	}
	return WikiExport{Data: data, ContentType: contentType, Filename: "wiki." + format}, nil
}

// wikiMarkdownFile renders one page; front matter values are JSON strings and
// arrays, which are valid YAML flow values.
func wikiMarkdownFile(r Record) archiveFile {
	p := r.Wiki.Page
	body := p.Reviewed
	quote := func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}
	citations := append([]string{}, body.Citations...)
	if len(citations) == 0 {
		citations = append(citations, p.MemoryIDs...)
	}
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("title: " + quote(body.Title) + "\n")
	b.WriteString("id: " + quote(r.ID) + "\n")
	b.WriteString("type: " + quote(p.Type) + "\n")
	b.WriteString("citations: " + quote(citations) + "\n")
	b.WriteString("knowledge_source_ids: " + quote(append([]string{}, p.SourceIDs...)) + "\n")
	b.WriteString("updated_at: " + quote(r.UpdatedAt.UTC().Format(time.RFC3339)) + "\n")
	b.WriteString("---\n\n")
	b.WriteString(body.Text)
	b.WriteString("\n")
	// Folder segments and slugs are validated names, so the joined path stays inside the archive root.
	return archiveFile{name: path.Join(p.Folder, p.Slug+".md"), body: []byte(b.String()), at: r.UpdatedAt.UTC()}
}
