package memory

import "testing"

func TestModelTransferCanonicalSourceOnly(t *testing.T) {
	f, _, in := modelTransferFixture(t)
	source := f.source()
	in.Refs = nil
	in.SourceRefs = []Ref{{Collection: "sources", ID: source.ID, Revision: source.Revision}}
	in.Content = `{"authority":"data_only_untrusted","data":"synthetic source observation"}`
	out, err := f.svc.AuthorizeModelArchiveTransfer(testCtx, "transfer-service", in)
	if err != nil || !out.Authorized || out.ContentHash != ContentHash(in.Content) {
		t.Fatalf("valid source-only transfer failed: %+v %v", out, err)
	}
}

func TestModelTransferCanonicalSourceFences(t *testing.T) {
	for _, mode := range []string{"stale", "deleted", "foreign", "quarantined", "restricted", "wrong-collection", "no-read", "agent", "audit"} {
		t.Run(mode, func(t *testing.T) {
			f, _, in := modelTransferFixture(t)
			source := f.source()
			in.Refs = nil
			in.SourceRefs = []Ref{{Collection: "sources", ID: source.ID, Revision: source.Revision}}
			in.Content = "synthetic exact serialized model data"
			switch mode {
			case "stale":
				in.SourceRefs[0].Revision++
			case "wrong-collection":
				in.SourceRefs[0].Collection = "memories"
			case "deleted":
				if err := f.repo.Delete(testCtx, "sources", scopeA, source.ID, source.Revision); err != nil {
					t.Fatal(err)
				}
			case "foreign":
				in.Scope = scopeB
			case "restricted", "quarantined":
				if mode == "restricted" {
					source.Sensitivity = "restricted"
				} else {
					source.PrivacyQuarantined = true
				}
				source.Revision++
				if err := f.repo.Replace(testCtx, "sources", source, source.Revision-1); err != nil {
					t.Fatal(err)
				}
				in.SourceRefs[0].Revision = source.Revision
			case "no-read":
				f.auth.actors["transfer-service"] = actor("harness-service", roleService, true, "transfer")
			case "agent":
				f.auth.actors["transfer-service"] = actor("agent", roleAgent, true, "transfer", "read")
			case "audit":
				f.repo.failAudit = true
			}
			out, err := f.svc.AuthorizeModelArchiveTransfer(testCtx, "transfer-service", in)
			if err == nil || out.Authorized || out.ContentHash != "" {
				t.Fatalf("unsafe source transfer %s allowed: %+v %v", mode, out, err)
			}
		})
	}
}
