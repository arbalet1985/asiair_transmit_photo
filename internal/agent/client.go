package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
)

// client talks to the receiver.
type client struct {
	base  string
	token string
	http  *http.Client

	mu     sync.Mutex
	pinned string       // normalised fingerprint the certificate must have
	onPin  func(string) // called when an empty fingerprint is learnt on first contact
}

// DialFunc connects to the receiver (Tailscale); nil = the usual network.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

func newClient(base, token, fingerprint string, dial DialFunc) (*client, error) {
	u, err := url.Parse(strings.TrimRight(config.NormalizeReceiverURL(base), "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("receiver_url %q: ожидается https://адрес:порт", base)
	}
	c := &client{base: u.String(), token: token}
	if fp := strings.TrimSpace(fingerprint); fp != "" {
		c.pinned = normFP(fp)
	}
	// The receiver uses a self-signed certificate: trust exactly that
	// certificate instead of a certificate authority. Without a fingerprint
	// in the settings (the receiver was unreachable when they were saved)
	// the certificate of the first contact is trusted and remembered.
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, VerifyConnection: c.verify}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       tlsCfg,
		TLSHandshakeTimeout:   20 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   4,
	}
	if dial != nil {
		tr.Proxy, tr.DialContext = nil, dial
	}
	c.http = &http.Client{Transport: tr}
	return c, nil
}

// closeIdle drops the kept connections (after a network change).
func (c *client) closeIdle() { c.http.CloseIdleConnections() }

func (c *client) verify(cs tls.ConnectionState) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("приёмник не прислал сертификат")
	}
	fp := protocol.Fingerprint(cs.PeerCertificates[0].Raw)
	c.mu.Lock()
	if c.pinned == "" {
		c.pinned = normFP(fp)
		pin := c.onPin
		c.mu.Unlock()
		if pin != nil {
			pin(fp)
		}
		return nil
	}
	want := c.pinned
	c.mu.Unlock()
	if normFP(fp) != want {
		return &FingerprintError{Got: fp}
	}
	return nil
}

// FingerprintError: the receiver presented another certificate than the
// one in the settings (the receiver was reinstalled, or someone is in the
// middle). Got is the fingerprint of the certificate received.
type FingerprintError struct{ Got string }

func (e *FingerprintError) Error() string {
	return "отпечаток сертификата приёмника не совпадает (получен " + e.Got + ")"
}

func normFP(s string) string {
	s = strings.ToUpper(s)
	s = strings.NewReplacer(":", "", " ", "", "-", "").Replace(s)
	return s
}

func (c *client) do(ctx context.Context, method, path string, q url.Values, body io.Reader, size int64) (*http.Response, error) {
	return c.doEncoded(ctx, method, path, q, body, size, "")
}

// doEncoded is do with a body compressed as encoding ("" = not
// compressed); size -1 = not known in advance.
func (c *client) doEncoded(ctx context.Context, method, path string, q url.Values, body io.Reader, size int64, encoding string) (*http.Response, error) {
	u := c.base + path
	if q != nil {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if size >= 0 {
		req.ContentLength = size
	}
	if encoding != "" {
		req.Header.Set("Content-Encoding", encoding)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	return c.http.Do(req)
}

// postJSON posts v and decodes the response into out (if non-nil).
func (c *client) postJSON(ctx context.Context, path string, v, out any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	resp, err := c.do(ctx, http.MethodPost, path, nil, bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := httpErr(resp); err != nil {
		return err
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// HTTPError is a non-2xx answer from the receiver.
type HTTPError struct {
	Code int
	Msg  string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("приёмник ответил %d: %s", e.Code, e.Msg)
}

func httpErr(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return &HTTPError{Code: resp.StatusCode, Msg: strings.TrimSpace(string(b))}
}

// CheckReceiver verifies that the receiver is reachable and accepts the token.
// dial is the Tailscale connection of the running agent, or nil.
func CheckReceiver(ctx context.Context, receiverURL, token, fingerprint string, dial DialFunc) error {
	c, err := newClient(receiverURL, token, fingerprint, dial)
	if err != nil {
		return err
	}
	st := protocol.AgentStatus{Version: protocol.Version}
	var resp protocol.SyncResponse
	err = c.postJSON(ctx, "/api/sync", protocol.SyncRequest{Status: st}, &resp)
	var he *HTTPError
	if errors.As(err, &he) && he.Code == http.StatusUnauthorized {
		return errors.New("приёмник отклонил токен: он должен совпадать с token в receiver.yaml")
	}
	return err
}

// FetchFingerprint connects to an https receiver and returns the SHA-256
// fingerprint of its certificate, so the user can confirm and pin it.
func FetchFingerprint(ctx context.Context, receiverURL string, dial DialFunc) (string, error) {
	u, err := url.Parse(config.NormalizeReceiverURL(receiverURL))
	if err != nil || u.Scheme != "https" {
		return "", errors.New("отпечаток есть только у https-адреса")
	}
	if dial == nil {
		dial = (&net.Dialer{Timeout: 20 * time.Second}).DialContext
	}
	raw, err := dial(ctx, "tcp", u.Host)
	if err != nil {
		return "", err
	}
	conn := tls.Client(raw, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12, ServerName: u.Hostname()})
	defer conn.Close()
	if err := conn.HandshakeContext(ctx); err != nil {
		return "", err
	}
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", errors.New("приёмник не прислал сертификат")
	}
	return protocol.Fingerprint(certs[0].Raw), nil
}
