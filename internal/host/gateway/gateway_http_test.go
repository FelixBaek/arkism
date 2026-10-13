package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/subscription"
)

// Whole HTTP answers recorded from the Python handler with the same fixture, fixed time and ids.
func TestGatewayHTTPGoldens(t *testing.T) {
	raw, err := os.ReadFile("testdata/gateway/http.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Method, Target, Ctype, Want string
		Body                              *string
		Reply                             *struct {
			Status      int
			Ctype, Data string
		}
		Status int
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	listing := []host.Obj{{{Key: "id", Value: "group/test"}, {Key: "label", Value: "테스트"}}, {{Key: "id", Value: "custom-x/m"}},
		{{Key: "id", Value: "google/gemini-2.5-pro"}, {Key: "label", Value: "Gemini"}}}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			f := newFixture(t)
			swap(t, &routingModels, func() []host.Obj { return listing })
			swap(t, &now, func() time.Time { return time.Unix(1700000000, 750_000_000) })
			counter := 0
			swap(t, &newHex, func() string { counter++; return fmt.Sprintf("%032x", counter) })
			if c.Reply != nil {
				f.fallback = &upstreamReply{c.Reply.Status, c.Reply.Ctype, []byte(c.Reply.Data)}
			}
			var body []byte
			if c.Body != nil {
				body = []byte(*c.Body)
			}
			headers := []string{"content-type", "application/json", "authorization", "Bearer caller-secret"}
			if c.Name == "messages_oversized_declared" {
				headers = append(headers, "content-length", strconv.Itoa(subscription.MaxInput+1))
			}
			reply := send(t, c.Method, f.gw.URL+c.Target, body, headers...)
			if reply.err != nil {
				t.Fatal(reply.err)
			}
			if reply.status != c.Status || reply.header.Get("content-type") != c.Ctype || string(reply.body) != c.Want {
				t.Fatalf("got %d %s\n%s\nwant %d %s\n%s", reply.status, reply.header.Get("content-type"), reply.body, c.Status, c.Ctype, c.Want)
			}
		})
	}
}
