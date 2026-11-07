package memory

import (
	"encoding/json"
	"net/url"
	"strconv"
	"time"
)

// A4 exercises the formerly missing operations through real HTTP routing and
// service transitions. Provider and storage inputs are synthetic local fixtures.
func contractMissingManagement(c *contractHTTP) {
	sc := c.scope
	c.call("POST /v1/wiki/build_from_memories", "", WikiMemoryBuildInput{Scope: sc, Query: "fixture"}, nil)
	c.call("POST /v1/wiki/folders", "", WikiFolderInput{Scope: sc, Path: "Contract"}, nil)
	c.call("PATCH /v1/wiki/folders", "", WikiFolderMoveInput{Scope: sc, From: "Contract", To: "Moved"}, nil)
	c.call("GET /v1/wiki/tree", "", nil, nil)
	pageID := wikiPageID(sc, "fixture")
	page, err := c.f.repo.Get(testCtx, colWikiPages, sc, pageID)
	if err != nil {
		c.t.Fatal(err)
	}
	out := c.call("PATCH /v1/wiki/pages/{id}", pageID, WikiPagePatchInput{Scope: sc, ExpectedRevision: page.Revision, Title: "Renamed fixture"}, nil)
	pageID = decodeContract[string](c.t, out["id"])
	page, err = c.f.repo.Get(testCtx, colWikiPages, sc, pageID)
	if err != nil {
		c.t.Fatal(err)
	}
	c.call("DELETE /v1/wiki/pages/{id}", pageID, nil, url.Values{"expected_revision": {strconv.FormatInt(page.Revision, 10)}})
	c.call("DELETE /v1/wiki/folders", "", nil, url.Values{"path": {"Moved"}})
	c.call("GET /v1/knowledge/sources/{id}/chunks", "contract-source", nil, nil)
	c.call("GET /v1/knowledge/sources/{id}/history", "contract-source", nil, nil)
	now := time.Now().UTC()
	c.seed(colJobs, Record{ID: "contract-extract-document", Scope: sc, Kind: fileExtractDocKind, Status: "active", Revision: 1, Target: &Ref{ID: "contract-extract-job"}, CreatedAt: now, UpdatedAt: now})
	c.seed(colJobs, Record{ID: "contract-extract-job", Scope: sc, Kind: fileExtractJobKind, Status: JobStatusDone, Revision: 1, CreatedAt: now, UpdatedAt: now, FileExtract: &FileExtractJob{Document: "contract-extract-document", Label: "Fixture", Chunks: []FileExtractChunk{}, Kept: []FileExtractChunk{}}})
	c.call("GET /v1/files/memories/documents/{id}/chunks", "contract-extract-document", nil, nil)
	c.call("GET /v1/files/memories/documents/{id}/history", "contract-extract-document", nil, nil)
	c.f.svc.Repo = opsFakeRepo{c.f.repo}
	c.call("POST /v1/extensions/{extension}/data", "", ExtDataChange{Scope: sc, Changes: []ExtDataOp{{Op: "put", Key: "key", Value: json.RawMessage(`{"value":"fixture"}`)}}}, nil)
	c.call("GET /v1/extensions/{extension}/data", "", nil, nil)
	c.call("GET /v1/extensions/{extension}/data/{key}", "", nil, nil)
}
