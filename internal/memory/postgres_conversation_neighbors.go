package memory

import "context"

// Optional repository capability: at most the immediately previous and next
// batch in one captured conversation. It avoids scanning unrelated conversations.
type conversationNeighborStore interface {
	ConversationNeighbors(context.Context, Record) ([]Record, error)
}

func (p *Postgres) ConversationNeighbors(ctx context.Context, anchor Record) ([]Record, error) {
	if anchor.SourceRef == "" || anchor.CollectedAt == nil || anchor.CreatedAt.IsZero() {
		return nil, ErrInvalid
	}
	if anchor.ConversationOrder != nil {
		if !validConversationOrder(anchor.ConversationOrder) {
			return nil, ErrInvalid
		}
		order := anchor.ConversationOrder
		where := `kind=$6 AND md5(doc->>'source_ref')=md5($7) AND md5(doc->>'origin_product')=md5($8) AND doc->>'source_ref'=$7 AND doc->>'origin_product'=$8 AND doc->'conversation_order'->>'epoch'=$9 AND (doc->'conversation_order'->>'total')::int=$10 AND (doc->'conversation_order'->>'index')::int=ANY($11)`
		rows, err := p.selectRecords(ctx, "sources", anchor.Scope, where, []any{conversationSourceKind, anchor.SourceRef, anchor.OriginProduct, order.Epoch, order.Total, []int{order.Index - 1, order.Index + 1}}, `(doc->'conversation_order'->>'index')::int ASC,id ASC`, 3)
		if err != nil {
			return nil, err
		}
		if len(rows) > 2 {
			return nil, ErrUnavailable
		}
		seen := map[int]bool{}
		for _, r := range rows {
			if r.ConversationOrder == nil || seen[r.ConversationOrder.Index] {
				return nil, ErrUnavailable
			}
			seen[r.ConversationOrder.Index] = true
		}
		return rows, nil
	}
	where := `doc->'conversation_order' IS NULL AND kind=$6 AND md5(doc->>'source_ref')=md5($7) AND md5(doc->>'origin_product')=md5($8) AND doc->>'source_ref'=$7 AND doc->>'origin_product'=$8 AND (doc->'collected_at'->>'$date')::timestamptz=$9`
	args := []any{conversationSourceKind, anchor.SourceRef, anchor.OriginProduct, *anchor.CollectedAt, anchor.CreatedAt, anchor.ID}
	// Include inactive entries when locating adjacency: deletion must not make a
	// farther, unrelated turn become the immediate neighbour. The caller rechecks it.
	before, err := p.selectRecords(ctx, "sources", anchor.Scope, where+` AND (`+pgDate+` < $10 OR (`+pgDate+` = $10 AND id < $11))`, args, pgDate+` DESC,id DESC`, 1)
	if err != nil {
		return nil, err
	}
	after, err := p.selectRecords(ctx, "sources", anchor.Scope, where+` AND (`+pgDate+` > $10 OR (`+pgDate+` = $10 AND id > $11))`, args, pgDate+` ASC,id ASC`, 1)
	if err != nil {
		return nil, err
	}
	return append(after, before...), nil
}
