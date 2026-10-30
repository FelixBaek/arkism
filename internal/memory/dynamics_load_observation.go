package memory

import "context"

// DynamicsLoadObservation is body-free. Transport request/attempt ledgers are
// separate: these counters never claim received requests, visibility, or success.
type DynamicsLoadObservation struct {
	QueueCount            int64
	ActiveQueueBytes      int64
	OldestAcceptedNS      *int64
	OldestAgeNS           *int64
	Head                  int64
	Tail                  int64
	LeaseGeneration       int64
	PublicationGeneration int64
	AsOfNS                int64
	PublishedNS           int64
	TerminalLedgerBytes   int64
	Withheld              bool
	RepairStartNS         *int64
}

func (s *Service) ObserveDynamicsLoad(ctx context.Context, token string, scope Scope) (DynamicsLoadObservation, error) {
	ctx, err := s.nativeActor(ctx, token, scope, "read:jobs")
	if err != nil {
		return DynamicsLoadObservation{}, err
	}
	var out DynamicsLoadObservation
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		row, e := dynamicsLockCoordinator(ctx, tx, scope)
		if e != nil {
			return e
		}
		if e = s.dynamicsBinding(ctx, tx, scope, row); e != nil {
			return e
		}
		p := row.DynamicsLoad
		total, e := tx.(dynamicsLoadQueries).DynamicsLoadLedgerBytes(ctx, scope)
		if e != nil {
			return e
		}
		out = DynamicsLoadObservation{QueueCount: p.Count, ActiveQueueBytes: p.ActiveBytes, Head: p.Head, Tail: p.Tail, LeaseGeneration: p.LeaseGeneration, PublicationGeneration: p.PublicationGeneration, AsOfNS: p.AsOfNS, PublishedNS: p.PublishedNS, TerminalLedgerBytes: total, Withheld: p.Withheld, RepairStartNS: p.RepairStartNS}
		if p.Count > 0 {
			oldest, e := tx.Get(ctx, colJobs, scope, dynamicsEntryID(scope, p.Head+1))
			if e != nil {
				return e
			}
			if e = dynamicsValidateRow(oldest, scope, "entry"); e != nil {
				return e
			}
			accepted := oldest.DynamicsLoad.AcceptedNS
			age := dynamicsNowNS() - accepted
			if accepted <= 0 || age < 0 {
				return ErrConflict
			}
			out.OldestAcceptedNS = &accepted
			out.OldestAgeNS = &age
		}
		return nil
	})
	return out, err
}
