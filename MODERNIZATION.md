# go-matrix Modernization Audit

Audit date: 2026-06-03. Last commit to this repo: 2016-06-20 (~10 years prior).
Current toolchain on this machine: `go1.26.3`.

The library is small (`client.go`, `auth.go`, `event.go`) and still **compiles and
passes `go vet`** once a module file exists. Nothing here is a "won't build"
emergency, but a decade of language, stdlib, and Matrix-spec drift has left a
number of deprecations, latent bugs, and missing best practices. They are grouped
below by priority.

---

## 1. Build / tooling modernization (do these first)

### 1.1 No `go.mod` — project predates Go modules
The repo uses the old `GOPATH`/`src` layout. Modules have been the default since
Go 1.16, and `go build ./...` fails outside a module:

```
pattern ./...: directory prefix . does not contain main module or its selected dependencies
```

**Action:** add a module file at the repo root.

```sh
go mod init github.com/ericevenchick/go-matrix
go mod tidy
```

This produces `go.mod` with a `go` directive (e.g. `go 1.26`). Commit both
`go.mod` and `go.sum` (the latter will be empty/absent since there are no
external deps — that's fine).

### 1.2 `io/ioutil` is deprecated (since Go 1.16)
`client.go` imports `io/ioutil` and calls `ioutil.ReadAll`.

**Action:** replace with `io.ReadAll`.

```go
// client.go
import (
    "io"      // was "io/ioutil"
    ...
)

body, err := io.ReadAll(resp.Body)   // was ioutil.ReadAll
```

### 1.3 `interface{}` → `any`
Go 1.18 introduced the `any` alias. `client.go` and `event.go` use `interface{}`
in several signatures (`makeMatrixRequest`, `SendEvent`).

**Action:** mechanical rename `interface{}` → `any`. Cosmetic but idiomatic for
modern Go; `gofmt`/`gopls` will suggest it.

---

## 2. Correctness bugs (latent today, will bite eventually)

### 2.1 Unchecked error from `http.NewRequest` → possible nil-pointer panic
`client.go:63-65`:

```go
req, err := http.NewRequest(method, uri, reqBuf)
req.Header.Add("Content-Type", "application/json")   // err not checked yet
resp, err := me.client.Do(req)                        // overwrites err
if err != nil { return err }
```

The error from `NewRequest` is overwritten by `Do`'s error before it is ever
checked. If `NewRequest` fails (e.g. malformed method/URL), `req` is `nil` and
`req.Header.Add` panics.

**Action:** check the error immediately.

```go
req, err := http.NewRequest(method, uri, reqBuf)
if err != nil {
    return err
}
req.Header.Add("Content-Type", "application/json")
resp, err := me.client.Do(req)
if err != nil {
    return err
}
```

### 2.2 `SyncOnce` updates `nextBatch` before checking the error
`event.go:115-119`:

```go
me.nextBatch = response.NextBatch   // runs even on error
if err != nil {
    return Sync{}, err
}
```

On a failed request `response` is the zero `Sync{}`, so `nextBatch` is reset to
`""`. The next sync then re-fetches from the start of the timeline (or behaves as
an initial sync), silently replaying/duplicating events.

**Action:** check `err` first, only assign `nextBatch` on success.

### 2.3 Error detection relies on JSON body, ignores HTTP status & unmarshal error
`client.go:79-83`:

```go
var errResp ErrorResponse
err = json.Unmarshal(body, &errResp)   // err discarded
if errResp.ErrorCode != "" {
    return errors.New(errResp.ErrorMessage)
}
```

Two problems:
- The `Unmarshal` error is assigned to `err` but never checked, so a non-JSON
  error body (proxy error, 502 HTML page, rate-limit text) is silently ignored
  and treated as success.
- `resp.StatusCode` is never inspected. A 4xx/5xx with an empty or unparseable
  body passes through as if it succeeded.

**Action:** check `resp.StatusCode >= 400` and surface a structured error that
preserves `errcode`, the HTTP status, and the message. Consider a custom error
type implementing `error` so callers can `errors.As` it:

```go
type MatrixError struct {
    StatusCode int
    Code       string // errcode
    Message    string
}
func (e *MatrixError) Error() string { ... }
```

### 2.4 `EventContent` JSON tag is wrong: `msg_type` should be `msgtype`
`event.go:75-78`:

```go
type EventContent struct {
    MessageType string `json:"msg_type"`   // WRONG
    Body        string `json:"body"`
}
```

The Matrix spec uses `msgtype` (no underscore). The `MessageEvent` type at
`event.go:13-16` already uses the correct `"msgtype"` tag, so this is an
inconsistency — incoming events never populate `MessageType`.

**Action:** change to `json:"msgtype"`.

### 2.5 Timestamp/age fields should be `int64`
`event.go`: `OriginServerTime int` and `Unsigned.Age int` hold millisecond
values. These are fine on 64-bit but overflow `int` on 32-bit builds (and `int64`
is simply the correct type for ms-since-epoch).

**Action:** use `int64` for `OriginServerTime` and `Age`.

### 2.6 README example does not compile against current code
`README.md`:

```go
c := matrix.NewClient("https://matrix.org")   // NewClient returns (*MatrixClient, error)
```

`NewClient` returns two values, and the return values of `PasswordLogin` /
`JoinRoom` are ignored.

**Action:** update the example to handle the error returns.

---

## 3. Matrix API / protocol drift (functional rot since 2016)

### 3.1 Endpoints pinned to the retired `r0` API version
`client.go:44-46` hardcodes `/_matrix/client/r0/...`. `r0` is the legacy
unstable line; the stable Client-Server API is now `v3`
(`/_matrix/client/v3/...`). Many homeservers still proxy `r0`, but it should not
be relied on.

**Action:** move to `/_matrix/client/v3/` (and consider making the version a
constant for easy bumps).

### 3.2 Access token sent as a URL query parameter (deprecated + insecure)
`JoinRoom`, `SendEvent`, and `SyncOnce` all append `?access_token=...`. The spec
deprecated query-param auth in favor of the `Authorization: Bearer <token>`
header. Query-param tokens leak into server logs, proxy logs, and browser
history.

**Action:** set the header in `makeMatrixRequest` instead of threading the token
through query strings:

```go
if me.accessToken != "" {
    req.Header.Set("Authorization", "Bearer "+me.accessToken)
}
```

Then drop the `access_token` `url.Values` plumbing from the three call sites.

### 3.3 Deprecated login request shape
`auth.go` sends `user` / `medium` / `address` as top-level fields. The modern
`m.login.password` flow nests them in an `identifier` object:

```json
{ "type": "m.login.password",
  "identifier": { "type": "m.id.user", "user": "alice" },
  "password": "..." }
```

`PasswordLogin` also hardcodes `Medium: "email"` regardless of the actual
identifier type.

**Action:** model an `identifier` object; stop hardcoding the medium.

### 3.4 `refresh_token` / `home_server` are legacy fields
`LoginResponse` reads `refresh_token` and `home_server`. The old refresh-token
field was removed; modern refresh tokens come from a different flow, and
`home_server` is deprecated in favor of `well_known`. Low priority unless you
need those values.

---

## 4. Reliability & API design best practices

### 4.1 No `context.Context` support anywhere
None of the exported methods accept a `context.Context`, so callers cannot cancel
or set per-call deadlines. Modern Go HTTP clients are expected to.

**Action:** add `ctx context.Context` as the first parameter to the exported
methods and use `http.NewRequestWithContext(ctx, ...)` in `makeMatrixRequest`.
(This is a breaking API change — appropriate for a major-version bump.)

### 4.2 `http.Client` has no timeout
`client.go:36` uses a bare `http.Client{}`. With `/sync?timeout=10000` and no
client timeout, a hung connection blocks forever.

**Action:** set a sensible `Timeout` (longer than the sync long-poll timeout),
or rely on per-request contexts from 4.1.

### 4.3 `MatrixClient` is not safe for concurrent use
`StartSync` runs a goroutine that mutates `me.nextBatch`; `SendEvent` mutates
`me.transactionID` with a non-atomic `+= 1`. Concurrent calls race.

**Action:** guard mutable state with a `sync.Mutex`, or use `atomic` for the
transaction counter. Document the concurrency contract either way. Run tests with
`-race`.

### 4.4 `StartSync` leaks its goroutine and busy-loops on error
`event.go:123-134`:

```go
go func() {
    for {
        sync, _ := me.SyncOnce()   // error swallowed
        ch <- sync                  // never returns; no stop signal
    }
}()
```

- Errors are discarded (`_`), so a persistently failing sync spins in a tight
  loop hammering the server.
- There is no way to stop the goroutine or the channel — it leaks for the life of
  the process.

**Action:** accept a `context.Context`, `select` on `ctx.Done()`, surface errors
(e.g. a `chan struct{ Sync; error }` or a second error channel), and back off on
failure.

### 4.5 No tests
There is no `*_test.go` file. For an HTTP client, `httptest.Server` makes this
straightforward and would have caught 2.2 and 2.4.

**Action:** add table-driven tests against an `httptest.Server` for login, join,
send, and sync (including the error-path and `nextBatch` behaviors).

---

## 5. Minor cleanups

- **`endpoints.sendEvent` is dead.** Declared in `client.go:27` but never assigned
  or read (`SendEvent` builds its path off `endpoints.room`). Remove it.
- **`Room.GetEvents` is a pointless copy** (`event.go:136-142`). The loop just
  copies the slice element-by-element. Return `me.Timeline.Events` directly (or
  drop the method).
- **No `User-Agent` header** is set on requests. Polite/diagnostic to add one.
- **`makeMatrixRequest` with `nil` body** still calls `json.Marshal(nil)` →
  `"null"`, sending a `null` JSON body on GET/`JoinRoom`. Harmless today but
  cleaner to send no body when `reqIf == nil`.
- **Receiver name `me`** is unconventional Go (vet/golint style prefers a short
  type-derived name like `c`). Optional, but worth a global rename if touching
  these files.

---

## Suggested order of work

1. `go mod init` + `io.ReadAll` (§1.1, §1.2) — unblocks tooling.
2. The four correctness bugs (§2.1–2.4) — small, high-value, low-risk.
3. Add `httptest`-based tests (§4.5) before any behavioral refactor.
4. Auth header + API `v3` (§3.1, §3.2) — protocol correctness.
5. Context + concurrency + `StartSync` rework (§4.1, §4.3, §4.4) — the breaking
   changes; batch them into a major-version release.
6. Login shape, timestamp types, and minor cleanups (§3.3, §2.5, §5).
