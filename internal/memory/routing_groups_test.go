package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func routingGroupsFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	p := Providers{Providers: []Provider{
		{ID: "alpha", Name: "Alpha", Kind: "api", Models: []string{"shared", "Shared", "solo"}},
		{ID: "beta", Name: "Beta", Kind: "api", Models: []string{"shared"}},
		{ID: "gamma", Name: "Gamma", Kind: "api", Models: []string{"outside"}},
	}}
	if err := f.repo.Insert(testCtx, colProviders, Record{ID: providersID(scopeA), Scope: scopeA,
		Kind: colProviders, Revision: 1, Status: statusActive, Providers: &p}); err != nil {
		t.Fatal(err)
	}
	return f
}

func routingTestGroup() RoutingGroup {
	return RoutingGroup{ID: "group/review", Name: "코드 검토 🐦", Enabled: true,
		Strategy: "ordered", Members: []string{"beta/shared", "alpha/shared"}}
}

func saveRoutingTestGroups(t *testing.T, f *fixture, groups []RoutingGroup, auto bool, rev int64) RoutingGroupsPage {
	t.Helper()
	out, err := f.svc.SetRoutingGroups(testCtx, f.human, RoutingGroupsInput{
		Scope: scopeA, ExpectedRevision: rev, Groups: groups, AutoDiscover: auto})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// full replacement and shared revisions.
func TestRoutingGroupsCRUDAndRevision(t *testing.T) {
	f := routingGroupsFixture(t)
	page, err := f.svc.RoutingGroups(testCtx, f.human, scopeA)
	if err != nil || page.Revision != 0 || page.AutoDiscover || page.Items == nil || len(page.Items) != 0 || page.Authority != AuthorityDataOnly {
		t.Fatal("initial document", page, err)
	}
	a, b := routingTestGroup(), routingTestGroup()
	b.ID, b.Name = "group/second", "두 번째"
	page = saveRoutingTestGroups(t, f, []RoutingGroup{a, b}, false, 0)
	if page.Revision != 1 || len(page.Items) != 2 || page.Items[0].Discovered {
		t.Fatal(page)
	}
	if _, err = f.svc.SetRoutingGroups(testCtx, f.human, RoutingGroupsInput{Scope: scopeA, Groups: []RoutingGroup{a}}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale replacement", err)
	}
	b.Name, b.Enabled, b.Strategy, b.Members = "새 이름", false, "smart", []string{"alpha/shared"}
	page = saveRoutingTestGroups(t, f, []RoutingGroup{b, a}, true, 1)
	if page.Revision != 2 || !page.AutoDiscover || page.Items[0].Name != b.Name || page.Items[0].Enabled || page.Items[0].Strategy != "smart" || len(page.Items[0].Members) != 1 || page.Items[1].ID != a.ID {
		t.Fatal("edit/reorder", page)
	}
	// Returned/request slices must not change stored state in an in-memory repository.
	page.Items[1].Members[0] = "gamma/outside"
	a.Members[0] = "gamma/outside"
	got, err := f.svc.RoutingGroups(testCtx, f.human, scopeA)
	if err != nil || got.Items[1].Members[0] != "beta/shared" {
		t.Fatal("slice alias", got, err)
	}
	off := false
	if _, err = f.svc.SetRoutingPolicy(testCtx, f.human, RoutingPolicyInput{Scope: scopeA, ExpectedRevision: 1, AutoRouting: &off}); !errors.Is(err, ErrConflict) {
		t.Fatal("policy did not share group revision", err)
	}
	if _, err = f.svc.SetRoutingModel(testCtx, f.human, "alpha/shared", RoutingModelInput{Scope: scopeA, ExpectedRevision: 1}); !errors.Is(err, ErrConflict) {
		t.Fatal("model did not share group revision", err)
	}
	if _, err = f.svc.SetRoutingPolicy(testCtx, f.human, RoutingPolicyInput{Scope: scopeA, ExpectedRevision: 2, AutoRouting: &off}); err != nil {
		t.Fatal(err)
	}
	page = saveRoutingTestGroups(t, f, []RoutingGroup{}, false, 3)
	doc, rev, err := f.svc.routingDoc(testCtx, scopeA)
	if err != nil || page.Revision != 4 || len(page.Items) != 0 || page.Items == nil || doc.AutoRouting || rev != 4 {
		t.Fatal("delete/preserve policy", page, doc, err)
	}
}

func TestRoutingGroupsDiscoveryAndOverride(t *testing.T) {
	f := routingGroupsFixture(t)
	page := saveRoutingTestGroups(t, f, []RoutingGroup{}, true, 0)
	if len(page.Items) != 1 || !page.Items[0].Discovered || page.Items[0].Name != "shared" ||
		!routingGroupIDRe.MatchString(page.Items[0].ID) || !slices.Equal(page.Items[0].Members, []string{"alpha/shared", "beta/shared"}) {
		t.Fatal("exact names across providers", page)
	}
	id := page.Items[0].ID
	p, _ := f.svc.providersDoc(testCtx, scopeA)
	p.Providers[0], p.Providers[1] = p.Providers[1], p.Providers[0]
	p.Providers[0].Models = append(p.Providers[0].Models, "shared")
	doc, _, _ := f.svc.routingDoc(testCtx, scopeA)
	if other := routingGroupsView(p, doc, 1); len(other.Items) != 1 || other.Items[0].ID != id || !slices.Equal(other.Items[0].Members, page.Items[0].Members) {
		t.Fatal("discovery must be stable and deduplicated", other)
	}
	d, err := f.svc.DecideRoute(testCtx, f.agent, RoutingDecideInput{Scope: scopeA, Task: "review", Group: id})
	if err != nil || d.ID != "alpha/shared" || d.Group != id || !slices.Equal(d.Fallbacks, []string{"beta/shared"}) {
		t.Fatal("discovered decision", d, err)
	}
	g := page.Items[0]
	g.Name, g.Discovered, g.Enabled, g.Strategy = "手動 그룹", false, false, "ordered"
	page = saveRoutingTestGroups(t, f, []RoutingGroup{g}, true, 1)
	if len(page.Items) != 1 || page.Items[0].Discovered || page.Items[0].Name != g.Name {
		t.Fatal("stored override", page)
	}
	if _, err = f.svc.DecideRoute(testCtx, f.agent, RoutingDecideInput{Scope: scopeA, Task: "review", Group: id}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("disabled override bypassed", err)
	}
	page = saveRoutingTestGroups(t, f, []RoutingGroup{}, true, 2)
	if len(page.Items) != 1 || page.Items[0].ID != id || !page.Items[0].Discovered {
		t.Fatal("override deletion restores discovery", page)
	}
	off := false
	if _, err = f.svc.SetRoutingModel(testCtx, f.human, "beta/shared", RoutingModelInput{Scope: scopeA, ExpectedRevision: 3, RoutingModel: RoutingModel{Enabled: &off}}); err != nil {
		t.Fatal(err)
	}
	page, err = f.svc.RoutingGroups(testCtx, f.human, scopeA)
	if err != nil || len(page.Items) != 0 {
		t.Fatal("disabled model discovered", page, err)
	}
}

func TestRoutingGroupsValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*RoutingGroupsInput)
		want error
	}{
		{"scope", func(in *RoutingGroupsInput) { in.Scope = Scope{} }, ErrInvalid},
		{"negative revision", func(in *RoutingGroupsInput) { in.ExpectedRevision = -1 }, ErrInvalid},
		{"nil groups", func(in *RoutingGroupsInput) { in.Groups = nil }, ErrInvalid},
		{"65 groups", func(in *RoutingGroupsInput) {
			for i := 1; i < 65; i++ {
				g := routingTestGroup()
				g.ID = fmt.Sprintf("group/g-%d", i)
				in.Groups = append(in.Groups, g)
			}
		}, ErrInvalid},
		{"duplicate groups", func(in *RoutingGroupsInput) { in.Groups = append(in.Groups, in.Groups[0]) }, ErrInvalid},
		{"empty slug", func(in *RoutingGroupsInput) { in.Groups[0].ID = "group/" }, ErrInvalid},
		{"uppercase slug", func(in *RoutingGroupsInput) { in.Groups[0].ID = "group/A" }, ErrInvalid},
		{"invalid slug", func(in *RoutingGroupsInput) { in.Groups[0].ID = "group/a_b" }, ErrInvalid},
		{"65 slug", func(in *RoutingGroupsInput) { in.Groups[0].ID = "group/" + strings.Repeat("a", 65) }, ErrInvalid},
		{"empty name", func(in *RoutingGroupsInput) { in.Groups[0].Name = " \t" }, ErrInvalid},
		{"101 Unicode name", func(in *RoutingGroupsInput) { in.Groups[0].Name = strings.Repeat("한", 101) }, ErrInvalid},
		{"invalid UTF8", func(in *RoutingGroupsInput) { in.Groups[0].Name = string([]byte{0xff}) }, ErrInvalid},
		{"secret name", func(in *RoutingGroupsInput) { in.Groups[0].Name = "password=hunter2" }, ErrInvalid},
		{"unknown strategy", func(in *RoutingGroupsInput) { in.Groups[0].Strategy = "random" }, ErrInvalid},
		{"discovered row", func(in *RoutingGroupsInput) { in.Groups[0].Discovered = true }, ErrInvalid},
		{"no members", func(in *RoutingGroupsInput) { in.Groups[0].Members = nil }, ErrInvalid},
		{"41 members", func(in *RoutingGroupsInput) { in.Groups[0].Members = make([]string, 41) }, ErrInvalid},
		{"duplicate members", func(in *RoutingGroupsInput) { in.Groups[0].Members = []string{"alpha/shared", "alpha/shared"} }, ErrInvalid},
		{"self recursion", func(in *RoutingGroupsInput) { in.Groups[0].Members = []string{"group/review"} }, ErrInvalid},
		{"other recursion", func(in *RoutingGroupsInput) { in.Groups[0].Members = []string{"group/other"} }, ErrInvalid},
		{"invalid member", func(in *RoutingGroupsInput) { in.Groups[0].Members = []string{"alpha"} }, ErrInvalid},
		{"secret member", func(in *RoutingGroupsInput) { in.Groups[0].Members = []string{"alpha/sk-aaaaaaaaaaaaaaaa"} }, ErrInvalid},
		{"unknown member", func(in *RoutingGroupsInput) { in.Groups[0].Members = []string{"alpha/missing"} }, ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := routingGroupsFixture(t)
			in := RoutingGroupsInput{Scope: scopeA, Groups: []RoutingGroup{routingTestGroup()}}
			tc.edit(&in)
			if _, err := f.svc.SetRoutingGroups(testCtx, f.human, in); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if _, rev, _ := f.svc.routingDoc(testCtx, scopeA); rev != 0 || len(f.repo.rows("audit_events")) != 0 {
				t.Fatal("rejected save wrote routing/audit")
			}
		})
	}
	f := routingGroupsFixture(t)
	g := routingTestGroup()
	g.ID, g.Name, g.Members = "group/"+strings.Repeat("a", 64), strings.Repeat("한", 100), []string{"alpha/shared"}
	groups := []RoutingGroup{g}
	for i := 1; i < 64; i++ {
		g.ID = fmt.Sprintf("group/g-%d", i)
		groups = append(groups, g)
	}
	if page := saveRoutingTestGroups(t, f, groups, false, 0); len(page.Items) != 64 {
		t.Fatal("valid upper boundaries", page)
	}
}

