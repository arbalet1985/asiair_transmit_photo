package config

import "testing"

func TestParseRate(t *testing.T) {
	cases := map[string]int64{
		"0":         0,
		"":          0,
		"unlimited": 0,
		"1024":      1024,
		"500KB/s":   500 * 1024,
		"2MB/s":     2 << 20,
		"2 MB/s":    2 << 20,
		"1.5M":      3 << 19,
		"10Mbit/s":  1_250_000,
		"10mbps":    1_250_000,
		"100kbit":   12_500,
		"1GB":       1 << 30,
	}
	for in, want := range cases {
		got, err := ParseRate(in)
		if err != nil || got != want {
			t.Errorf("ParseRate(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"fast", "-1MB", "MB/s"} {
		if _, err := ParseRate(bad); err == nil {
			t.Errorf("ParseRate(%q): expected error", bad)
		}
	}
}

func TestExampleConfigs(t *testing.T) {
	a, err := LoadAgent("../../configs/agent.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if a.ASIAIR == "" || a.ScanInterval.D().Minutes() != 1 || len(a.Shares) != 2 {
		t.Fatalf("agent example: %+v", a)
	}
	r, err := LoadReceiver("../../configs/receiver.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if r.Listen != ":8443" || r.UIListen != "127.0.0.1:8080" {
		t.Fatalf("receiver example: %+v", r)
	}
}
