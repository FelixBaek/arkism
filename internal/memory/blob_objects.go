package memory

// Blob bytes may live in S3-compatible object storage instead of the local
// blob folder. The folder still holds the lock, the database binding and
// upload staging; only the content-addressed files (named by their sha256)
// move. Requests are signed with Signature Version 4 using the standard
// library, so no storage SDK is needed.
import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// BlobObjectStore keeps blob bytes by digest. Put's body hashes to digest.
type BlobObjectStore interface {
	Put(ctx context.Context, digest string, body io.Reader, size int64) error
	// Get returns the bytes, ErrNotFound when absent, ErrInvalid past max bytes.
	Get(ctx context.Context, digest string, max int64) ([]byte, error)
	// Delete succeeds when the object is already gone.
	Delete(ctx context.Context, digest string) error
	// List calls fn for every stored digest and its size.
	List(ctx context.Context, fn func(digest string, size int64) error) error
}

// S3BlobStore talks to one bucket with path-style URLs.
// shortcut: path-style addressing only; add virtual-host URLs if a provider refuses path-style.
type S3BlobStore struct {
	Endpoint     *url.URL
	Bucket       string
	Region       string
	Prefix       string
	AccessKey    string
	SecretKey    string
	SessionToken string
	Client       *http.Client
	Now          func() time.Time
}

// S3BlobStoreFromEnv reads MEMORY_BLOB_S3_* settings; nil, nil when unset.
func S3BlobStoreFromEnv() (BlobObjectStore, error) {
	endpoint := os.Getenv("MEMORY_BLOB_S3_ENDPOINT")
	if endpoint == "" {
		return nil, nil
	}
	store, err := NewS3BlobStore(endpoint, os.Getenv("MEMORY_BLOB_S3_BUCKET"), os.Getenv("MEMORY_BLOB_S3_REGION"), os.Getenv("MEMORY_BLOB_S3_PREFIX"),
		os.Getenv("MEMORY_BLOB_S3_ACCESS_KEY_ID"), os.Getenv("MEMORY_BLOB_S3_SECRET_ACCESS_KEY"), os.Getenv("MEMORY_BLOB_S3_SESSION_TOKEN"))
	if err != nil {
		return nil, err
	}
	return store, nil
}

