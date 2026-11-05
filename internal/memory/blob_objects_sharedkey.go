package memory

// SharedKeyBlobStore keeps blob bytes in a container of object storage that
// speaks the Shared Key blob REST protocol (x-ms-version 2021-08-06): each
// request is signed with HMAC-SHA256 over its canonical form, standard
// library only, like the S3 client next to it.
import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
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

	"github.com/FelixBaek/arkism/internal/httpguard"
)

const sharedKeyVersion = "2021-08-06"

type SharedKeyBlobStore struct {
	Endpoint  *url.URL
	Account   string
	Container string
	Prefix    string
	Key       []byte
	Client    *http.Client
	Now       func() time.Time
}

// NewSharedKeyBlobStore checks the settings like NewS3BlobStore; key is the
// account key in base64.
func NewSharedKeyBlobStore(endpoint, account, container, prefix, key string) (*SharedKeyBlobStore, error) {
	if err := checkWebhookURL(endpoint, true); err != nil {
		return nil, fmt.Errorf("blob object storage endpoint: %w", err)
	}
	u, _ := url.Parse(endpoint)
	secret, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(secret) == 0 || u.RawQuery != "" || !validRunLabel(account, 64) || !s3NamePattern.MatchString(account) || strings.Contains(account, "/") ||
		!s3HostBucket.MatchString(container) || len(prefix) > 256 || !s3NamePattern.MatchString(prefix) || strings.HasPrefix(prefix, "/") || strings.Contains(prefix, "..") {
		return nil, fmt.Errorf("blob object storage settings: %w", ErrInvalid)
	}
	return &SharedKeyBlobStore{Endpoint: u, Account: account, Container: container, Prefix: prefix, Key: secret,
		Client: httpguard.OutboundClient(true, 10*time.Minute, nil)}, nil
}

// BlobObjectStoreFromEnv picks the object storage the settings name
// (MEMORY_BLOB_S3_* or MEMORY_BLOB_SHAREDKEY_*); nil, nil when neither is set.
func BlobObjectStoreFromEnv() (BlobObjectStore, error) {
	endpoint := os.Getenv("MEMORY_BLOB_SHAREDKEY_ENDPOINT")
	if endpoint == "" {
		return S3BlobStoreFromEnv()
	}
	if os.Getenv("MEMORY_BLOB_S3_ENDPOINT") != "" {
		return nil, fmt.Errorf("blob object storage: set one storage, not both: %w", ErrInvalid)
	}
	return NewSharedKeyBlobStore(endpoint, os.Getenv("MEMORY_BLOB_SHAREDKEY_ACCOUNT"), os.Getenv("MEMORY_BLOB_SHAREDKEY_CONTAINER"),
		os.Getenv("MEMORY_BLOB_SHAREDKEY_PREFIX"), os.Getenv("MEMORY_BLOB_SHAREDKEY_KEY"))
}

func (s *SharedKeyBlobStore) blobURL(key string, query url.Values) *url.URL {
	u := *s.Endpoint
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + s.Container
	if key != "" {
		u.Path += "/" + key
	}
	u.RawPath, u.RawQuery = "", query.Encode()
	return &u
}

