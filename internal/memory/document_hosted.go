package memory

// An operator may add external reading services as further readers in the
// file reader chain. Two common protocols are supported:
//
//   - "upload_job" (MEMORY_FILE_PARSER_JOB_URL, _API_KEY): POST {base}/upload
//     with the file as multipart form data answers {"id"}; GET {base}/{id}
//     reports the job status; GET {base}/{id}?expand=markdown_full returns the text.
//   - "presigned_job" (MEMORY_FILE_PARSER_PRESIGNED_URL, _TOKEN, _ORG):
//     POST {base}/org/{org}/files answers {"fileId","uploadUrl"}; the bytes go
//     to uploadUrl with PUT; POST {base}/org/{org}/extraction starts reading
//     and GET {base}/org/{org}/extraction/{id} reports {"ready","data"}.
//
// A reader only runs when the request's or the operator's reader order names
// it, every file passes the per-file transfer check first (destination
// external:file_parser), the person's file name never leaves the server, and
// a status the service calls unsupported hands the file to the next reader.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/httpguard"
)

const (
	parserUploadJob    = "upload_job"
	parserPresignedJob = "presigned_job"
)

// hostedPollInterval and hostedPollTimeout are variables so a test can shorten them.
var (
	hostedPollInterval = 2 * time.Second
	hostedPollTimeout  = 5 * time.Minute
)

// HostedParser reads one file through an external service.
type HostedParser interface {
	Parse(ctx context.Context, ext, mime string, raw []byte) (string, error)
}

// hostedParsersFromEnv reads the configured external readers; none is the default.
func hostedParsersFromEnv() (map[string]HostedParser, error) {
	out := map[string]HostedParser{}
	if base := os.Getenv("MEMORY_FILE_PARSER_JOB_URL"); base != "" {
		if validateEndpoint(base) != nil {
			return nil, fmt.Errorf("MEMORY_FILE_PARSER_JOB_URL: %w", ErrInvalid)
		}
		out[parserUploadJob] = &UploadJobParser{Base: strings.TrimRight(base, "/"), APIKey: os.Getenv("MEMORY_FILE_PARSER_JOB_API_KEY")}
	}
	if base := os.Getenv("MEMORY_FILE_PARSER_PRESIGNED_URL"); base != "" {
		org := os.Getenv("MEMORY_FILE_PARSER_PRESIGNED_ORG")
		if validateEndpoint(base) != nil || !validRunLabel(org, 128) || strings.ContainsAny(org, "/?#") {
			return nil, fmt.Errorf("MEMORY_FILE_PARSER_PRESIGNED_*: %w", ErrInvalid)
		}
		out[parserPresignedJob] = &PresignedJobParser{Base: strings.TrimRight(base, "/"), Org: org, Token: os.Getenv("MEMORY_FILE_PARSER_PRESIGNED_TOKEN")}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// hostedStatusError maps a service status: 400, 415 and 422 mean the service
// cannot read this file, so the next reader may try.
func hostedStatusError(status int) error {
	switch status {
	case http.StatusBadRequest, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
		return fmt.Errorf("file reader: status %d: %w", status, errUnsupportedFormat)
	}
	return dependencyStatusError("file reader", status)
}

// hostedJSON sends one request and decodes a JSON answer.
func hostedJSON(ctx context.Context, client *http.Client, method, endpoint string, header http.Header, body []byte, dest any) error {
	raw, status, err := sendBody(ctx, client, method, endpoint, header, body, defaultResponseMax)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return hostedStatusError(status)
	}
	if dest != nil && json.Unmarshal(raw, dest) != nil {
		return fmt.Errorf("file reader answer: %w", ErrUnavailable)
	}
	return nil
}

// hostedPoll calls check until it says done, the service failed, or the time runs out.
func hostedPoll(ctx context.Context, check func() (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, hostedPollTimeout)
	defer cancel()
	for {
		done, err := check()
		if done || err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("file reader did not finish: %w", ErrUnavailable)
		case <-time.After(hostedPollInterval):
		}
	}
}

func bearerHeader(credential string) http.Header {
	h := http.Header{"Accept": {"application/json"}}
	if credential != "" {
		h.Set("Authorization", "Bearer "+credential)
	}
	return h
}

// UploadJobParser speaks the upload-and-poll protocol.
type UploadJobParser struct {
	Base, APIKey string
	Client       *http.Client
}