func TestRoutingGroupsPermissionsAndScope(t *testing.T) {
	f := routingGroupsFixture(t)
	in := RoutingGroupsInput{Scope: scopeA, Groups: []RoutingGroup{routingTestGroup()}}
	f.auth.actors["pipeline"] = actor("pipeline-1", "pipeline", true, allVerbs...)
	f.auth.actors["service"] = actor("service-1", "service", true, allVerbs...)
	f.auth.actors["reader"] = actor("reader-1", "human", true, "read")
	f.auth.actors["wrong"] = fakeActor{principal: "wrong", role: "human", verbs: map[string]bool{"read": true, "write": true, "route": true}, wrongScope: true}
	for _, token := range []string{f.agent, "pipeline", "service", "reader", "wrong", "", "unknown"} {
		if _, err := f.svc.SetRoutingGroups(testCtx, token, in); !errors.Is(err, ErrDenied) {
			t.Fatalf("write allowed for %q: %v", token, err)
		}
	}
	for _, token := range []string{"wrong", "", "unknown"} {
		if page, err := f.svc.RoutingGroups(testCtx, token, scopeA); !errors.Is(err, ErrDenied) || page.Items != nil {
			t.Fatalf("read allowed for %q: %+v %v", token, page, err)
		}
	}
	saveRoutingTestGroups(t, f, in.Groups, true, 0)
	for _, scope := range []Scope{scopeB, scopeFar, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: "other"}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, AgentID: "other"}} {
		page, err := f.svc.RoutingGroups(testCtx, f.human, scope)
		if err != nil || len(page.Items) != 0 || page.AutoDiscover || page.Revision != 0 {
			t.Fatal("scope leaked", scope, page, err)
		}
		if _, err = f.svc.DecideRoute(testCtx, f.agent, RoutingDecideInput{Scope: scope, Task: "review", Group: "group/review"}); !errors.Is(err, ErrNotFound) {
			t.Fatal("foreign group routed", scope, err)
		}
		if _, err = f.svc.SetRoutingGroups(testCtx, f.human, RoutingGroupsInput{Scope: scope, Groups: in.Groups}); !errors.Is(err, ErrNotFound) {
			t.Fatal("foreign models saved", scope, err)
		}
	}
}

