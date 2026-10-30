package memory

import (
	"reflect"
	"testing"
)

func TestNativeSearchShadowHTTPActors(t *testing.T) {
	for _, who := range []string{"human", "agent"} {
		t.Run(who, func(t *testing.T) {
			f := newFixture(t)
			f.activeMemory("searchcanary body", "searchcanary")
			token := f.human
			if who == "agent" {
				token = f.agent
			}
			in := nativeSearchTestInput()
			off := nativeSearchHTTP(t, f.svc, token, in)
			r := nativeSearchTestRuntime(t, "B2")
			f.svc.NativeSearchShadow = r
			on := nativeSearchHTTP(t, f.svc, token, in)
			off.ContextID = ""
			on.ContextID = ""
			if !reflect.DeepEqual(off, on) || len(on.Items) != 1 {
				t.Fatal("HTTP baseline differs")
			}
			got := r.Observations()
			if len(got) != 1 || got[0].Reason != "native_service_role_required" || !got[0].BaselineReturned {
				t.Fatal(got)
			}
		})
	}
}
