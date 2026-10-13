package memory

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

// 잘못된 입력은 외부 임베딩 서버를 한 번도 호출하지 않아야 한다.
func TestTEIInputBoundariesWithoutNetwork(t *testing.T) {
	for _, tc := range []struct {
		name, purpose string
		texts         []string
	}{
		{"nil", "query", nil},
		{"empty list", "query", []string{}},
		{"empty text", "query", []string{""}},
		{"unicode whitespace", "query", []string{"\t\n\u2003"}},
		{"invalid UTF8", "document", []string{string([]byte{0xff})}},
		{"invalid second item", "document", []string{"valid", ""}},
		{"over batch ceiling", "query", []string{"a", "b", "c", "d", "e"}},
		{"empty purpose", "", []string{"text"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			embedder := teiForTest(t, "https://tei.invalid/embed", func(c *TEIConfig) {
				c.Client = &http.Client{Transport: systemOneTransport(func(*http.Request) (*http.Response, error) {
					t.Fatal("invalid input reached transport")
					return nil, nil
				})}
			})
			got, err := embedder.Embed(context.Background(), tc.texts, tc.purpose)
			if !errors.Is(err, ErrInvalid) || got != nil {
				t.Fatalf("got %v, error %v; want nil, ErrInvalid", got, err)
			}
		})
	}
}

// JSON 타입·숫자 범위·HTTP 실패를 대역 응답으로 주입하며 부분 결과와 재시도를 금지한다.
func TestTEIResponseFailuresWithoutNetwork(t *testing.T) {
	// 차원은 정상으로 유지해 성분 타입 검증만 실패하게 한다.
	vectorWith := func(component string) string {
		return "[[1," + component + strings.Repeat(",0", teiDimension-2) + "]]"
	}
	for _, tc := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"empty body", "", 200, ErrUnavailable},
		{"invalid JSON", "{", 200, ErrUnavailable},
		{"null", "null", 200, ErrUnavailable},
		{"object instead of vectors", `{ "vectors": [] }`, 200, ErrUnavailable},
		{"string component", vectorWith(`"1"`), 200, ErrUnavailable},
		{"overflow component", vectorWith("1e309"), 200, ErrUnavailable},
		{"null component", vectorWith("null"), 200, ErrUnavailable},
		{"null vector", `[null]`, 200, ErrUnavailable},
		{"trailing JSON", `[[1]] {}`, 200, ErrUnavailable},
		{"rate limit", "secret-provider-message", 429, ErrUnavailable},
		{"server error", "secret-provider-message", 503, ErrUnavailable},
		{"bad request", "secret-provider-message", 400, ErrInvalid},
		{"unauthorized", "secret-provider-message", 401, ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			embedder := teiForTest(t, "https://tei.invalid/embed", func(c *TEIConfig) {
				c.Client = &http.Client{Transport: systemOneTransport(func(*http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
				})}
			})
			got, err := embedder.Embed(context.Background(), []string{"문서"}, "document")
			if !errors.Is(err, tc.want) || got != nil || calls != 1 {
				t.Fatalf("got %d embeddings, error %v, calls %d", len(got), err, calls)
			}
			if strings.Contains(err.Error(), "secret-provider-message") {
				t.Fatal("provider response leaked into error")
			}
		})
	}
}

// 첫 벡터가 정상이더라도 뒤의 벡터가 잘못되면 배치 전체를 거부한다.
func TestTEIPartialBatchFailureWithoutNetwork(t *testing.T) {
	valid := "[1" + strings.Repeat(",0", teiDimension-1) + "]"
	for _, invalid := range []string{"[1]", "[" + strings.Repeat("0,", teiDimension-1) + "0]", "[null" + strings.Repeat(",1", teiDimension-1) + "]"} {
		embedder := teiForTest(t, "https://tei.invalid/embed", func(c *TEIConfig) {
			c.Client = &http.Client{Transport: systemOneTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("[" + valid + "," + invalid + "]"))}, nil
			})}
		})
		got, err := embedder.Embed(context.Background(), []string{"first", "second"}, "document")
		if !errors.Is(err, ErrUnavailable) || got != nil {
			t.Fatalf("partial batch returned %d embeddings, error %v", len(got), err)
		}
	}
}