func TestRoutingGroupsDecisionIsolation(t *testing.T) {
	for _, tc := range []struct {
		name               string
		strategy           string
		auto, failover     bool
		choice             string
		pickerErr          error
		exclude, providers []string
		want               string
		how                string
		fallbacks          []string
		seen               int
	}{
		{"ordered ignores classifier/default/failover order", "ordered", true, true, "gamma/outside", nil, nil, nil, "beta/shared", "fixed", []string{"alpha/shared"}, 0},
		{"smart selects member", "smart", true, true, "alpha/shared", nil, nil, nil, "alpha/shared", "clef", []string{"beta/shared"}, 2},
		{"smart classifier down", "smart", true, true, "alpha/shared", ErrUnavailable, nil, nil, "beta/shared", "default", []string{"alpha/shared"}, 2},
		{"smart rejects outside choice", "smart", true, true, "gamma/outside", nil, nil, nil, "beta/shared", "default", []string{"alpha/shared"}, 2},
		{"smart global routing off", "smart", false, true, "alpha/shared", nil, nil, nil, "beta/shared", "fixed", []string{"alpha/shared"}, 0},
		{"failover off", "ordered", true, false, "", nil, nil, nil, "beta/shared", "fixed", nil, 0},
		{"exclude filters", "ordered", true, true, "", nil, []string{"beta/shared"}, nil, "alpha/shared", "fixed", nil, 0},
		{"provider filters", "smart", true, true, "gamma/outside", nil, nil, []string{"alpha"}, "alpha/shared", "default", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := routingGroupsFixture(t)
			g := routingTestGroup()
			g.Strategy = tc.strategy
			saveRoutingTestGroups(t, f, []RoutingGroup{g}, false, 0)
			outside := "gamma/outside"
			failover := defaultRouting().Failover
			failover.Order = []string{outside, "alpha/shared", "beta/shared"}
			if _, err := f.svc.SetRoutingPolicy(testCtx, f.human, RoutingPolicyInput{Scope: scopeA, ExpectedRevision: 1, DefaultModel: &outside,
				AutoRouting: &tc.auto, AutoFailover: &tc.failover, Failover: &RoutingFailoverInput{RoutingFailover: failover}}); err != nil {
				t.Fatal(err)
			}
			picker := &fakePicker{choice: tc.choice, err: tc.pickerErr}
			f.svc.ModelPicker = picker
			d, err := f.svc.DecideRoute(testCtx, f.agent, RoutingDecideInput{Scope: scopeA, Task: "review", Group: g.ID, Exclude: tc.exclude, Providers: tc.providers})
			if err != nil || d.ID != tc.want || d.Decision != tc.how || d.Group != g.ID || !slices.Equal(d.Fallbacks, tc.fallbacks) || len(picker.seen) != tc.seen {
				t.Fatal("decision", d, picker.seen, err)
			}
			for _, option := range picker.seen {
				if !slices.Contains(g.Members, option.ID) {
					t.Fatal("classifier escaped group", option)
				}
			}
			for _, id := range append(slices.Clone(d.Fallbacks), d.ID) {
				if !slices.Contains(g.Members, id) {
					t.Fatal("route escaped group", id)
				}
			}
		})
	}
	f := routingGroupsFixture(t)
	g := routingTestGroup()
	saveRoutingTestGroups(t, f, []RoutingGroup{g}, false, 0)
	for _, in := range []RoutingDecideInput{
		{Scope: scopeA, Task: "x", Group: g.ID, Providers: []string{"gamma"}},
		{Scope: scopeA, Task: "x", Group: g.ID, Exclude: slices.Clone(g.Members)},
	} {
		if _, err := f.svc.DecideRoute(testCtx, f.agent, in); !errors.Is(err, ErrUnavailable) {
			t.Fatal("empty group escaped", err)
		}
	}
	if _, err := f.svc.DecideRoute(testCtx, "", RoutingDecideInput{Scope: scopeA, Task: "x", Group: g.ID}); !errors.Is(err, ErrDenied) {
		t.Fatal("unauthorized decide", err)
	}
	if _, err := f.svc.DecideRoute(testCtx, f.agent, RoutingDecideInput{Scope: scopeA, Task: "x", Group: "group/missing"}); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown group", err)
	}
	// Omitting group keeps the existing global default and fallback order.
	off, outside := false, "gamma/outside"
	failover := defaultRouting().Failover
	failover.Order = []string{"alpha/shared", "beta/shared"}
	if _, err := f.svc.SetRoutingPolicy(testCtx, f.human, RoutingPolicyInput{Scope: scopeA, ExpectedRevision: 1, AutoRouting: &off, DefaultModel: &outside, Failover: &RoutingFailoverInput{RoutingFailover: failover}}); err != nil {
		t.Fatal(err)
	}
	d, err := f.svc.DecideRoute(testCtx, f.agent, RoutingDecideInput{Scope: scopeA, Task: "x"})
	if err != nil || d.ID != outside || d.Group != "" || !slices.Equal(d.Fallbacks, []string{"alpha/shared", "beta/shared", "alpha/Shared"}) {
		t.Fatal("legacy routing", d, err)
	}
}

