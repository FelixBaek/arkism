package memory

// ServiceAccountBlobStore keeps blob bytes in a bucket of object storage that
// speaks the JSON object API with OAuth service-account keys: the server signs
// a short RS256 assertion with the key file's private key, trades it at the
// key file's token address for a bearer token (cached until shortly before it
// expires), and then uploads, reads, deletes and lists objects. Unlike the S3
// compatible path, which needs separate HMAC keys, this takes the service
// account key file the provider issues. Standard library only.
import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FelixBaek/arkism/internal/httpguard"
)

type ServiceAccountBlobStore struct {
	Endpoint *url.URL
	Bucket   string
	Prefix   string
	Email    string
	TokenURL string
	Scope    string
	Key      *rsa.PrivateKey
	Client   *http.Client
	Now      func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

// serviceAccountKey is the part of a service-account key file used here.
type serviceAccountKey struct {
	Type       string `json:"type"`
	Email      string `json:"client_email"`
	PrivateKey string `json:"private_key"`
	TokenURI   string `json:"token_uri"`
}

// ServiceAccountBlobStoreFromEnv reads MEMORY_BLOB_SERVICE_ACCOUNT_ENDPOINT
// (the JSON object API base URL), _BUCKET, _PREFIX, _SCOPE (the read-write
// storage scope) and _KEY_FILE (the key file path); nil, nil when no endpoint is set.
func ServiceAccountBlobStoreFromEnv() (BlobObjectStore, error) {
	endpoint := os.Getenv("MEMORY_BLOB_SERVICE_ACCOUNT_ENDPOINT")
	if endpoint == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(os.Getenv("MEMORY_BLOB_SERVICE_ACCOUNT_KEY_FILE"))
	if err != nil {
		return nil, fmt.Errorf("blob object storage key file: %w", ErrInvalid)
	}
	return NewServiceAccountBlobStore(endpoint, os.Getenv("MEMORY_BLOB_SERVICE_ACCOUNT_BUCKET"), os.Getenv("MEMORY_BLOB_SERVICE_ACCOUNT_PREFIX"),
		os.Getenv("MEMORY_BLOB_SERVICE_ACCOUNT_SCOPE"), raw)
}

// NewServiceAccountBlobStore checks the settings like NewS3BlobStore; the
// token address in the key file passes the same outbound address check.
func NewServiceAccountBlobStore(endpoint, bucket, prefix, scope string, keyFile []byte) (*ServiceAccountBlobStore, error) {
	if err := checkWebhookURL(endpoint, true); err != nil {
		return nil, fmt.Errorf("blob object storage endpoint: %w", err)
	}
	var k serviceAccountKey
	if json.Unmarshal(keyFile, &k) != nil || k.Type != "service_account" || !validRunLabel(k.Email, 256) || checkWebhookURL(k.TokenURI, true) != nil {
		return nil, fmt.Errorf("blob object storage key file: %w", ErrInvalid)
	}
	block, _ := pem.Decode([]byte(k.PrivateKey))
	if block == nil {
		return nil, fmt.Errorf("blob object storage key file: %w", ErrInvalid)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	key, ok := parsed.(*rsa.PrivateKey)
	u, _ := url.Parse(endpoint)
	if err != nil || !ok || u.RawQuery != "" || !s3HostBucket.MatchString(bucket) || !validRunLabel(scope, 256) || strings.ContainsAny(scope, " \t") ||
		len(prefix) > 256 || !s3NamePattern.MatchString(prefix) || strings.HasPrefix(prefix, "/") || strings.Contains(prefix, "..") {
		return nil, fmt.Errorf("blob object storage settings: %w", ErrInvalid)
	}
	return &ServiceAccountBlobStore{Endpoint: u, Bucket: bucket, Prefix: prefix, Email: k.Email, TokenURL: k.TokenURI, Scope: scope, Key: key,
		Client: httpguard.OutboundClient(true, 10*time.Minute, nil)}, nil
}

func (s *ServiceAccountBlobStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// bearer returns a cached access token or trades a fresh signed assertion for one.
func (s *ServiceAccountBlobStore) bearer(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.token != "" && now.Before(s.expires) {
		return s.token, nil
	}
	assertion, err := s.assertion(now)
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
	header := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}, "Accept": {"application/json"}}
	raw, status, err := sendBody(ctx, s.Client, http.MethodPost, s.TokenURL, header, []byte(form.Encode()), 64<<10)
	if err != nil || status != http.StatusOK {
		return "", ErrUnavailable
	}
	var out struct {
		Token   string `json:"access_token"`
		Expires int64  `json:"expires_in"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Token == "" || out.Expires < 1 {
		return "", ErrUnavailable
	}
	// Renew a minute early so a request never carries an expiring token.
	s.token, s.expires = out.Token, now.Add(time.Duration(out.Expires)*time.Second-time.Minute)
	return s.token, nil
}

// assertion is the RS256-signed token request, valid for one hour.
func (s *ServiceAccountBlobStore) assertion(now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{"iss": s.Email, "scope": s.Scope, "aud": s.TokenURL, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	signing := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.Key, crypto.SHA256, sum[:])
	if err != nil {
		return "", ErrUnavailable
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

func (s *ServiceAccountBlobStore) do(ctx context.Context, method, rawURL string, body io.Reader, size int64) (*http.Response, error) {
	token, err := s.bearer(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, ErrInvalid
	}
	req.ContentLength = size
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	if resp.StatusCode == http.StatusUnauthorized {
		s.mu.Lock()
		if s.token == token {
			s.token = "" // revoked early: the next request trades a fresh assertion
		}
		s.mu.Unlock()
	}
	return resp, nil
}

// objectURL is {endpoint}{base}/b/{bucket}/o[/{key}] with the key escaped as one segment.
func (s *ServiceAccountBlobStore) objectURL(base, key string, query url.Values) string {
	u := strings.TrimSuffix(s.Endpoint.String(), "/") + base + "/b/" + s.Bucket + "/o"
	if key != "" {
		u += "/" + url.PathEscape(key)
	}
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return u
}

func (s *ServiceAccountBlobStore) key(digest string) (string, error) {
	if !sourceHashPattern.MatchString(digest) {
		return "", ErrInvalid
	}
	return s.Prefix + digest, nil
}

func (s *ServiceAccountBlobStore) Put(ctx context.Context, digest string, body io.Reader, size int64) error {
	key, err := s.key(digest)
	if err != nil {
		return err
	}
	resp, err := s.do(ctx, http.MethodPost, s.objectURL("/upload/storage/v1", "", url.Values{"uploadType": {"media"}, "name": {key}}), body, size)
	if err != nil {
		return err
	}
	return s3Status(resp, http.StatusOK)
}

func (s *ServiceAccountBlobStore) Get(ctx context.Context, digest string, max int64) ([]byte, error) {
	key, err := s.key(digest)
	if err != nil {
		return nil, err
	}
	resp, err := s.do(ctx, http.MethodGet, s.objectURL("/storage/v1", key, url.Values{"alt": {"media"}}), nil, 0)
	if err != nil {
		return nil, err
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

func (s *ServiceAccountBlobStore) Delete(ctx context.Context, digest string) error {
	key, err := s.key(digest)
	if err != nil {
		return err
	}
	resp, err := s.do(ctx, http.MethodDelete, s.objectURL("/storage/v1", key, nil), nil, 0)
	if err != nil {
		return err
	}
	if err = s3Status(resp, http.StatusNoContent, http.StatusOK); errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// List pages through the prefix; a name that is not a digest stops the walk.
func (s *ServiceAccountBlobStore) List(ctx context.Context, fn func(string, int64) error) error {
	token := ""
	for {
		q := url.Values{"prefix": {s.Prefix}, "fields": {"items(name,size),nextPageToken"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		var page struct {
			Items []struct {
				Name string `json:"name"`
				Size string `json:"size"`
			} `json:"items"`
			Next string `json:"nextPageToken"`
		}
		if err := s.listPage(ctx, q, &page); err != nil {
			return err
		}
		for _, item := range page.Items {
			digest, ok := strings.CutPrefix(item.Name, s.Prefix)
			size, err := strconv.ParseInt(item.Size, 10, 64)
			if !ok || !sourceHashPattern.MatchString(digest) || err != nil {
				return ErrUnavailable
			}
			if err = fn(digest, size); err != nil {
				return err
			}
		}
		if page.Next == "" {
			return nil
		}
		token = page.Next
	}
}

func (s *ServiceAccountBlobStore) listPage(ctx context.Context, q url.Values, page any) error {
	resp, err := s.do(ctx, http.MethodGet, s.objectURL("/storage/v1", "", q), nil, 0)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return s3Status(resp, http.StatusOK)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil || json.NewDecoder(bytes.NewReader(raw)).Decode(page) != nil {
		return ErrUnavailable
	}
	return nil
}
