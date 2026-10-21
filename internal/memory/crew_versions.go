package memory

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// Earlier definitions of a crew item (판 이력): UpdateCrew keeps the definition it
// replaces as "<id>.v<revision>". The collection is private like work checkpoints:
// generic CRUD never reaches it, only CrewVersions reads it.
const (
	colCrewVersions = "crew_versions"
	crewVersionsMax = 20
)

type CrewVersion struct {
	Revision int64     `json:"revision"`
	At       time.Time `json:"at"`
	Crew     Crew      `json:"crew"`
}

type CrewVersionPage struct {
	Items []CrewVersion `json:"items"`
}

func crewVersionID(id string, revision int64) string {
	return id + ".v" + strconv.FormatInt(revision, 10)
}

// keepCrewVersion stores r's current definition before UpdateCrew replaces it, and drops the row
// that just left the window CrewVersions reads, so each item keeps at most crewVersionsMax-1 rows.
func keepCrewVersion(ctx context.Context, tx Tx, r Record) error {
	c := *r.Crew
	if err := tx.Insert(ctx, colCrewVersions, Record{ID: crewVersionID(r.ID, r.Revision), Scope: r.Scope, Kind: colCrewVersions, Status: statusActive,
		Revision: 1, Text: c.Name, Crew: &c, CreatedAt: r.UpdatedAt, UpdatedAt: r.UpdatedAt, CreatedBy: r.CreatedBy, CreatedByType: r.CreatedByType}); err != nil {
		return err
	}
	// After this save the live revision is r.Revision+1; CrewVersions reads down to r.Revision+2-crewVersionsMax.
	if old := r.Revision + 1 - crewVersionsMax; old >= 1 {
		if err := tx.Delete(ctx, colCrewVersions, r.Scope, crewVersionID(r.ID, old), 1); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return nil
}

// CrewVersions returns the live definition and the kept ones among the crewVersionsMax-1 before it, newest first.
// Saves made before versions were kept have no row and are skipped.
func (s *Service) CrewVersions(ctx context.Context, token string, scope Scope, id string) (CrewVersionPage, error) {
	if !validScope(scope) || !validRunLabel(id, 256) {
		return CrewVersionPage{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colCrew, scope, id, ""); err != nil {
		return CrewVersionPage{}, err
	}
	cur, err := s.Repo.Get(ctx, colCrew, scope, id)
	if err != nil {
		return CrewVersionPage{}, err
	}
	if cur.Crew == nil || cur.Status != statusActive {
		return CrewVersionPage{}, ErrNotFound
	}
	out := CrewVersionPage{Items: []CrewVersion{{Revision: cur.Revision, At: cur.UpdatedAt, Crew: *cur.Crew}}}
	for rev := cur.Revision - 1; rev >= 1 && rev > cur.Revision-crewVersionsMax; rev-- {
		v, err := s.Repo.Get(ctx, colCrewVersions, scope, crewVersionID(id, rev))
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return CrewVersionPage{}, err
		}
		if v.Crew != nil {
			out.Items = append(out.Items, CrewVersion{Revision: rev, At: v.UpdatedAt, Crew: *v.Crew})
		}
	}
	return out, nil
}

func crewVersionRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/crew/versions/{id}", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.CrewVersions(r.Context(), bearer(r), scopeQuery(r), r.PathValue("id"))
		respond(w, out, e)
	})
}