func TestRoutingGroupsInactiveAndQuotaFilters(t *testing.T) {
	for _, mode := range []string{"provider disabled", "routing disabled", "quota exhausted"} {
		t.Run(mode, func(t *testing.T) {
			f := routingGroupsFixture(t)
			g := routingTestGroup()
			saveRoutingTestGroups(t, f, []RoutingGroup{g}, true, 0)
			r, _ := f.repo.Get(testCtx, colProviders, scopeA, providersID(scopeA))
			p := *r.Providers
			p.Providers = slices.Clone(p.Providers)
			rev := int64(1)
			switch mode {
			case "provider disabled":
				p.Disabled = []string{"beta/shared"}
			case "routing disabled":
				off := false
				if _, err := f.svc.SetRoutingModel(testCtx, f.human, "beta/shared", RoutingModelInput{Scope: scopeA, ExpectedRevision: rev, RoutingModel: RoutingModel{Enabled: &off}}); err != nil {
					t.Fatal(err)
				}
				rev++
			case "quota exhausted":
				now, zero := time.Now(), 0
				p.Providers[1].Kind = "subscription"
				p.Providers[1].Accounts = []ProviderAccount{{ID: "a", Status: "connected", Default: true, UsageStatus: "ok", UsageCheckedAt: &now, Quotas: []ProviderQuota{{LeftPct: &zero}}}}
			}
			r.Providers, r.Revision = &p, r.Revision+1
			if err := f.repo.Replace(testCtx, colProviders, r, r.Revision-1); err != nil {
				t.Fatal(err)
			}
			d, err := f.svc.DecideRoute(testCtx, f.agent, RoutingDecideInput{Scope: scopeA, Task: "x", Group: g.ID})
			if err != nil || d.ID != "alpha/shared" || len(d.Fallbacks) != 0 {
				t.Fatal("inactive/quota filter", d, err)
			}
			if mode != "quota exhausted" {
				if _, err = f.svc.SetRoutingGroups(testCtx, f.human, RoutingGroupsInput{Scope: scopeA, ExpectedRevision: rev, Groups: []RoutingGroup{g}}); !errors.Is(err, ErrNotFound) {
					t.Fatal("inactive member saved", err)
				}
			}
			if _, err = f.svc.DecideRoute(testCtx, f.agent, RoutingDecideInput{Scope: scopeA, Task: "x", Group: g.ID, Exclude: []string{"alpha/shared"}}); !errors.Is(err, ErrUnavailable) {
				t.Fatal("no eligible member escaped", err)
			}
		})
	}
}

