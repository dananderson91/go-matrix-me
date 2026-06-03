package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// apiPrefix is the base path of the Matrix Client-Server API (v3).
	apiPrefix = "/_matrix/client/v3"

	// userAgent is sent with every request.
	userAgent = "go-matrix-me/2.0"

	// defaultTimeout bounds each HTTP request. It must be comfortably longer
	// than the /sync long-poll timeout (syncTimeoutMS).
	defaultTimeout = 60 * time.Second
)

// MatrixClient is a Matrix.org Client-Server API client. It is safe for
// concurrent use by multiple goroutines.
type MatrixClient struct {
	endpoints endpoints
	client    *http.Client

	// transactionID provides unique, monotonic transaction IDs for sent events.
	transactionID atomic.Int64

	// mu guards the mutable session state below.
	mu          sync.Mutex
	accessToken string
	nextBatch   string
}

type endpoints struct {
	login url.URL
	room  url.URL
	sync  url.URL
}

// MatrixError is returned when the homeserver reports an error, either via a
// Matrix "errcode" in the response body or a non-2xx HTTP status. It preserves
// the errcode, the HTTP status, and the human-readable message so callers can
// inspect them with errors.As.
type MatrixError struct {
	StatusCode int
	Code       string // Matrix errcode, e.g. "M_FORBIDDEN"
	Message    string
}

func (e *MatrixError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("matrix: %s (%s, http %d)", e.Message, e.Code, e.StatusCode)
	}
	return fmt.Sprintf("matrix: http %d: %s", e.StatusCode, e.Message)
}

type errorResponse struct {
	ErrorCode    string `json:"errcode"`
	ErrorMessage string `json:"error"`
}

// NewClient creates a client for the homeserver at the given base URL
// (e.g. "https://matrix.org").
func NewClient(server string) (*MatrixClient, error) {
	u, err := url.Parse(server)
	if err != nil {
		return nil, err
	}
	mkURL := func(p string) url.URL {
		return url.URL{Scheme: u.Scheme, Host: u.Host, Path: apiPrefix + p}
	}
	return &MatrixClient{
		endpoints: endpoints{
			login: mkURL("/login"),
			room:  mkURL("/rooms/"),
			sync:  mkURL("/sync"),
		},
		client: &http.Client{Timeout: defaultTimeout},
	}, nil
}

// token returns the current access token under lock.
func (c *MatrixClient) token() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.accessToken
}

// makeMatrixRequest performs a JSON request against the homeserver. reqIf, if
// non-nil, is marshalled as the request body; respIf, if non-nil, must be a
// pointer into which the response body is unmarshalled.
func (c *MatrixClient) makeMatrixRequest(ctx context.Context, method, uri string, reqIf, respIf any) error {
	var body io.Reader
	if reqIf != nil {
		reqBody, err := json.Marshal(reqIf)
		if err != nil {
			return err
		}
		body = bytes.NewReader(reqBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, uri, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if tok := c.token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	// Surface an error if the server reported one, either via an errcode in
	// the body or a non-2xx status. The body may not be JSON, so the unmarshal
	// error is intentionally ignored here.
	var errResp errorResponse
	_ = json.Unmarshal(respBody, &errResp)
	if errResp.ErrorCode != "" || resp.StatusCode >= http.StatusBadRequest {
		return &MatrixError{
			StatusCode: resp.StatusCode,
			Code:       errResp.ErrorCode,
			Message:    errResp.ErrorMessage,
		}
	}

	if respIf != nil {
		if err := json.Unmarshal(respBody, respIf); err != nil {
			return err
		}
	}
	return nil
}

// JoinRoom joins the room with the given ID or alias.
func (c *MatrixClient) JoinRoom(ctx context.Context, roomID string) error {
	uri := c.endpoints.room
	uri.Path += path.Join(roomID, "join")
	return c.makeMatrixRequest(ctx, http.MethodPost, uri.String(), nil, nil)
}
