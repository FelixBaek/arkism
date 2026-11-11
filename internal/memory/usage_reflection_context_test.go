package memory

import (
	"context"
	"testing"
)

func TestReflectionContextKeepsWriterIdentityUnchanged(t *testing.T) {
	writer := &httpActor{principal: "agent:a", role: "agent"}
	ctx := context.WithValue(context.Background(), httpActorKey{}, writer)
	hub := Decision{PrincipalID: "service:hub", Role: "service"}
	if !sameRequestActor(reflectionContext(ctx, "hub-token"), hub) || writer.changed.Load() {
		t.Fatal("the hub's reflection marked the writer's request as an identity change")
	}
	if sameRequestActor(ctx, hub) || !writer.changed.Load() {
		t.Fatal("another principal on the writer's own context must still count as an identity change")
	}
}