func (s *SharedKeyBlobStore) do(ctx context.Context, method string, u *url.URL, body io.Reader, size int64, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = size
	for k, v := range header {
		req.Header[k] = v
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	req.Header.Set("X-Ms-Date", now().UTC().Format(http.TimeFormat))
	req.Header.Set("X-Ms-Version", sharedKeyVersion)
	req.Header.Set("Authorization", "SharedKey "+s.Account+":"+signSharedKey(req, s.Account, s.Key))
	return s.Client.Do(req)
}

// signSharedKey signs the request's canonical string with the account key.
func signSharedKey(req *http.Request, account string, key []byte) string {
	length := ""
	if req.ContentLength > 0 {
		length = strconv.FormatInt(req.ContentLength, 10)
	}
	h := req.Header
	parts := []string{req.Method, h.Get("Content-Encoding"), h.Get("Content-Language"), length, h.Get("Content-MD5"), h.Get("Content-Type"),
		"", h.Get("If-Modified-Since"), h.Get("If-Match"), h.Get("If-None-Match"), h.Get("If-Unmodified-Since"), h.Get("Range")}
	names := []string{}
	for name := range h {
		if strings.HasPrefix(strings.ToLower(name), "x-ms-") {
			names = append(names, strings.ToLower(name))
		}
	}
	slices.Sort(names)
	var b strings.Builder
	b.WriteString(strings.Join(parts, "\n") + "\n")
	for _, name := range names {
		b.WriteString(name + ":" + strings.TrimSpace(h.Get(name)) + "\n")
	}
	b.WriteString("/" + account + req.URL.EscapedPath())
	q := req.URL.Query()
	keys := []string{}
	for k := range q {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		vs := slices.Clone(q[k])
		slices.Sort(vs)
		b.WriteString("\n" + strings.ToLower(k) + ":" + strings.Join(vs, ","))
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(b.String()))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

func (s *SharedKeyBlobStore) key(digest string) (string, error) {
	if !sourceHashPattern.MatchString(digest) {
		return "", ErrInvalid
	}
	return s.Prefix + digest, nil
}

func (s *SharedKeyBlobStore) Put(ctx context.Context, digest string, body io.Reader, size int64) error {
	key, err := s.key(digest)
	if err != nil {
		return err
	}
	resp, err := s.do(ctx, http.MethodPut, s.blobURL(key, nil), body, size, http.Header{"X-Ms-Blob-Type": {"BlockBlob"}})
	if err != nil {
		return ErrUnavailable
	}
	return s3Status(resp, http.StatusCreated)
}

func (s *SharedKeyBlobStore) Get(ctx context.Context, digest string, max int64) ([]byte, error) {
	key, err := s.key(digest)
	if err != nil {
		return nil, err
	}
	resp, err := s.do(ctx, http.MethodGet, s.blobURL(key, nil), nil, 0, nil)
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

func (s *SharedKeyBlobStore) Delete(ctx context.Context, digest string) error {
	key, err := s.key(digest)
	if err != nil {
		return err
	}
	resp, err := s.do(ctx, http.MethodDelete, s.blobURL(key, nil), nil, 0, nil)
	if err != nil {
		return ErrUnavailable
	}
	if err = s3Status(resp, http.StatusAccepted, http.StatusOK); errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

type sharedKeyList struct {
	Blobs []struct {
		Name string `xml:"Name"`
		Size int64  `xml:"Properties>Content-Length"`
	} `xml:"Blobs>Blob"`
	NextMarker string `xml:"NextMarker"`
}

// List pages through the prefix; a name that is not a digest stops the walk.
func (s *SharedKeyBlobStore) List(ctx context.Context, fn func(string, int64) error) error {
	marker := ""
	for {
		q := url.Values{"restype": {"container"}, "comp": {"list"}, "prefix": {s.Prefix}}
		if marker != "" {
			q.Set("marker", marker)
		}
		page, err := s.listPage(ctx, q)
		if err != nil {
			return err
		}
		for _, b := range page.Blobs {
			digest, ok := strings.CutPrefix(b.Name, s.Prefix)
			if !ok || !sourceHashPattern.MatchString(digest) {
				return ErrUnavailable
			}
			if err = fn(digest, b.Size); err != nil {
				return err
			}
		}
		if page.NextMarker == "" {
			return nil
		}
		marker = page.NextMarker
	}
}

func (s *SharedKeyBlobStore) listPage(ctx context.Context, q url.Values) (sharedKeyList, error) {
	var page sharedKeyList
	resp, err := s.do(ctx, http.MethodGet, s.blobURL("", q), nil, 0, nil)
	if err != nil {
		return page, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return page, s3Status(resp, http.StatusOK)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	// The listing may start with a byte order mark.
	if err != nil || xml.Unmarshal(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")), &page) != nil {
		return page, ErrUnavailable
	}
	return page, nil
}
