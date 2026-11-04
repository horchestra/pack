package logging

import (
	"net/http"
	"time"
)

// Transport logs each request of base (nil — http.DefaultTransport) at trace: its method, its URL without the
// query and the user (where a token may be), the status, how long. No header, no body.
func Transport(component string, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &transport{component: component, base: base}
}

type transport struct {
	component string
	base      http.RoundTripper
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.base.RoundTrip(req)
	l := For(t.component)
	if e := l.Trace(); e.Enabled() {
		u := *req.URL
		u.RawQuery, u.User, u.Fragment = "", nil, ""
		e = e.Str("method", req.Method).Str("url", u.String()).Dur("took", time.Since(start))
		if resp != nil {
			e = e.Int("status", resp.StatusCode)
		}
		e.Err(err).Msg("http")
	}
	return resp, err
}
