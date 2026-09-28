package poker

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// newClient builds the HTTP client. The timeout is short on purpose: a hung
// request must never wedge the loop, because a wedged loop is silence.
func newClient(o options) *http.Client {
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   o.Timeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          2,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   o.Timeout,
		ExpectContinueTimeout: time.Second,
	}
	if o.Insecure {
		// Opt-in, for a self-signed endpoint. The daemon's own listener
		// carries no secret worth protecting here; the risk is talking to
		// something that is not the daemon.
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	}
	return &http.Client{
		Timeout:   o.Timeout,
		Transport: tr,
		// Redirects are not followed: one poke should be exactly one arrival.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// pinger sends the reassurance.
type pinger struct {
	client *http.Client
	url    string
	// userAgent identifies the client in the daemon's access log. Apart from
	// this header the request carries nothing at all.
	userAgent string
}

// poke performs one request. A non-2xx status is an error: a 200 tells us the
// arrival registered, and anything else means it did not, whatever the reason.
func (p *pinger) poke(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", p.userAgent)
	req.ContentLength = 0

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Drain a little so the connection can be reused. The body carries no
	// meaning; the code is the whole answer.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}
	return nil
}

// run pokes immediately and then every interval, until ctx is cancelled or,
// in -once mode, after the first poke. It returns nil on a clean shutdown.
//
// Request failures are logged and retried; they never end the loop. Only
// cancellation does.
func run(ctx context.Context, p *pinger, o options, log *slog.Logger) error {
	log.Info("poke client starting",
		"url", p.url,
		"interval", o.Interval.String(),
		"timeout", o.Timeout.String(),
		"insecure", o.Insecure,
		"once", o.Once,
		"config", configLabel(o.configPath),
	)

	failures := 0
	poke := func() bool {
		start := time.Now()
		err := p.poke(ctx)
		switch {
		case err == nil:
			failures = 0
			log.Info("reassurance sent", "url", p.url,
				"took", time.Since(start).Round(time.Millisecond).String())
			return true
		case ctx.Err() != nil:
			log.Info("poke abandoned, shutting down", "url", p.url)
			return false
		default:
			// This is the line that matters. If it stops appearing, the
			// machine is on its own and nobody is watching it.
			failures++
			willRetry := !o.Once
			log.Error("reassurance failed",
				"url", p.url,
				"will_retry", willRetry,
				"consecutive_failures", failures,
				"took", time.Since(start).Round(time.Millisecond).String(),
				"err", err)
			return false
		}
	}

	ok := poke()
	if o.Once {
		if !ok {
			return errors.New("poke failed")
		}
		log.Info("single poke complete")
		return nil
	}

	t := time.NewTicker(o.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("poke client stopping")
			return nil
		case <-t.C:
			poke()
		}
	}
}
