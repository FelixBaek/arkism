package host

import "testing"

func TestMemoryDeliveryMetadataTracksLatestRevisionAndResets(t *testing.T) {
	store := SessionStore{Root: t.TempDir()}
	refs := []MemoryDelivery{{Collection: "memories", ID: "m", Revision: 3, Form: "meta", Scope: map[string]string{"user_id": "u"}}, {Collection: "memories", ID: "m", Revision: 2, Form: "full", Scope: map[string]string{"user_id": "u"}}}
	if err := store.MarkMemoryDelivered("s", refs); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDelivered("s", map[string]int64{"m": 3}); err != nil {
		t.Fatal(err)
	}
	got := store.DeliveredMemories("s")["m"]
	if got.Revision != 3 || got.Form != "meta" || got.Scope["user_id"] != "u" {
		t.Fatal(got)
	}
	if err := store.ResetDelivered("s"); err != nil {
		t.Fatal(err)
	}
	if len(store.Delivered("s")) != 0 || len(store.DeliveredMemories("s")) != 0 {
		t.Fatal("compaction left delivery receipts")
	}
}
