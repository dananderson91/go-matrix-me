package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestClient spins up an httptest server with the given handler and returns
// a client pointed at it.
func newTestClient(t *testing.T, handler http.HandlerFunc) (*MatrixClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c, srv
}

func TestPasswordLogin(t *testing.T) {
	var gotPath string
	var gotReq LoginRequest
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		_, _ = w.Write([]byte(`{"access_token":"tok123","user_id":"@alice:hs","device_id":"DEV"}`))
	})

	if err := c.PasswordLogin(context.Background(), "alice", "secret"); err != nil {
		t.Fatalf("PasswordLogin: %v", err)
	}

	if gotPath != "/_matrix/client/v3/login" {
		t.Errorf("login path = %q, want v3 login endpoint", gotPath)
	}
	if gotReq.Type != "m.login.password" || gotReq.Identifier.Type != "m.id.user" || gotReq.Identifier.User != "alice" {
		t.Errorf("unexpected login request: %+v", gotReq)
	}
	if gotReq.Password != "secret" {
		t.Errorf("password not sent")
	}
	if c.token() != "tok123" {
		t.Errorf("access token = %q, want tok123", c.token())
	}
}

func TestRequestsUseBearerAuth(t *testing.T) {
	var gotAuth string
	var gotQuery string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{}`))
	})
	c.accessToken = "tok123"

	if err := c.JoinRoom(context.Background(), "!room:hs"); err != nil {
		t.Fatalf("JoinRoom: %v", err)
	}
	if gotAuth != "Bearer tok123" {
		t.Errorf("Authorization header = %q, want Bearer tok123", gotAuth)
	}
	if gotQuery != "" {
		t.Errorf("access token leaked into query string: %q", gotQuery)
	}
}

func TestJoinRoomPath(t *testing.T) {
	var gotPath, gotMethod string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		_, _ = w.Write([]byte(`{}`))
	})

	if err := c.JoinRoom(context.Background(), "!abc:hs"); err != nil {
		t.Fatalf("JoinRoom: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/_matrix/client/v3/rooms/!abc:hs/join" {
		t.Errorf("join path = %q", gotPath)
	}
}

func TestSendEvent(t *testing.T) {
	var paths []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(`{"event_id":"$evt1"}`))
	})

	msg := MessageEvent{MessageType: "m.text", Body: "hi"}
	id, err := c.SendEvent(context.Background(), "!room:hs", "m.room.message", msg)
	if err != nil {
		t.Fatalf("SendEvent: %v", err)
	}
	if id != "$evt1" {
		t.Errorf("event id = %q, want $evt1", id)
	}

	// A second send must use a distinct transaction ID.
	if _, err := c.SendEvent(context.Background(), "!room:hs", "m.room.message", msg); err != nil {
		t.Fatalf("SendEvent #2: %v", err)
	}
	if len(paths) != 2 || paths[0] == paths[1] {
		t.Errorf("transaction IDs not unique: %v", paths)
	}
}

func TestSyncOnceAdvancesPosition(t *testing.T) {
	var sinceValues []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		sinceValues = append(sinceValues, r.URL.Query().Get("since"))
		_, _ = w.Write([]byte(`{"next_batch":"batch2"}`))
	})

	if _, err := c.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce #1: %v", err)
	}
	if c.nextBatch != "batch2" {
		t.Errorf("nextBatch = %q, want batch2", c.nextBatch)
	}
	if _, err := c.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce #2: %v", err)
	}
	if len(sinceValues) != 2 || sinceValues[0] != "" || sinceValues[1] != "batch2" {
		t.Errorf("since params = %v, want [\"\" \"batch2\"]", sinceValues)
	}
}

func TestSyncOnceErrorPreservesPosition(t *testing.T) {
	fail := false
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"errcode":"M_UNKNOWN","error":"boom"}`))
			return
		}
		_, _ = w.Write([]byte(`{"next_batch":"good"}`))
	})

	if _, err := c.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	fail = true
	if _, err := c.SyncOnce(context.Background()); err == nil {
		t.Fatal("expected error from failing sync")
	}
	if c.nextBatch != "good" {
		t.Errorf("nextBatch = %q, want it preserved as \"good\"", c.nextBatch)
	}
}

func TestMatrixErrorIsStructured(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errcode":"M_FORBIDDEN","error":"nope"}`))
	})

	err := c.JoinRoom(context.Background(), "!room:hs")
	if err == nil {
		t.Fatal("expected error")
	}
	var merr *MatrixError
	if !errors.As(err, &merr) {
		t.Fatalf("error is not *MatrixError: %v", err)
	}
	if merr.Code != "M_FORBIDDEN" || merr.StatusCode != http.StatusForbidden || merr.Message != "nope" {
		t.Errorf("unexpected MatrixError: %+v", merr)
	}
}

func TestContextCancellation(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.JoinRoom(ctx, "!room:hs"); err == nil {
		t.Fatal("expected error from cancelled context")
	}
}