// NewS3BlobStore checks the settings; the endpoint is https (or loopback http).
func NewS3BlobStore(endpoint, bucket, region, prefix, accessKey, secretKey, sessionToken string) (*S3BlobStore, error) {
	if err := validateEndpoint(endpoint); err != nil {
		return nil, fmt.Errorf("blob object storage: %w", err)
	}
	u, _ := url.Parse(endpoint)
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil || !validRunLabel(bucket, 63) || strings.ContainsAny(bucket, "/?#") ||
		!validRunLabel(region, 64) || accessKey == "" || secretKey == "" || len(prefix) > 256 || strings.HasPrefix(prefix, "/") || strings.Contains(prefix, "..") {
		return nil, fmt.Errorf("blob object storage settings: %w", ErrInvalid)
	}
	return &S3BlobStore{Endpoint: u, Bucket: bucket, Region: region, Prefix: prefix, AccessKey: accessKey, SecretKey: secretKey, SessionToken: sessionToken,
		Client: &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (s *S3BlobStore) objectURL(key string, query url.Values) *url.URL {
	u := *s.Endpoint
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + s.Bucket
	if key != "" {
		u.Path += "/" + key
	}
	u.RawPath, u.RawQuery = "", query.Encode()
	return &u
}

// do signs and sends one request; payloadHash is the body's sha256 in hex.
func (s *S3BlobStore) do(ctx context.Context, method string, u *url.URL, body io.Reader, size int64, payloadHash string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = size
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if s.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", s.SessionToken)
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	signV4(req, payloadHash, s.AccessKey, s.SecretKey, s.Region, "s3", now())
	return s.Client.Do(req)
}

var emptyPayloadHash = hex.EncodeToString(sha256.New().Sum(nil))

func (s *S3BlobStore) key(digest string) (string, error) {
	if !sourceHashPattern.MatchString(digest) {
		return "", ErrInvalid
	}
	return s.Prefix + digest, nil
}

func (s *S3BlobStore) Put(ctx context.Context, digest string, body io.Reader, size int64) error {
	key, err := s.key(digest)
	if err != nil {
		return err
	}
	resp, err := s.do(ctx, http.MethodPut, s.objectURL(key, nil), body, size, digest)
	if err != nil {
		return ErrUnavailable
	}
	return s3Status(resp, http.StatusOK)
}

func (s *S3BlobStore) Get(ctx context.Context, digest string, max int64) ([]byte, error) {
	key, err := s.key(digest)
	if err != nil {
		return nil, err
	}
	resp, err := s.do(ctx, http.MethodGet, s.objectURL(key, nil), nil, 0, emptyPayloadHash)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, s3Status(resp, http.StatusOK)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, ErrUnavailable
	}
	if int64(len(data)) > max {
		return nil, ErrInvalid
	}
	return data, nil
}

func (s *S3BlobStore) Delete(ctx context.Context, digest string) error {
	key, err := s.key(digest)
	if err != nil {
		return err
	}
	resp, err := s.do(ctx, http.MethodDelete, s.objectURL(key, nil), nil, 0, emptyPayloadHash)
	if err != nil {
		return ErrUnavailable
	}
	if err = s3Status(resp, http.StatusNoContent, http.StatusOK); errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

type s3ListResult struct {
	Contents []struct {
		Key  string `xml:"Key"`
		Size int64  `xml:"Size"`
	} `xml:"Contents"`
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
}

// List pages through the prefix; a key that is not a digest stops the walk.
func (s *S3BlobStore) List(ctx context.Context, fn func(string, int64) error) error {
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {s.Prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		page, err := s.listPage(ctx, q)
		if err != nil {
			return err
		}
		for _, c := range page.Contents {
			digest, ok := strings.CutPrefix(c.Key, s.Prefix)
			if !ok || !sourceHashPattern.MatchString(digest) {
				return ErrUnavailable
			}
			if err = fn(digest, c.Size); err != nil {
				return err
			}
		}
		if !page.IsTruncated || page.NextContinuationToken == "" {
			return nil
		}
		token = page.NextContinuationToken
	}
}

func (s *S3BlobStore) listPage(ctx context.Context, q url.Values) (s3ListResult, error) {
	var page s3ListResult
	resp, err := s.do(ctx, http.MethodGet, s.objectURL("", q), nil, 0, emptyPayloadHash)
	if err != nil {
		return page, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return page, s3Status(resp, http.StatusOK)
	}
	if xml.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&page) != nil {
		return page, ErrUnavailable
	}
	return page, nil
}

// s3Status closes the body of a failed response and maps its status.
func s3Status(resp *http.Response, ok ...int) error {
	if slices.Contains(ok, resp.StatusCode) {
		if resp.Request.Method != http.MethodGet {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
		}
		return nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	return fmt.Errorf("blob object storage status %d: %w", resp.StatusCode, ErrUnavailable)
}

// signV4 adds a Signature Version 4 Authorization header covering the
// host and every header already set on the request.
func signV4(req *http.Request, payloadHash, accessKey, secretKey, region, service string, t time.Time) {
	t = t.UTC()
	amzDate, day := t.Format("20060102T150405Z"), t.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	canonical, signed := canonicalHeaders(req)
	request := strings.Join([]string{req.Method, canonicalURI(req.URL), canonicalQuery(req.URL), canonical, signed, payloadHash}, "\n")
	scope := day + "/" + region + "/" + service + "/aws4_request"
	sum := sha256.Sum256([]byte(request))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	key := []byte("AWS4" + secretKey)
	for _, part := range []string{day, region, service, "aws4_request"} {
		key = hmacSHA256(key, part)
	}
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+hex.EncodeToString(hmacSHA256(key, toSign)))
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// canonicalHeaders returns the canonical header block (with its trailing
// blank line) and the signed header list.
func canonicalHeaders(req *http.Request) (string, string) {
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	values := map[string]string{"host": host}
	for name, vs := range req.Header {
		trimmed := make([]string, len(vs))
		for i, v := range vs {
			trimmed[i] = strings.Join(strings.Fields(v), " ")
		}
		values[strings.ToLower(name)] = strings.Join(trimmed, ",")
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	var b strings.Builder
	for _, name := range names {
		b.WriteString(name + ":" + values[name] + "\n")
	}
	return b.String(), strings.Join(names, ";")
}

// canonicalURI encodes each path segment once, as S3 expects.
func canonicalURI(u *url.URL) string {
	p := u.Path
	if p == "" {
		return "/"
	}
	return sigv4Encode(p, false)
}

func canonicalQuery(u *url.URL) string {
	q := u.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := []string{}
	for _, k := range keys {
		vs := slices.Clone(q[k])
		slices.Sort(vs)
		for _, v := range vs {
			parts = append(parts, sigv4Encode(k, true)+"="+sigv4Encode(v, true))
		}
	}
	return strings.Join(parts, "&")
}

// sigv4Encode percent-encodes all but unreserved characters; slashes too when asked.
func sigv4Encode(s string, slash bool) string {
	var b bytes.Buffer
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' || c == '/' && !slash {
			b.WriteByte(c)
			continue
		}
		b.WriteString("%" + strings.ToUpper(strconv.FormatInt(int64(c)|0x100, 16)[1:]))
	}
	return b.String()
}
