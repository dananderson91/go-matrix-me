# go-matrix-me

A small Go client library for the [Matrix](https://matrix.org) Client-Server API.

> Modernized fork of the original `go-matrix`. It targets the **v3**
> Client-Server API, uses Go modules, and follows current Go conventions
> (`context.Context` on every call, bearer-token auth, structured errors,
> goroutine-safe client). See [`MODERNIZATION.md`](./MODERNIZATION.md) for the
> full audit of what changed and why.

## Features

- Password login (`m.login.password`)
- Joining rooms
- Sending events
- Receiving events via `/sync` (one-shot or streaming)

## Install

```sh
go get github.com/dananderson91/go-matrix-me
```

Requires Go 1.24+.

## Usage

Every network call takes a `context.Context` (for cancellation and timeouts)
and returns an `error`. The access token is sent as an
`Authorization: Bearer` header and never appears in URLs.

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	matrix "github.com/dananderson91/go-matrix-me"
)

func main() {
	ctx := context.Background()

	c, err := matrix.NewClient("https://matrix.org")
	if err != nil {
		log.Fatal(err)
	}

	if err := c.PasswordLogin(ctx, os.Args[1], os.Args[2]); err != nil {
		log.Fatal(err)
	}

	if err := c.JoinRoom(ctx, os.Args[3]); err != nil {
		log.Fatal(err)
	}

	msg := matrix.MessageEvent{MessageType: "m.text", Body: "Hello World!"}
	eventID, err := c.SendEvent(ctx, os.Args[3], "m.room.message", msg)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("sent event:", eventID)
}
```

### Receiving events

`SyncOnce` performs a single long-poll and advances the sync position only on
success:

```go
sync, err := c.SyncOnce(ctx)
if err != nil {
	log.Fatal(err)
}
for roomID, room := range sync.Rooms.Join {
	for _, ev := range room.GetEvents() {
		fmt.Printf("[%s] %s: %s\n", roomID, ev.Sender, ev.Content.Body)
	}
}
```

`StartSync` streams results from a background goroutine until the context is
cancelled. Failed attempts are reported on the channel and retried with
exponential backoff; the channel is closed when the context is done.

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

for res := range c.StartSync(ctx) {
	if res.Err != nil {
		log.Println("sync error:", res.Err)
		continue
	}
	for roomID, room := range res.Sync.Rooms.Join {
		for _, ev := range room.GetEvents() {
			fmt.Printf("[%s] %s: %s\n", roomID, ev.Sender, ev.Content.Body)
		}
	}
}
```

## Error handling

Homeserver errors (and non-2xx responses) are returned as `*MatrixError`,
which preserves the Matrix `errcode`, the HTTP status, and the message:

```go
err := c.JoinRoom(ctx, "!nope:matrix.org")
var merr *matrix.MatrixError
if errors.As(err, &merr) {
	fmt.Println(merr.Code, merr.StatusCode, merr.Message)
}
```

## Concurrency

`MatrixClient` is safe for concurrent use by multiple goroutines.

## Development

```sh
go build ./...
go vet ./...
go test -race ./...
```

## License

See [LICENSE](./LICENSE).