func TestRoutingGroupsMemberAndDecisionLimits(t *testing.T) {
	f := routingGroupsFixture(t)
	r, _ := f.repo.Get(testCtx, colProviders, scopeA, providersID(scopeA))
	p := *r.Providers
	p.Providers = slices.Clone(p.Providers)
	g := routingTestGroup()
	g.Members = nil
	p.Providers[0].Models = nil
	for i := 0; i < 40; i++ {
		model := fmt.Sprintf("m-%02d", i)
		p.Providers[0].Models = append(p.Providers[0].Models, model)
		g.Members = append(g.Members, "alpha/"+model)
	}
	r.Providers, r.Revision = &p, 2
	if err := f.repo.Replace(testCtx, colProviders, r, 1); err != nil {
		t.Fatal(err)
	}
	slices.Reverse(g.Members)
	saveRoutingTestGroups(t, f, []RoutingGroup{g}, false, 0)
	d, err := f.svc.DecideRoute(testCtx, f.agent, RoutingDecideInput{Scope: scopeA, Task: "x", Group: g.ID})
	if err != nil || d.ID != g.Members[0] || len(d.Fallbacks) != routingFallbacks || !slices.Equal(d.Fallbacks, g.Members[1:4]) {
		t.Fatal("40 members/member order/three fallbacks", d, err)
	}
	for _, field := range []string{"exclude", "providers"} {
		in := RoutingDecideInput{Scope: scopeA, Task: "x", Group: g.ID}
		if field == "exclude" {
			in.Exclude = make([]string, 41)
		} else {
			in.Providers = make([]string, 41)
		}
		if _, err = f.svc.DecideRoute(testCtx, f.agent, in); !errors.Is(err, ErrInvalid) {
			t.Fatal("existing input limit changed", field, err)
		}
	}
	g.Strategy = "smart"
	saveRoutingTestGroups(t, f, []RoutingGroup{g}, false, 1)
	for _, safe := range []bool{false, true} {
		picker := &fakePicker{choice: g.Members[39]}
		f.svc.ModelPicker = picker
		f.svc.Auth = authorizerFunc(func(_ context.Context, _ string, req Authorization) (Decision, error) {
			return Decision{Allowed: true, PrincipalID: "agent-1", Role: "agent", Scope: req.Scope, ExternalTransfer: safe, ContentSafe: safe}, nil
		})
		d, err = f.svc.DecideRoute(testCtx, f.agent, RoutingDecideInput{Scope: scopeA, Task: "x", Group: g.ID})
		if err != nil || (!safe && (d.ID != g.Members[0] || len(picker.seen) != 0)) || (safe && (d.ID != g.Members[39] || len(picker.seen) != 40)) {
			t.Fatal("classifier transfer permission/40 options", safe, d, picker.seen, err)
		}
	}
}

