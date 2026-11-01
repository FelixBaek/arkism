package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func initialPrefixHashRequest() string {
	return `{"type_name":"StreamInitialPrefixProjectionV2","payload":{"stream_id":"s","clock_domain":"c","scope":{"workspace":"w","project":"p","agent":"a","user":"u"},"dimension":"work_task","config_sha256":"` + strings.Repeat("0", 64) + `"}}`
}

func TestNativeHashStreamKnownCanonicalProjection(t *testing.T) {
	// Independent canonical byte/hash example already fixed in the codec's
	// hand examples, including the fixed domain prefix. The second example
	// changes only the final stream-id byte; neither uses the hash API.
	expected := []string{"f9d550e544c83e4088217c54181830a4b20cbf40b546c3937a5317edc90e42bf", "296ebacc594f1991651492efad192dceac39ec9d98ff73ffdf956a72f6a8f784"}
	var output bytes.Buffer
	request := initialPrefixHashRequest()
	if code := nativeHashStream(strings.NewReader(request+"\n"+strings.Replace(request, `"stream_id":"s"`, `"stream_id":"t"`, 1)+"\n"), &output); code != 0 {
		t.Fatalf("valid stream failed: exit%d %s", code, output.String())
	}
	decoder := json.NewDecoder(&output)
	for _, sha := range expected {
		var result map[string]string
		if err := decoder.Decode(&result); err != nil || result["protocol"] != "exp02-native-hash-v1" || result["status"] != "hashed" || result["sha256"] != sha {
			t.Fatalf("wrong independent hash or protocol: %v %v", result, err)
		}
	}
	if _, err := decoder.Token(); err != io.EOF {
		t.Fatal("stream returned an unexpected record")
	}
}

func TestNativeHashStreamRejectsMalformedAndUnsupportedRecords(t *testing.T) {
	for _, raw := range []string{
		"", "[]", "{", "{}", `{"type_name":"x","payload":`,
		`{"type_name":"x","payload":{},"extra":"raw-canary"}`,
		`{"type_name":"x","type_name":"x","payload":{}}`,
		`{"type_name":false,"payload":{}}`,
		`{"type_name":"ContractErrorV2","payload":{"code":"invalid_json","path":"$"}}`,
		initialPrefixHashRequest() + `{}`,
		strings.Replace(initialPrefixHashRequest(), `"stream_id":"s"`, `"stream_id":"raw-canary","extra":true`, 1),
	} {
		var output bytes.Buffer
		code := nativeHashStream(strings.NewReader(raw), &output)
		var result struct {
			Protocol string          `json:"protocol"`
			Status   string          `json:"status"`
			Error    json.RawMessage `json:"error"`
		}
		if code != 2 || json.Unmarshal(output.Bytes(), &result) != nil || result.Protocol != "exp02-native-hash-v1" || result.Status != "error" || len(result.Error) == 0 || strings.Contains(output.String(), "raw-canary") {
			t.Fatalf("invalid input was accepted or echoed: exit%d %s", code, output.String())
		}
	}
}

func TestNativeHashStreamIOFailures(t *testing.T) {
	var output bytes.Buffer
	if code := nativeHashStream(iotest.ErrReader(io.ErrUnexpectedEOF), &output); code != 2 || !strings.Contains(output.String(), `"status":"error"`) {
		t.Fatal("reader failure became a successful empty stream")
	}
	if code := nativeHashStream(strings.NewReader(initialPrefixHashRequest()), closedWriter{}); code != 2 {
		t.Fatal("writer failure became successful delivery")
	}
}
