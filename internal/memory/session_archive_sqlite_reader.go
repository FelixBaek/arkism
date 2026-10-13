package memory

import (
	"context"
	"errors"
	"io"
	"sort"

	sqlite "github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/vfs/readervfs"
)

var errArchiveSQLiteBudget = errors.New("archive SQLite read budget")
var errArchiveSQLiteUnsupported = errors.New("archive SQLite schema unsupported")

type archiveChunkExtent struct {
	ID, SHA string
	Bytes   int64
}
type archiveChunkExtentStore interface {
	SessionArchiveChunkExtents(context.Context, Scope, string, []string) ([]archiveChunkExtent, error)
}

// Return only lengths and hashes, never all ciphertext bodies. GCM v1 stores a
// 12-byte nonce and 16-byte tag in padded standard base64. Commit has verified
// every chunk; ReadAt authenticates each body again and checks its exact size.
func (p *Postgres) SessionArchiveChunkExtents(ctx context.Context, scope Scope, parent string, ids []string) ([]archiveChunkExtent, error) {
	if checkScope(scope) != nil || len(ids) == 0 || len(ids) > sessionArchiveMaxChunks {
		return nil, ErrInvalid
	}
	rows, err := p.q().Query(ctx, `SELECT id,doc->>'content_hash',
 CASE WHEN length(doc#>>'{session_archive,cipher}')%4=0 THEN
 length(doc#>>'{session_archive,cipher}')/4*3-28-
 CASE WHEN right(doc#>>'{session_archive,cipher}',2)='==' THEN 2 WHEN right(doc#>>'{session_archive,cipher}',1)='=' THEN 1 ELSE 0 END ELSE -1 END
 FROM memory_records WHERE collection=$5 AND workspace_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4
 AND kind=$6 AND id=ANY($7::text[]) AND doc#>>'{session_archive,parent_id}'=$8`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, colSessionArchiveChunks, kindSessionArchiveChunk, ids, parent)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	var out []archiveChunkExtent
	for rows.Next() {
		var e archiveChunkExtent
		if err := rows.Scan(&e.ID, &e.SHA, &e.Bytes); err != nil {
			return nil, ErrUnavailable
		}
		out = append(out, e)
	}
	return out, pgErr(rows.Err())
}

type archiveSQLiteReader struct {
	line    *archiveLineReader
	ends    []int64
	budget  *int
	cache   map[int][]byte
	order   []int
	failure error
}

func newArchiveSQLiteReader(ctx context.Context, s *Service, h Record, budget *int) (*archiveSQLiteReader, error) {
	store, ok := s.Repo.(archiveChunkExtentStore)
	if !ok {
		return nil, ErrUnavailable
	}
	extents, err := store.SessionArchiveChunkExtents(ctx, h.Scope, h.ID, h.SessionArchive.Chunks)
	if err != nil {
		return nil, err
	}
	sizes := map[string]int64{}
	for _, e := range extents {
		if e.Bytes < 1 || e.Bytes > SessionArchiveChunkSize || sessionArchiveChunkID(h.Scope, h.ID, e.SHA) != e.ID {
			return nil, ErrConflict
		}
		if _, duplicate := sizes[e.ID]; duplicate {
			return nil, ErrConflict
		}
		sizes[e.ID] = e.Bytes
	}
	r := &archiveSQLiteReader{line: newArchiveLineReader(s, ctx, h, ArchiveMessageRef{}), budget: budget, cache: map[int][]byte{}}
	var total int64
	for _, id := range h.SessionArchive.Chunks {
		n, ok := sizes[id]
		if !ok {
			return nil, ErrConflict
		}
		total += n
		r.ends = append(r.ends, total)
	}
	return r, nil
}
func (r *archiveSQLiteReader) Size() (int64, error) { return r.ends[len(r.ends)-1], nil }
func (r *archiveSQLiteReader) ReadAt(dst []byte, offset int64) (n int, err error) {
	defer func() {
		if err != nil && !errors.Is(err, io.EOF) {
			r.failure = err
		}
	}()
	if offset < 0 {
		return 0, ErrInvalid
	}
	for len(dst) > 0 {
		if err := r.line.ctx.Err(); err != nil {
			return n, err
		}
		index := sort.Search(len(r.ends), func(i int) bool { return r.ends[i] > offset })
		if index == len(r.ends) {
			return n, io.EOF
		}
		start := int64(0)
		if index > 0 {
			start = r.ends[index-1]
		}
		data, ok := r.cache[index]
		if !ok {
			size := int(r.ends[index] - start)
			if *r.budget < size {
				return n, errArchiveSQLiteBudget
			}
			*r.budget -= size
			data, err = r.line.chunk(index)
			if err != nil {
				return n, err
			}
			if len(data) != size {
				return n, ErrConflict
			}
			if len(r.order) == 4 {
				delete(r.cache, r.order[0])
				r.order = r.order[1:]
			}
			r.order = append(r.order, index)
			r.cache[index] = data
		}
		copied := copy(dst, data[offset-start:])
		n += copied
		offset += int64(copied)
		dst = dst[copied:]
	}
	return n, nil
}
func openArchiveSQLite(ctx context.Context, r *archiveSQLiteReader) (*sqlite.Conn, func(), error) {
	name := "arkism-" + ID()
	readervfs.Create(name, r)
	cleanup := func() { readervfs.Delete(name) }
	db, err := sqlite.OpenContext(sqlite.WithMaxMemory(ctx, 64<<20), "file:"+name+"?vfs=reader&mode=ro")
	if err != nil {
		cleanup()
		if r.failure != nil {
			return nil, nil, r.failure
		}
		return nil, nil, errArchiveSQLiteUnsupported
	}
	close := func() { db.Close(); cleanup() }
	db.SetInterrupt(ctx)
	db.Limit(sqlite.LIMIT_LENGTH, archiveRecordLimit+4096)
	db.Limit(sqlite.LIMIT_ATTACHED, 0)
	db.HardHeapLimit(32 << 20)
	if _, err = db.Config(sqlite.DBCONFIG_DEFENSIVE, true); err != nil {
		close()
		return nil, nil, ErrUnavailable
	}
	if _, err = db.Config(sqlite.DBCONFIG_TRUSTED_SCHEMA, false); err != nil {
		close()
		return nil, nil, ErrUnavailable
	}
	if err = db.Exec("PRAGMA query_only=ON; PRAGMA temp_store=MEMORY"); err != nil {
		close()
		return nil, nil, ErrUnavailable
	}
	return db, close, nil
}
