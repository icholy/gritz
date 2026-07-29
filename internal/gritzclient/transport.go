package gritzclient

import (
	"net/http"
)

// AuthTransport injects Bearer tokens into requests. When ClientID is
// non-empty, it is also sent as X-Client-ID so the server can tag the
// resulting notifications with the originator's id and the originator
// can filter them back out.
type AuthTransport struct {
	Transport http.RoundTripper
	Token     string
	ClientID  string
}

func (t *AuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.Token)
	if t.ClientID != "" {
		req.Header.Set("X-Client-ID", t.ClientID)
	}
	return t.Transport.RoundTrip(req)
}