// This fake adds commit-time revision comparison; fakeRepo alone permits lost updates.
type routingGroupsCASRepo struct {
	*fakeRepo
	ready  chan struct{}
	commit chan struct{}
}

func (r *routingGroupsCASRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	r.mu.Lock()
	before := r.snapshot()
	next := r.snapshot()
	r.mu.Unlock()
	if err := fn(ctx, fakeTx{store: next}); err != nil {
		return err
	}
	r.ready <- struct{}{}
	<-r.commit
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, row := range next[colRouting] {
		if old := before[colRouting][key]; row.Revision != old.Revision && r.store[colRouting][key].Revision != old.Revision {
			return ErrConflict
		}
	}
	r.store = next
	return nil
}

func TestRoutingGroupsConcurrentReplacement(t *testing.T) {
	for _, initial := range []bool{false, true} {
		t.Run(fmt.Sprint(initial), func(t *testing.T) {
			f := routingGroupsFixture(t)
			rev := int64(0)
			if initial {
				saveRoutingTestGroups(t, f, []RoutingGroup{routingTestGroup()}, false, 0)
				rev = 1
			}
			repo := &routingGroupsCASRepo{fakeRepo: f.repo, ready: make(chan struct{}, 2), commit: make(chan struct{})}
			f.svc.Repo = repo
			other := &Service{Repo: repo, Auth: f.svc.Auth}
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for i, svc := range []*Service{f.svc, other} {
				wg.Add(1)
				go func(i int, svc *Service) {
					defer wg.Done()
					g := routingTestGroup()
					g.Name = fmt.Sprintf("winner-%d", i)
					_, err := svc.SetRoutingGroups(testCtx, f.human, RoutingGroupsInput{Scope: scopeA, ExpectedRevision: rev, Groups: []RoutingGroup{g}})
					results <- err
				}(i, svc)
			}
			for i := 0; i < 2; i++ {
				select {
				case <-repo.ready:
				case <-time.After(5 * time.Second):
					close(repo.commit)
					t.Fatal("transaction barrier timed out")
				}
			}
			close(repo.commit)
			wg.Wait()
			close(results)
			ok, conflicts := 0, 0
			for err := range results {
				if err == nil {
					ok++
				} else if errors.Is(err, ErrConflict) {
					conflicts++
				} else {
					t.Fatal(err)
				}
			}
			page, err := f.svc.RoutingGroups(testCtx, f.human, scopeA)
			if err != nil || ok != 1 || conflicts != 1 || page.Revision != rev+1 || len(page.Items) != 1 {
				t.Fatal("lost update", page, ok, conflicts, err)
			}
		})
	}
}

