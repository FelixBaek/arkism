package memory

import (
	"context"
	"errors"
)

// Visibility is checked by the caller before the batched derived-row read.
// A missing signal is normal; malformed data or storage errors remain failures.
func (s *Service) visibleSignalRecords(ctx context.Context, scope Scope, memories []string) (map[string]Record, error) {
	out := map[string]Record{}
	if len(memories) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(memories))
	wanted := map[string]string{}
	for _, id := range memories {
		key := memorySignalID(scope, id)
		ids = append(ids, key)
		wanted[key] = id
	}
	var records []Record
	if reader, ok := s.Repo.(signalRecordReader); ok {
		var err error
		records, err = reader.MemorySignalRecords(ctx, scope, ids)
		if err != nil {
			return nil, err
		}
	} else {
		for _, id := range ids {
			r, err := s.Repo.Get(ctx, colJobs, scope, id)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			records = append(records, r)
		}
	}
	if len(records) > len(ids) {
		return nil, ErrUnavailable
	}
	for _, r := range records {
		id, ok := wanted[r.ID]
		if !ok || r.Scope != scope || r.Kind != memorySignalKind || r.MemorySignal == nil || r.MemorySignal.Memory.ID != id {
			return nil, ErrUnavailable
		}
		if _, dup := out[id]; dup {
			return nil, ErrUnavailable
		}
		out[id] = r
	}
	return out, nil
}
