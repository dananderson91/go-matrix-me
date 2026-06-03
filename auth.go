package matrix

import (
	"context"
	"net/http"
)

// LoginIdentifier identifies the user logging in. See the Matrix spec's
// "Identifier types" (e.g. "m.id.user").
type LoginIdentifier struct {
	Type string `json:"type"`
	User string `json:"user,omitempty"`
}

type LoginRequest struct {
	Type       string          `json:"type"`
	Identifier LoginIdentifier `json:"identifier"`
	Password   string          `json:"password,omitempty"`
}

type LoginResponse struct {
	AccessToken string `json:"access_token"`
	DeviceID    string `json:"device_id"`
	UserID      string `json:"user_id"`
}

// PasswordLogin authenticates with the homeserver using the m.login.password
// flow and stores the resulting access token on the client.
func (c *MatrixClient) PasswordLogin(ctx context.Context, user, pass string) error {
	req := LoginRequest{
		Type: "m.login.password",
		Identifier: LoginIdentifier{
			Type: "m.id.user",
			User: user,
		},
		Password: pass,
	}

	var resp LoginResponse
	if err := c.makeMatrixRequest(ctx, http.MethodPost, c.endpoints.login.String(), req, &resp); err != nil {
		return err
	}

	c.mu.Lock()
	c.accessToken = resp.AccessToken
	c.mu.Unlock()
	return nil
}
