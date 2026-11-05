package memory

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestSignV4Vectors checks the signer against two published examples: the
// generic "get-vanilla" suite case and the object storage GET Object example.
func TestSignV4Vectors(t *testing.T) {
	for _, c := range []struct {
		name, url, region, service, access, secret, payload, when, signed, want string
		header                                                                  map[string]string
	}{
		{name: "get-vanilla", url: "https://example.amazonaws.com/", region: "us-east-1", service: "service",
			access: "AKIDEXAMPLE", secret: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", payload: emptyPayloadHash, when: "20150830T123600Z",
			signed: "host;x-amz-date", want: "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"},
		{name: "get-object", url: "https://examplebucket.s3.amazonaws.com/test.txt", region: "us-east-1", service: "s3",
			access: "AKIAIOSFODNN7EXAMPLE", secret: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", payload: emptyPayloadHash, when: "20130524T000000Z",
			header: map[string]string{"Range": "bytes=0-9", "X-Amz-Content-Sha256": emptyPayloadHash},
			signed: "host;range;x-amz-content-sha256;x-amz-date", want: "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"},
	} {
		req, _ := http.NewRequest(http.MethodGet, c.url, nil)
		for k, v := range c.header {
			req.Header.Set(k, v)
		}
		at, _ := time.Parse("20060102T150405Z", c.when)
		signV4(req, c.payload, c.access, c.secret, c.region, c.service, at)
		got := req.Header.Get("Authorization")
		if !strings.Contains(got, "SignedHeaders="+c.signed+",") || !strings.HasSuffix(got, "Signature="+c.want) {
			t.Fatalf("%s: %s", c.name, got)
		}
	}
	if sigv4Encode("a b/c~", true) != "a%20b%2Fc~" || sigv4Encode("/k e", false) != "/k%20e" {
		t.Fatal("encoding")
	}
}

func TestS3BlobStoreSettings(t *testing.T) {
	for _, c := range [][]string{
		{"http://storage.example", "b", "r", "", "a", "s"},      // plain http off loopback
		{"https://storage.example", "", "r", "", "a", "s"},      // no bucket
		{"https://storage.example", "b", "r", "", "", "s"},      // no key
		{"https://storage.example", "b", "r", "../", "a", "s"},  // escaping prefix
		{"https://169.254.169.254", "b", "r", "", "a", "s"},     // metadata address
		{"https://[fe80::1]", "b", "r", "", "a", "s"},           // link-local
		{"https://storage.example", "b", "r", "a b!", "a", "s"}, // prefix the signed path would encode differently
		{"https://storage.example", "b?x", "r", "", "a", "s"},   // bucket outside the URL-safe alphabet
	} {
		if _, err := NewS3BlobStore(c[0], c[1], c[2], c[3], c[4], c[5], ""); err == nil {
			t.Fatal("accepted", c)
		}
	}
	t.Setenv("MEMORY_BLOB_S3_ENDPOINT", "")
	if store, err := S3BlobStoreFromEnv(); store != nil || err != nil {
		t.Fatal("unset env must keep local storage", store, err)
	}
}
