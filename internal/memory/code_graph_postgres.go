package memory

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

// Code graph tables. They carry organization_id/project_id like the trace
// tables, so InitRLS gives them the same per-project policies.
var codeGraphTables = []string{colCodeRepos, colCodeSymbols, colCodeEdges}

func (p *Postgres) initCodeGraphSchema(ctx context.Context) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS code_repos (
organization_id text NOT NULL, project_id text NOT NULL, repo text NOT NULL,
commit_sha text NOT NULL DEFAULT '', indexed_at timestamptz, updated_at timestamptz NOT NULL, revision bigint NOT NULL,
stats jsonb NOT NULL DEFAULT '{}'::jsonb, files jsonb NOT NULL DEFAULT '{}'::jsonb,
PRIMARY KEY (organization_id,project_id,repo))`,
		`CREATE TABLE IF NOT EXISTS code_symbols (
organization_id text NOT NULL, project_id text NOT NULL, repo text NOT NULL, id text NOT NULL,
path text NOT NULL, lang text NOT NULL, kind text NOT NULL, name text NOT NULL, qname text NOT NULL,
signature text NOT NULL DEFAULT '', start_line integer NOT NULL, end_line integer NOT NULL, exported boolean NOT NULL DEFAULT false, doc text NOT NULL DEFAULT '',
search tsvector GENERATED ALWAYS AS (to_tsvector('simple'::regconfig,
  name || ' ' || regexp_replace(name, '([a-z0-9])([A-Z])', '\1 \2', 'g') || ' ' || regexp_replace(qname, '[^A-Za-z0-9_]+', ' ', 'g'))) STORED,
PRIMARY KEY (organization_id,project_id,repo,id),
FOREIGN KEY (organization_id,project_id,repo) REFERENCES code_repos(organization_id,project_id,repo) ON DELETE CASCADE)`,
		`CREATE INDEX IF NOT EXISTS code_symbols_path ON code_symbols(organization_id,project_id,repo,path)`,
		`CREATE INDEX IF NOT EXISTS code_symbols_name ON code_symbols(organization_id,project_id,lower(name))`,
		`CREATE INDEX IF NOT EXISTS code_symbols_qname ON code_symbols(organization_id,project_id,qname)`,
		`CREATE INDEX IF NOT EXISTS code_symbols_search ON code_symbols USING gin (search)`,
		`CREATE TABLE IF NOT EXISTS code_edges (
organization_id text NOT NULL, project_id text NOT NULL, repo text NOT NULL,
from_id text NOT NULL, to_id text NOT NULL, kind text NOT NULL, path text NOT NULL, line integer NOT NULL,
PRIMARY KEY (organization_id,project_id,repo,from_id,to_id,kind),
FOREIGN KEY (organization_id,project_id,repo) REFERENCES code_repos(organization_id,project_id,repo) ON DELETE CASCADE)`,
		`CREATE INDEX IF NOT EXISTS code_edges_to ON code_edges(organization_id,project_id,repo,to_id,kind)`,
		`CREATE INDEX IF NOT EXISTS code_edges_path ON code_edges(organization_id,project_id,repo,path)`,
	} {
		if _, err := p.q().Exec(ctx, stmt); err != nil {
			return pgErr(err)
		}
	}
	return nil
}

const codeRepoColumns = `repo,commit_sha,indexed_at,updated_at,revision,stats,
(SELECT count(*) FROM jsonb_object_keys(files)),
(SELECT count(*) FROM code_symbols s WHERE s.organization_id=r.organization_id AND s.project_id=r.project_id AND s.repo=r.repo),
(SELECT count(*) FROM code_edges e WHERE e.organization_id=r.organization_id AND e.project_id=r.project_id AND e.repo=r.repo)`

func scanCodeRepo(row pgx.Row) (CodeRepo, error) {
	var r CodeRepo
	var stats []byte
	if err := row.Scan(&r.Repo, &r.Commit, &r.IndexedAt, &r.UpdatedAt, &r.Revision, &stats, &r.Files, &r.Symbols, &r.Edges); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CodeRepo{}, ErrNotFound
		}
		return CodeRepo{}, pgErr(err)
	}
	r.Stats = json.RawMessage(stats)
	return r, nil
}

func (p *Postgres) CodeRepos(ctx context.Context, s Scope) ([]CodeRepo, error) {
	if checkScope(s) != nil {
		return nil, ErrInvalid
	}
	rows, err := p.q().Query(ctx, `SELECT `+codeRepoColumns+` FROM code_repos r WHERE organization_id=$1 AND project_id=$2 ORDER BY repo LIMIT 100`, s.WorkspaceID, s.ProjectID)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	out := []CodeRepo{}
	for rows.Next() {
		r, err := scanCodeRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, pgErr(rows.Err())
}

func (p *Postgres) CodeRepoFiles(ctx context.Context, s Scope, repo string) (CodeGraphFiles, error) {
	if checkScope(s) != nil || repo == "" {
		return CodeGraphFiles{}, ErrInvalid
	}
	out := CodeGraphFiles{Repo: repo, Files: map[string]string{}}
	var files []byte
	err := p.q().QueryRow(ctx, `SELECT revision,files FROM code_repos WHERE organization_id=$1 AND project_id=$2 AND repo=$3`, s.WorkspaceID, s.ProjectID, repo).Scan(&out.Revision, &files)
	if errors.Is(err, pgx.ErrNoRows) {
		return CodeGraphFiles{}, ErrNotFound
	}
	if err != nil {
		return CodeGraphFiles{}, pgErr(err)
	}
	if json.Unmarshal(files, &out.Files) != nil {
		return CodeGraphFiles{}, ErrUnavailable
	}
	return out, nil
}

// lockCodeRepo reads the repository row for update and checks the revision the
// writer saw; a missing row is revision 0.
func lockCodeRepo(ctx context.Context, q pgQuery, s Scope, repo string, expected int64) (int64, int, error) {
	var rev int64
	var files int
	err := q.QueryRow(ctx, `SELECT revision,(SELECT count(*) FROM jsonb_object_keys(files)) FROM code_repos WHERE organization_id=$1 AND project_id=$2 AND repo=$3 FOR UPDATE`, s.WorkspaceID, s.ProjectID, repo).Scan(&rev, &files)
	if errors.Is(err, pgx.ErrNoRows) {
		rev = 0
	} else if err != nil {
		return 0, 0, pgErr(err)
	}
	if rev != expected {
		return 0, 0, ErrConflict
	}
	return rev, files, nil
}

func (p *Postgres) ApplyCodeBatch(ctx context.Context, s Scope, in CodeGraphBatch, now time.Time) (CodeRepo, error) {
	if checkScope(s) != nil || in.Repo == "" {
		return CodeRepo{}, ErrInvalid
	}
	var out CodeRepo
	err := p.transactionWithIsolation(ctx, pgx.ReadCommitted, func(ctx context.Context, tx Tx) error {
		q := tx.(*Postgres).q()
		rev, count, err := lockCodeRepo(ctx, q, s, in.Repo, in.ExpectedRevision)
		if err != nil {
			return err
		}
		if count+len(in.Files) > codeRepoFiles {
			return ErrInvalid
		}
		if rev == 0 {
			if _, err := q.Exec(ctx, `INSERT INTO code_repos(organization_id,project_id,repo,updated_at,revision) VALUES($1,$2,$3,$4,0)`, s.WorkspaceID, s.ProjectID, in.Repo, now); err != nil {
				return pgErr(err)
			}
		}
		paths := make([]string, 0, len(in.Files)+len(in.Remove))
		hashes := map[string]string{}
		for _, f := range in.Files {
			paths = append(paths, f.Path)
			hashes[f.Path] = f.Hash
		}
		paths = append(paths, in.Remove...)
		for _, table := range []string{"code_edges", "code_symbols"} {
			if _, err := q.Exec(ctx, `DELETE FROM `+table+` WHERE organization_id=$1 AND project_id=$2 AND repo=$3 AND path=ANY($4)`, s.WorkspaceID, s.ProjectID, in.Repo, paths); err != nil {
				return pgErr(err)
			}
		}
		if n := len(in.Symbols); n > 0 {
			ids, path, lang, kind, name, qname, sig, doc := make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
			start, end, exported := make([]int32, n), make([]int32, n), make([]bool, n)
			for i, sym := range in.Symbols {
				ids[i], path[i], lang[i], kind[i], name[i], qname[i], sig[i], doc[i] = sym.ID, sym.Path, sym.Lang, sym.Kind, sym.Name, sym.QName, sym.Signature, sym.Doc
				start[i], end[i], exported[i] = int32(sym.StartLine), int32(sym.EndLine), sym.Exported
			}
			// A symbol that moved to a file of this batch from a file of a later
			// batch changes owner here; the later batch then no longer deletes it.
			if _, err := q.Exec(ctx, `INSERT INTO code_symbols(organization_id,project_id,repo,id,path,lang,kind,name,qname,signature,doc,start_line,end_line,exported)
