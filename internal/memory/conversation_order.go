package memory

// ConversationOrder is the producer's explicit order within one immutable
// capture epoch. It is never inferred from timestamps or assigned to old rows.
type ConversationOrder struct {
	Epoch string `json:"epoch" bson:"epoch"`
	Index int    `json:"index" bson:"index"`
	Total int    `json:"total" bson:"total"`
}

func validConversationOrder(o *ConversationOrder) bool {
	return o == nil || idempotencyPattern.MatchString(o.Epoch) && o.Index >= 1 && o.Total >= o.Index && o.Total <= 100000
}
func sameConversationEpoch(a, b Record) bool {
	if a.ConversationOrder == nil || b.ConversationOrder == nil {
		return a.ConversationOrder == nil && b.ConversationOrder == nil && a.CollectedAt != nil && b.CollectedAt != nil && a.CollectedAt.Equal(*b.CollectedAt)
	}
	return a.ConversationOrder.Epoch == b.ConversationOrder.Epoch && a.ConversationOrder.Total == b.ConversationOrder.Total
}
func adjacentConversation(a, b Record) bool {
	if !sameConversationEpoch(a, b) {
		return false
	}
	if a.ConversationOrder == nil {
		return true
	}
	d := a.ConversationOrder.Index - b.ConversationOrder.Index
	return d == 1 || d == -1
}
