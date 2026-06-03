package matrix

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"time"
)

// syncTimeoutMS is the long-poll timeout sent to the /sync endpoint.
const syncTimeoutMS = 10000

type SendEventResponse struct {
	EventID string `json:"event_id"`
}

type MessageEvent struct {
	Body        string `json:"body"`
	MessageType string `json:"msgtype"`
}

type Sync struct {
	NextBatch string        `json:"next_batch"`
	Rooms     AllRoomEvents `json:"rooms"`
	Presence  Presence      `json:"presence"`
}
type AllRoomEvents struct {
	Invite map[string]Room `json:"invite"`
	Join   map[string]Room `json:"join"`
	Leave  map[string]Room `json:"leave"`
}
type Room struct {
	AccountData         AccountData              `json:"account_data"`
	State               State                    `json:"state"`
	Timeline            Timeline                 `json:"timeline"`
	Ephemeral           Ephemeral                `json:"ephemeral"`
	UnreadNotifications UnreadNotificationCounts `json:"unread_notifications"`
	InviteState         InviteState              `json:"invite_state"`
}
type Timeline struct {
	Limited       bool    `json:"limited"`
	Events        []Event `json:"events"`
	PreviousBatch string  `json:"prev_batch"`
}
type State struct {
	Events []Event `json:"events"`
}
type AccountData struct {
	Events []Event `json:"events"`
}
type Ephemeral struct {
	Events []Event `json:"events"`
}
type InviteState struct {
	Events []Event `json:"events"`
}
type Presence struct {
	Events []Event `json:"events"`
}
type UnreadNotificationCounts struct {
	HighlightCount    int `json:"highlight_count"`
	NotificationCount int `json:"notification_count"`
}
type Event struct {
	Type             string       `json:"type"`
	ID               string       `json:"event_id"`
	Sender           string       `json:"sender"`
	StateKey         string       `json:"state_key"`
	OriginServerTime int64        `json:"origin_server_ts"`
	Content          EventContent `json:"content"`
	Unsigned         Unsigned     `json:"unsigned"`
}
type Unsigned struct {
	PreviousContent EventContent `json:"prev_content,omitempty"`
	Age             int64        `json:"age"`
	TransactionID   string       `json:"transaction_id"`
}

type EventContent struct {
	MessageType string `json:"msgtype"`
	Body        string `json:"body"`
}

// SendEvent sends an event of the given type to a room and returns the
// server-assigned event ID.
func (c *MatrixClient) SendEvent(ctx context.Context, roomID, eventType string, event any) (string, error) {
	txnID := c.transactionID.Add(1)
	uri := c.endpoints.room
	uri.Path += path.Join(roomID, "send", eventType, fmt.Sprintf("m%d", txnID))

	var response SendEventResponse
	if err := c.makeMatrixRequest(ctx, http.MethodPut, uri.String(), event, &response); err != nil {
		return "", err
	}
	return response.EventID, nil
}

// SyncOnce performs a single /sync long-poll. On success it advances the
// client's sync position so the next call returns only newer events; on error
// the position is left unchanged.
func (c *MatrixClient) SyncOnce(ctx context.Context) (Sync, error) {
	uri := c.endpoints.sync
	params := url.Values{}
	params.Set("timeout", strconv.Itoa(syncTimeoutMS))

	c.mu.Lock()
	since := c.nextBatch
	c.mu.Unlock()
	if since != "" {
		params.Set("since", since)
	}
	uri.RawQuery = params.Encode()

	var response Sync
	if err := c.makeMatrixRequest(ctx, http.MethodGet, uri.String(), nil, &response); err != nil {
		return Sync{}, err
	}

	// Advance the sync position only after a successful sync.
	c.mu.Lock()
	c.nextBatch = response.NextBatch
	c.mu.Unlock()
	return response, nil
}

// SyncResult pairs a /sync response with any error from that attempt.
type SyncResult struct {
	Sync Sync
	Err  error
}

// StartSync repeatedly calls SyncOnce in a background goroutine, delivering
// each result on the returned channel. Failed attempts are reported and then
// retried with exponential backoff. The goroutine runs until ctx is cancelled,
// at which point the channel is closed.
func (c *MatrixClient) StartSync(ctx context.Context) <-chan SyncResult {
	ch := make(chan SyncResult)

	go func() {
		defer close(ch)
		const maxBackoff = 30 * time.Second
		backoff := time.Second

		for {
			if ctx.Err() != nil {
				return
			}

			sync, err := c.SyncOnce(ctx)
			select {
			case ch <- SyncResult{Sync: sync, Err: err}:
			case <-ctx.Done():
				return
			}

			if err != nil {
				select {
				case <-time.After(backoff):
				case <-ctx.Done():
					return
				}
				if backoff < maxBackoff {
					backoff *= 2
				}
				continue
			}
			backoff = time.Second
		}
	}()

	return ch
}

// GetEvents returns the timeline events for the room.
func (r *Room) GetEvents() []Event {
	return r.Timeline.Events
}