SELECT $1,$2,$3,u.* FROM unnest($4::text[],$5::text[],$6::text[],$7::text[],$8::text[],$9::text[],$10::text[],$11::text[],$12::int[],$13::int[],$14::bool[]) AS u
ON CONFLICT (organization_id,project_id,repo,id) DO UPDATE SET path=EXCLUDED.path,lang=EXCLUDED.lang,kind=EXCLUDED.kind,name=EXCLUDED.name,qname=EXCLUDED.qname,
signature=EXCLUDED.signature,doc=EXCLUDED.doc,start_line=EXCLUDED.start_line,end_line=EXCLUDED.end_line,exported=EXCLUDED.exported`,
				s.WorkspaceID, s.ProjectID, in.Repo, ids, path, lang, kind, name, qname, sig, doc, start, end, exported); err != nil {
				return pgErr(err)
			}
		}
		if n := len(in.Edges); n > 0 {
			from, to, kind, path := make([]string, n), make([]string, n), make([]string, n), make([]string, n)
			line := make([]int32, n)
			for i, e := range in.Edges {
				from[i], to[i], kind[i], path[i], line[i] = e.From, e.To, e.Kind, e.Path, int32(e.Line)
			}
			if _, err := q.Exec(ctx, `INSERT INTO code_edges(organization_id,project_id,repo,from_id,to_id,kind,path,line)
