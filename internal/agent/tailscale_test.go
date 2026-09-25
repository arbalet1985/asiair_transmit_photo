package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/receiver"
	"github.com/arbalet1985/asiair_transmit_photo/internal/tsnode"
	"github.com/arbalet1985/asiair_transmit_photo/internal/tsnode/tsnodetest"
)

// TestEndToEndTailscale: the agent reaches the receiver by its name in the
// tailnet, not by the address the receiver listens on.
func TestEndToEndTailscale(t *testing.T) {
	ctrl := tsnodetest.Control(t, false).HTTPTestServer.URL
	tmp := t.TempDir()
	asiair := filepath.Join(tmp, "EMMC Images")
	writeRandom(t, filepath.Join(asiair, "Autorun", "Light", "M31", "Light_M31_0001.fit"), 200_000)

	uiAddr := freeAddr(t)
	listen := freeAddr(t)
	rcfg := &config.Receiver{
		Listen: listen, UIListen: uiAddr, Token: "secret",
		DestDir: filepath.Join(tmp, "dest"), DataDir: filepath.Join(tmp, "data"),
		TailscaleOn: true,
		Tailscale:   config.Tailscale{TailscaleHostname: "asiair-receiver", TailscaleControlURL: ctrl},
	}
	srv, err := receiver.New(rcfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	background(t, func() { srv.Run(ctx) })
	startTransfer(t, uiAddr)

	_, port, _ := net.SplitHostPort(listen)
	acfg := &config.Agent{
		ASIAIR:       "dir:" + asiair,
		ReceiverURL:  "https://asiair-receiver:" + port,
		Token:        "secret",
		Connection:   config.ConnTailscale,
		Tailscale:    config.Tailscale{TailscaleHostname: "asiair-agent", TailscaleControlURL: ctrl},
		TailscaleDir: filepath.Join(tmp, "agent-ts"),
		ScanInterval: config.Duration(300 * time.Millisecond),
		PollInterval: config.Duration(200 * time.Millisecond),
		StateFile:    filepath.Join(tmp, "agent-state.json"),
		Extensions:   []string{".fit"},
	}
	a, err := New(acfg)
	if err != nil {
		t.Fatal(err)
	}
	background(t, func() { a.Run(ctx) })

	dest := filepath.Join(rcfg.DestDir, "EMMC Images", "Autorun", "Light", "M31", "Light_M31_0001.fit")
	waitFor(t, "file over Tailscale", 90*time.Second, func() bool {
		_, err := os.Stat(dest)
		return err == nil
	})
	waitFor(t, "agent Tailscale status", 10*time.Second, func() bool {
		st := a.Tailscale()
		return st != nil && st.State == "running" && st.Name == "asiair-agent"
	})

	// the receiver page shows the Tailscale state and the agent's connection
	var st struct {
		Tailscale struct {
			Enabled bool   `json:"enabled"`
			State   string `json:"state"`
			Name    string `json:"name"`
		} `json:"tailscale"`
		Agent struct {
			Connection string `json:"connection"`
		} `json:"agent"`
	}
	waitFor(t, "receiver Tailscale status", 10*time.Second, func() bool {
		resp, err := http.Get("http://" + uiAddr + "/ui/api/state")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		json.NewDecoder(resp.Body).Decode(&st)
		return st.Tailscale.State == "running" && st.Agent.Connection != ""
	})
	if !st.Tailscale.Enabled || st.Tailscale.State != "running" || st.Tailscale.Name != "asiair-receiver" || st.Agent.Connection != "tailscale" {
		t.Fatalf("page state %+v", st)
	}

	// the page from a phone in the same tailnet, after a password is set
	uiPost := func(body any) {
		t.Helper()
		b, _ := json.Marshal(body)
		resp, err := http.Post("http://"+uiAddr+"/ui/api/remote", "application/json", bytes.NewReader(b))
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("remote settings %v: %v %v", body, resp, err)
		}
		resp.Body.Close()
	}
	uiPost(map[string]string{"action": "password", "password": "звёздное небо"})
	uiPost(map[string]string{"action": "tailscale-on"})
	phone, err := tsnode.Start(tsnode.Config{Dir: filepath.Join(tmp, "phone-ts"), Hostname: "phone", ControlURL: ctrl, Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	defer phone.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second, Transport: &http.Transport{DialContext: phone.Dial}}
	waitFor(t, "login page on port 80 in the tailnet", 90*time.Second, func() bool {
		resp, err := client.Get("http://asiair-receiver/login")
		if err != nil {
			return false
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode == 200 && strings.Contains(string(b), "Пароль")
	})
	resp, err := client.Post("http://asiair-receiver/login", "application/json", strings.NewReader(`{"password":"звёздное небо"}`))
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("login over Tailscale: %v %v", resp, err)
	}
	resp.Body.Close()
	resp, err = client.Get("http://asiair-receiver/ui/api/state")
	if err != nil {
		t.Fatal(err)
	}
	var rs struct {
		Remote struct {
			Remote bool `json:"remote"`
		} `json:"remote_access"`
	}
	json.NewDecoder(resp.Body).Decode(&rs)
	resp.Body.Close()
	if resp.StatusCode != 200 || !rs.Remote.Remote {
		t.Fatalf("state over Tailscale: %d %+v", resp.StatusCode, rs)
	}
	// the test tailnet issues no certificates: the page says how to enable HTTPS
	waitFor(t, "https state", 20*time.Second, func() bool {
		resp, err := http.Get("http://" + uiAddr + "/ui/api/state")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var ls struct {
			Remote struct {
				TSError string `json:"ts_error"`
			} `json:"remote_access"`
		}
		json.NewDecoder(resp.Body).Decode(&ls)
		return strings.Contains(ls.Remote.TSError, "HTTPS")
	})
}