func (p *UploadJobParser) Parse(ctx context.Context, ext, mime string, raw []byte) (string, error) {
	client := providerClient(p.Client, 120*time.Second)
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="document`+ext+`"`)
	h.Set("Content-Type", mime)
	part, err := w.CreatePart(h)
	if err == nil {
		_, err = part.Write(raw)
	}
	if err != nil || w.Close() != nil {
		return "", ErrInvalid
	}
	header := bearerHeader(p.APIKey)
	header.Set("Content-Type", w.FormDataContentType())
	var job struct {
		ID string `json:"id"`
	}
	if err := hostedJSON(ctx, client, http.MethodPost, p.Base+"/upload", header, body.Bytes(), &job); err != nil {
		return "", err
	}
	if !validRunLabel(job.ID, 200) || strings.ContainsAny(job.ID, "/?#") {
		return "", fmt.Errorf("file reader job id: %w", ErrUnavailable)
	}
	jobURL := p.Base + "/" + url.PathEscape(job.ID)
	err = hostedPoll(ctx, func() (bool, error) {
		var state struct {
			Status string `json:"status"`
			Job    struct {
				Status string `json:"status"`
			} `json:"job"`
		}
		if err := hostedJSON(ctx, client, http.MethodGet, jobURL, bearerHeader(p.APIKey), nil, &state); err != nil {
			return false, err
		}
		switch strings.ToUpper(state.Job.Status + state.Status) {
		case "COMPLETED", "SUCCESS":
			return true, nil
		case "FAILED", "CANCELLED", "ERROR":
			return false, fmt.Errorf("file reader job failed: %w", ErrInvalid)
		}
		return false, nil
	})
	if err != nil {
		return "", err
	}
	var result struct {
		Markdown string `json:"markdown_full"`
	}
	if err := hostedJSON(ctx, client, http.MethodGet, jobURL+"?expand=markdown_full", bearerHeader(p.APIKey), nil, &result); err != nil {
		return "", err
	}
	return result.Markdown, nil
}

// PresignedJobParser speaks the presigned-upload protocol.
type PresignedJobParser struct {
	Base, Org, Token string
	Client           *http.Client
}

func (p *PresignedJobParser) Parse(ctx context.Context, ext, mime string, raw []byte) (string, error) {
	client := providerClient(p.Client, 120*time.Second)
	org := p.Base + "/org/" + url.PathEscape(p.Org)
	payload, _ := json.Marshal(map[string]string{"name": "document" + ext, "contentType": mime})
	var file struct {
		FileID    string `json:"fileId"`
		UploadURL string `json:"uploadUrl"`
	}
	if err := hostedJSON(ctx, client, http.MethodPost, org+"/files", bearerHeader(p.Token), payload, &file); err != nil {
		return "", err
	}
	if !validRunLabel(file.FileID, 200) || validateEndpoint(file.UploadURL) != nil {
		return "", fmt.Errorf("file reader upload address: %w", ErrUnavailable)
	}
	// The upload address is signed; the service credential never goes there.
	// The service chose that address, so it may reach a private network only
	// when the operator's own service address is on one.
	if err := hostedJSON(ctx, p.uploadClient(), http.MethodPut, file.UploadURL, http.Header{"Content-Type": {mime}}, raw, nil); err != nil {
		return "", err
	}
	payload, _ = json.Marshal(map[string]string{"fileId": file.FileID})
	var started struct {
		ID string `json:"extractionId"`
	}
	if err := hostedJSON(ctx, client, http.MethodPost, org+"/extraction", bearerHeader(p.Token), payload, &started); err != nil {
		return "", err
	}
	if !validRunLabel(started.ID, 200) {
		return "", fmt.Errorf("file reader extraction id: %w", ErrUnavailable)
	}
	var text string
	err := hostedPoll(ctx, func() (bool, error) {
		var state struct {
			Ready bool `json:"ready"`
			Data  struct {
				Success bool   `json:"success"`
				Text    string `json:"text"`
			} `json:"data"`
		}
		if err := hostedJSON(ctx, client, http.MethodGet, org+"/extraction/"+url.PathEscape(started.ID), bearerHeader(p.Token), nil, &state); err != nil {
			return false, err
		}
		if state.Ready && !state.Data.Success {
			return false, fmt.Errorf("file reader extraction failed: %w", ErrInvalid)
		}
		text = state.Data.Text
		return state.Ready, nil
	})
	return text, err
}

// uploadClient sends to the address the service handed back: the injected
// client in tests, else one that refuses private addresses (checked after DNS)
// unless the configured service itself is on a private network.
func (p *PresignedJobParser) uploadClient() *http.Client {
	if p.Client != nil {
		return providerClient(p.Client, 120*time.Second)
	}
	u, _ := url.Parse(p.Base)
	return httpguard.OutboundClient(u != nil && isPrivateNetworkHost(u.Hostname()), 120*time.Second, nil)
}
