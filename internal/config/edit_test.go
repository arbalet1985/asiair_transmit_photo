package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReceiverURL(t *testing.T) {
	s, h, p := SplitReceiverURL("https://home.example.com:9443")
	if s != "https" || h != "home.example.com" || p != 9443 {
		t.Fatalf("split: %s %s %d", s, h, p)
	}
	if _, h, p := SplitReceiverURL(""); h != "" || p != DefaultReceiverPort {
		t.Fatalf("empty: %s %d", h, p)
	}
	cases := []struct {
		scheme, host string
		port         int
		want         string
	}{
		{"https", "home.example.com", 8443, "https://home.example.com:8443"},
		{"https", " 1.2.3.4 ", 443, "https://1.2.3.4:443"},
		{"https", "1.2.3.4:9000", 8443, "https://1.2.3.4:9000"},
		{"https", "http://vpn-host:8080", 8443, "http://vpn-host:8080"},
		{"https", "fe80::1", 8443, "https://[fe80::1]:8443"},
	}
	for _, c := range cases {
		got, err := JoinReceiverURL(c.scheme, c.host, c.port)
		if err != nil || got != c.want {
			t.Errorf("Join(%q,%q,%d) = %q, %v; want %q", c.scheme, c.host, c.port, got, err, c.want)
		}
	}
	for _, bad := range []struct {
		host string
		port int
	}{{"", 8443}, {"a b", 8443}, {"host", 0}, {"host", 70000}} {
		if _, err := JoinReceiverURL("https", bad.host, bad.port); err == nil {
			t.Errorf("Join(%q,%d): expected error", bad.host, bad.port)
		}
	}
}

func TestUpdateAgentFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "agent.yaml")
	b, _ := os.ReadFile("../../configs/agent.example.yaml")
	os.WriteFile(p, b, 0o644)
	err := UpdateAgentFile(p, map[string]string{
		"asiair":       `\\192.168.0.7\EMMC Images`,
		"receiver_url": "https://1.2.3.4:9443",
		"token":        "it's-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := LoadAgent(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.ASIAIR != `\\192.168.0.7\EMMC Images` || c.ReceiverURL != "https://1.2.3.4:9443" || c.Token != "it's-secret" {
		t.Fatalf("values: %+v", c)
	}
	if c.MaxRate != "10MB/s" || len(c.Shares) != 2 {
		t.Fatalf("other settings lost: %+v", c)
	}
	out, _ := os.ReadFile(p)
	if !strings.Contains(string(out), "# Ссылка на ASIAIR") {
		t.Fatal("comments lost")
	}

	// invalid values are refused and the file is kept
	if err := UpdateAgentFile(p, map[string]string{"token": ""}); err == nil {
		t.Fatal("empty token accepted")
	}
	if c, _ := LoadAgent(p); c == nil || c.Token != "it's-secret" {
		t.Fatal("file changed after a refused update")
	}

	// a new file is created
	p2 := filepath.Join(dir, "new.yaml")
	if err := UpdateAgentFile(p2, map[string]string{"asiair": "10.0.0.1", "receiver_url": "https://h:1", "token": "t"}); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeReceiverURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://home.example.ru":      "https://home.example.ru:8443",
		"https://home.example.ru/":     "https://home.example.ru:8443/",
		"https://home.example.ru:9000": "https://home.example.ru:9000",
		"http://10.0.0.5":              "http://10.0.0.5:8443",
		" https://212.104.67.93:8443 ": "https://212.104.67.93:8443",
	} {
		if got := NormalizeReceiverURL(in); got != want {
			t.Errorf("NormalizeReceiverURL(%q) = %q; want %q", in, got, want)
		}
	}
	if got := ReceiverHostPort("https://home.example.ru"); got != "home.example.ru:8443" {
		t.Fatal(got)
	}
}