// 전송 장애와 본문 읽기 실패는 성공 응답이나 빈 임베딩으로 둔갑하면 안 된다.
func TestTEITransportFailuresWithoutNetwork(t *testing.T) {
	for _, reading := range []bool{false, true} {
		t.Run(map[bool]string{false: "transport", true: "body read"}[reading], func(t *testing.T) {
			failure := errors.New("injected failure")
			embedder := teiForTest(t, "https://tei.invalid/embed", func(c *TEIConfig) {
				c.Client = &http.Client{Transport: systemOneTransport(func(*http.Request) (*http.Response, error) {
					if reading {
						return &http.Response{StatusCode: 200, Body: io.NopCloser(iotest.ErrReader(failure))}, nil
					}
					return nil, failure
				})}
			})
			got, err := embedder.Embed(context.Background(), []string{"text"}, "query")
			if got != nil || !errors.Is(err, ErrUnavailable) {
				t.Fatalf("got %v, error %v", got, err)
			}
			if !reading && !errors.Is(err, failure) {
				t.Fatal("transport cause lost")
			}
		})
	}
}

// UTF-8 요청의 정확한 바이트 상한과 최대 배치 4개를 검증한다.
func TestTEIExactRequestLimitWithoutNetwork(t *testing.T) {
	texts := []string{"한글😀", "beta", "gamma", "delta"}
	wantInputs := []string{"query: 한글😀", "query: beta", "query: gamma", "query: delta"}
	encoded, err := json.Marshal(teiRequest{Inputs: wantInputs})
	if err != nil {
		t.Fatal(err)
	}
	response, err := json.Marshal([][]float64{teiVector(math.MaxFloat64), teiVector(-1), teiVector(2), teiVector(3)})
	if err != nil {
		t.Fatal(err)
	}
	for _, below := range []bool{false, true} {
		t.Run(map[bool]string{false: "at limit", true: "one byte over"}[below], func(t *testing.T) {
			calls := 0
			embedder := teiForTest(t, "https://tei.invalid/embed", func(c *TEIConfig) {
				c.MaxRequestBytes = int64(len(encoded))
				if below {
					c.MaxRequestBytes--
				}
				c.Client = &http.Client{Transport: systemOneTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					var body teiRequest
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !reflect.DeepEqual(body.Inputs, wantInputs) {
						t.Fatalf("request %v, error %v", body, err)
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(response)))}, nil
				})}
			})
			got, err := embedder.Embed(context.Background(), texts, "query")
			if below {
				if !errors.Is(err, ErrInvalid) || got != nil || calls != 0 {
					t.Fatalf("oversize request: got %v, error %v, calls %d", got, err, calls)
				}
			} else if err != nil || len(got) != 4 || got[0].Vector[0] != math.MaxFloat64 || calls != 1 {
				t.Fatalf("exact limit: got %v, error %v, calls %d", got, err, calls)
			}
		})
	}
}

// 모델 메타데이터의 256바이트 경계, 제어 문자, nil 설정을 생성 단계에서 검증한다.
func TestTEIMetadataBoundaries(t *testing.T) {
	for _, value := range []string{" ", "model\nrevision", strings.Repeat("x", 257)} {
		for _, field := range []string{"model", "revision"} {
			t.Run(field+"/"+value, func(t *testing.T) {
				cfg := TEIConfig{EndpointURL: "https://tei.invalid/embed", Model: "model", Revision: "revision", Prefixes: map[string]string{"query": "", "document": ""}}
				if field == "model" {
					cfg.Model = value
				} else {
					cfg.Revision = value
				}
				if got, err := NewTEIEmbedder(cfg); got != nil || !errors.Is(err, ErrInvalid) {
					t.Fatalf("got %v, error %v", got, err)
				}
			})
		}
	}
	cfg := TEIConfig{EndpointURL: "https://tei.invalid/embed", Model: strings.Repeat("x", 256), Revision: "r", Prefixes: map[string]string{"query": "", "document": ""}}
	if _, err := NewTEIEmbedder(cfg); err != nil {
		t.Fatalf("256-byte model rejected: %v", err)
	}
	cfg.Prefixes = nil
	if _, err := NewTEIEmbedder(cfg); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil prefixes: %v", err)
	}
}