SELECT $1,$2,$3,u.* FROM unnest($4::text[],$5::text[],$6::text[],$7::text[],$8::int[]) AS u
ON CONFLICT (organization_id,project_id,repo,from_id,to_id,kind) DO UPDATE SET path=EXCLUDED.path,line=EXCLUDED.line`,
				s.WorkspaceID, s.ProjectID, in.Repo, from, to, kind, path, line); err != nil {
				return pgErr(err)
			}
		}
		add, err := json.Marshal(hashes)
		if err != nil {
			return ErrInvalid
		}
		if _, err := q.Exec(ctx, `UPDATE code_repos SET files=(files - $4::text[]) || $5::jsonb, revision=revision+1, updated_at=$6
WHERE organization_id=$1 AND project_id=$2 AND repo=$3`, s.WorkspaceID, s.ProjectID, in.Repo, append([]string{}, in.Remove...), string(add), now); err != nil {
			return pgErr(err)
		}
		out, err = scanCodeRepo(q.QueryRow(ctx, `SELECT `+codeRepoColumns+` FROM code_repos r WHERE organization_id=$1 AND project_id=$2 AND repo=$3`, s.WorkspaceID, s.ProjectID, in.Repo))
		return err
	})
	return out, err
}

func (p *Postgres) FinishCodeIndex(ctx context.Context, s Scope, in CodeGraphFinish, now time.Time) (CodeRepo, error) {
	if checkScope(s) != nil || in.Repo == "" {
		return CodeRepo{}, ErrInvalid
	}
	var out CodeRepo
	err := p.transactionWithIsolation(ctx, pgx.ReadCommitted, func(ctx context.Context, tx Tx) error {
		q := tx.(*Postgres).q()
		if _, _, err := lockCodeRepo(ctx, q, s, in.Repo, in.ExpectedRevision); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `UPDATE code_repos SET commit_sha=$4, stats=$5::jsonb, indexed_at=$6, updated_at=$6, revision=revision+1
