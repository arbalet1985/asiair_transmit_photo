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
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
)

// client talks to the receiver.
type client struct {
	base  string
	token string
	http  *http.Client
}

func newClient(base, token, fingerprint string) (*client, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("receiver_url %q: ожидается https://адрес:порт", base)
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if fp := strings.TrimSpace(fingerprint); fp != "" {
		want := normFP(fp)
		// The receiver uses a self-signed certificate: trust exactly that
		// certificate instead of a certificate authority.
		tlsCfg.InsecureSkipVerify = true
		tlsCfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("приёмник не прислал сертификат")
			}
			got := normFP(protocol.Fingerprint(cs.PeerCertificates[0].Raw))
			if got != want {
				return fmt.Errorf("отпечаток сертификата приёмника не совпадает (получен %s)", protocol.Fingerprint(cs.PeerCertificates[0].Raw))
			}
			return nil
		}
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       tlsCfg,
		TLSHandshakeTimeout:   20 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   4,
	}
	return &client{base: u.String(), token: token, http: &http.Client{Transport: tr}}, nil
}

func normFP(s string) string {
	s = strings.ToUpper(s)
	s = strings.NewReplacer(":", "", " ", "", "-", "").Replace(s)
	return s
}

func (c *client) do(ctx context.Context, method, path string, q url.Values, body io.Reader, size int64) (*http.Response, error) {
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
func CheckReceiver(ctx context.Context, receiverURL, token, fingerprint string) error {
	c, err := newClient(receiverURL, token, fingerprint)
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
func FetchFingerprint(ctx context.Context, receiverURL string) (string, error) {
	u, err := url.Parse(receiverURL)
	if err != nil || u.Scheme != "https" {
		return "", errors.New("отпечаток есть только у https-адреса")
	}
	d := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 20 * time.Second},
		Config:    &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
	}
	conn, err := d.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", errors.New("приёмник не прислал сертификат")
	}
	return protocol.Fingerprint(certs[0].Raw), nil
}