func TestRoutingGroupsAuditFailureRollsBack(t *testing.T) {
	f := routingGroupsFixture(t)
	saveRoutingTestGroups(t, f, []RoutingGroup{routingTestGroup()}, false, 0)
	before, _ := f.svc.RoutingGroups(testCtx, f.human, scopeA)
	f.repo.failAudit = true
	if _, err := f.svc.SetRoutingGroups(testCtx, f.human, RoutingGroupsInput{Scope: scopeA, ExpectedRevision: 1, Groups: []RoutingGroup{}, AutoDiscover: true}); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	after, err := f.svc.RoutingGroups(testCtx, f.human, scopeA)
	if err != nil || structuredText(before) != structuredText(after) {
		t.Fatal("partial write after audit failure", before, after, err)
	}
}

func TestRoutingGroupsHTTPContract(t *testing.T) {
	f := routingGroupsFixture(t)
	h := Handler(f.svc)
	call := func(method, path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	query := "?workspace_id=ws-a&project_id=proj-a"
	w := call("GET", "/v1/routing/groups"+query, f.human, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) || !strings.Contains(w.Body.String(), `"auto_discover":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
	body := `{"scope":{"workspace_id":"ws-a","project_id":"proj-a"},"expected_revision":0,"groups":[{"id":"group/review","name":"검토","enabled":true,"strategy":"ordered","members":["beta/shared","alpha/shared"]}],"auto_discover":true}`
	w = call("POST", "/v1/routing/groups", f.human, body)
	var page RoutingGroupsPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || page.Revision != 1 || len(page.Items) != 2 || page.Authority != AuthorityDataOnly {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	get := call("GET", "/v1/routing/groups"+query, f.human, "")
	if get.Body.String() != w.Body.String() {
		t.Fatal("POST/GET differ", get.Body.String(), w.Body.String())
	}
	if w = call("POST", "/v1/routing/groups", f.human, body); w.Code != 409 {
		t.Fatal("revision HTTP", w.Code, w.Body.String())
	}
	for _, bad := range []string{
		strings.Replace(body, `"auto_discover":true`, `"auto_discover":null`, 1),
		strings.Replace(body, `,"auto_discover":true`, "", 1),
		strings.Replace(body, `"expected_revision":0,`, "", 1),
		strings.Replace(body, `"groups":[`, `"unknown":1,"groups":[`, 1),
		strings.Replace(body, `"enabled":true`, `"enabled":true,"discovered":true`, 1),
		strings.Replace(body, `"enabled":true`, `"enabled":null`, 1),
		strings.Replace(body, `"enabled":true,`, "", 1),
		body + `{}`,
	} {
		if w = call("POST", "/v1/routing/groups", f.human, bad); w.Code != 400 {
			t.Fatal("invalid HTTP", w.Code, w.Body.String())
		}
	}
	for _, tc := range []struct {
		group string
		code  int
	}{{"group/review", 200}, {"group/missing", 404}} {
		request := fmt.Sprintf(`{"scope":{"workspace_id":"ws-a","project_id":"proj-a"},"task":"x","group":%q}`, tc.group)
		w = call("POST", "/v1/routing/decide", f.agent, request)
		if w.Code != tc.code {
			t.Fatal("group decide HTTP", w.Code, w.Body.String())
		}
		if tc.code == 200 && !strings.Contains(w.Body.String(), `"group":"group/review"`) {
			t.Fatal("missing response group", w.Body.String())
		}
	}
	for _, tc := range []struct {
		method, token string
		code          int
	}{{"GET", "", 403}, {"POST", f.agent, 403}} {
		path := "/v1/routing/groups"
		if tc.method == "GET" {
			path += query
		}
		if w = call(tc.method, path, tc.token, body); w.Code != tc.code {
			t.Fatal("permission HTTP", w.Code, w.Body.String())
		}
	}
	if w = call("POST", "/v1/routing/groups", f.human, `{"scope":{"workspace_id":"ws-a","project_id":"proj-a"},"expected_revision":1,"groups":[],"auto_discover":false}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatal("delete HTTP", w.Code, w.Body.String())
	}
}