WHERE organization_id=$1 AND project_id=$2 AND repo=$3`, s.WorkspaceID, s.ProjectID, in.Repo, in.Commit, string(in.Stats), now); err != nil {
			return pgErr(err)
		}
		var err error
		out, err = scanCodeRepo(q.QueryRow(ctx, `SELECT `+codeRepoColumns+` FROM code_repos r WHERE organization_id=$1 AND project_id=$2 AND repo=$3`, s.WorkspaceID, s.ProjectID, in.Repo))
		return err
	})
	return out, err
}

func (p *Postgres) DeleteCodeRepo(ctx context.Context, s Scope, repo string, expected int64) error {
	if checkScope(s) != nil || repo == "" {
		return ErrInvalid
	}
	return p.transactionWithIsolation(ctx, pgx.ReadCommitted, func(ctx context.Context, tx Tx) error {
		q := tx.(*Postgres).q()
		var rev int64
		err := q.QueryRow(ctx, `SELECT revision FROM code_repos WHERE organization_id=$1 AND project_id=$2 AND repo=$3 FOR UPDATE`, s.WorkspaceID, s.ProjectID, repo).Scan(&rev)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return pgErr(err)
		}
		if rev != expected {
			return ErrConflict
		}
		_, err = q.Exec(ctx, `DELETE FROM code_repos WHERE organization_id=$1 AND project_id=$2 AND repo=$3`, s.WorkspaceID, s.ProjectID, repo)
		return pgErr(err)
	})
}

const codeSymbolColumns = `s.repo,s.id,s.path,s.lang,s.kind,s.name,s.qname,s.signature,s.start_line,s.end_line,s.exported,s.doc`

func scanCodeSymbol(rows pgx.Rows, extra ...any) (string, CodeSymbol, error) {
	var repo string
	var sym CodeSymbol
	dest := append([]any{&repo, &sym.ID, &sym.Path, &sym.Lang, &sym.Kind, &sym.Name, &sym.QName, &sym.Signature, &sym.StartLine, &sym.EndLine, &sym.Exported, &sym.Doc}, extra...)
	if err := rows.Scan(dest...); err != nil {
		return "", CodeSymbol{}, pgErr(err)
	}
	return repo, sym, nil
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// codeTSQuery turns free text into prefix terms joined by AND; "" when none.
func codeTSQuery(q string) string {
	var terms []string
	for _, w := range strings.FieldsFunc(q, func(r rune) bool { return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') }) {
		if len(terms) == 8 {
			break
		}
		terms = append(terms, strings.ToLower(w)+":*")
	}
	return strings.Join(terms, " & ")
}

func (p *Postgres) FindCodeSymbols(ctx context.Context, s Scope, repo, query string, exact bool, limit int) ([]CodeSymbolHit, error) {
	if checkScope(s) != nil || query == "" || limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	var rows pgx.Rows
	var err error
	if exact {
		suffix := likeEscape(query)
		rows, err = p.q().Query(ctx, `SELECT `+codeSymbolColumns+`, CASE WHEN s.id=$4 THEN 0 WHEN s.qname=$4 THEN 1
 WHEN s.qname LIKE '%/'||$5 OR s.qname LIKE '%.'||$5 OR s.qname LIKE '%#'||$5 THEN 2 ELSE 3 END AS rank
FROM code_symbols s WHERE s.organization_id=$1 AND s.project_id=$2 AND ($3='' OR s.repo=$3)
 AND (s.id=$4 OR s.qname=$4 OR s.qname LIKE '%/'||$5 OR s.qname LIKE '%.'||$5 OR s.qname LIKE '%#'||$5 OR lower(s.name)=lower($4))
ORDER BY rank, s.kind='package', s.repo, s.qname LIMIT $6`, s.WorkspaceID, s.ProjectID, repo, query, suffix, limit)
	} else {
		ts := codeTSQuery(query)
		like := "%" + likeEscape(strings.ToLower(query)) + "%"
		rows, err = p.q().Query(ctx, `SELECT `+codeSymbolColumns+`, CASE WHEN lower(s.name)=lower($4) THEN 0 WHEN lower(s.name) LIKE $5 THEN 1 ELSE 2 END AS rank
FROM code_symbols s WHERE s.organization_id=$1 AND s.project_id=$2 AND ($3='' OR s.repo=$3)
 AND (lower(s.name) LIKE $5 OR lower(s.qname) LIKE $5 OR ($6<>'' AND s.search @@ to_tsquery('simple',$6)))
ORDER BY rank, CASE WHEN $6<>'' THEN ts_rank(s.search, to_tsquery('simple',$6)) ELSE 0 END DESC, s.exported DESC, length(s.qname), s.qname LIMIT $7`,
			s.WorkspaceID, s.ProjectID, repo, query, like, ts, limit)
	}
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	out := []CodeSymbolHit{}
	for rows.Next() {
		var rank int
		r, sym, err := scanCodeSymbol(rows, &rank)
		if err != nil {
			return nil, err
		}
		out = append(out, CodeSymbolHit{Repo: r, Rank: rank, Symbol: sym})
	}
	return out, pgErr(rows.Err())
}

func (p *Postgres) CodeNeighbors(ctx context.Context, s Scope, repo string, ids []string, incoming bool, kinds []string, limit int) ([]CodeNeighbor, error) {
	if checkScope(s) != nil || repo == "" || len(ids) == 0 || len(ids) > 2000 || len(kinds) == 0 || limit < 1 || limit > 5000 {
		return nil, ErrInvalid
	}
	match, other := "to_id", "e.from_id"
	if !incoming {
		match, other = "from_id", "e.to_id"
	}
	// One index probe per id (code_edges_to or the primary key) and one primary
	// key probe per edge. OFFSET 0 keeps the planner from flattening the lateral
	// probes into a join: a freshly uploaded graph has no statistics until
	// autovacuum runs, and the flattened plan then nests full scans (tens of
	// seconds on this repository's own graph).
	rows, err := p.q().Query(ctx, `SELECT `+codeSymbolColumns+`, e.from_id,e.to_id,e.kind,e.path,e.line
FROM unnest($4::text[]) AS q(id)
CROSS JOIN LATERAL (SELECT x.from_id,x.to_id,x.kind,x.path,x.line FROM code_edges x
  WHERE x.organization_id=$1 AND x.project_id=$2 AND x.repo=$3 AND x.`+match+`=q.id AND x.kind=ANY($5) OFFSET 0) e
CROSS JOIN LATERAL (SELECT y.* FROM code_symbols y
  WHERE y.organization_id=$1 AND y.project_id=$2 AND y.repo=$3 AND y.id=`+other+` OFFSET 0) s
ORDER BY s.path, s.start_line, e.kind LIMIT $6`, s.WorkspaceID, s.ProjectID, repo, ids, kinds, limit)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	out := []CodeNeighbor{}
	for rows.Next() {
		var e CodeEdge
		_, sym, err := scanCodeSymbol(rows, &e.From, &e.To, &e.Kind, &e.Path, &e.Line)
		if err != nil {
			return nil, err
		}
		out = append(out, CodeNeighbor{Edge: e, Symbol: sym})
	}
	return out, pgErr(rows.Err())
}

func (p *Postgres) CodeSymbolsInRanges(ctx context.Context, s Scope, repo string, ranges []CodeLineRange, kinds []string, limit int) ([]CodeSymbol, error) {
	if checkScope(s) != nil || repo == "" || len(ranges) == 0 || len(ranges) > 1000 || len(kinds) == 0 || limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	paths, starts, ends := make([]string, len(ranges)), make([]int32, len(ranges)), make([]int32, len(ranges))
	for i, r := range ranges {
		paths[i], starts[i], ends[i] = r.Path, int32(r.StartLine), int32(r.EndLine)
	}
	// code_symbols_path narrows to the file; the overlap test runs on its few rows.
	rows, err := p.q().Query(ctx, `SELECT `+codeSymbolColumns+` FROM code_symbols s
WHERE s.organization_id=$1 AND s.project_id=$2 AND s.repo=$3 AND s.kind=ANY($7)
 AND EXISTS (SELECT 1 FROM unnest($4::text[],$5::int[],$6::int[]) AS r(path,a,b) WHERE r.path=s.path AND s.start_line<=r.b AND s.end_line>=r.a)
ORDER BY s.path, s.start_line, s.qname LIMIT $8`, s.WorkspaceID, s.ProjectID, repo, paths, starts, ends, kinds, limit)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	out := []CodeSymbol{}
	for rows.Next() {
		_, sym, err := scanCodeSymbol(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sym)
	}
	return out, pgErr(rows.Err())
}

var _ codeGraphStore = (*Postgres)(nil)
