// Package config loads YAML configuration for the agent and the receiver
// and parses human-friendly values such as transfer rates.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Agent is the configuration of the service running next to the ASIAIR.
type Agent struct {
	// ASIAIR is the link to the ASIAIR: "192.168.1.50", "\\192.168.1.50\EMMC Images",
	// "smb://user:pass@192.168.1.50/EMMC Images" or "dir:C:\some\folder".
	ASIAIR string `yaml:"asiair"`
	// Shares used when the link does not name a share. Default: ["EMMC Images"].
	Shares []string `yaml:"shares"`
	// Username/Password for SMB. Empty means guest access (ASIAIR default).
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Domain   string `yaml:"domain"`

	// Extensions to synchronise (lower case, with dot). Empty = all files.
	Extensions []string `yaml:"extensions"`
	// Path prefixes (inside a share) that are skipped, e.g. "Thumbnail".
	Exclude []string `yaml:"exclude"`

	ReceiverURL string `yaml:"receiver_url"`
	Token       string `yaml:"token"`
	// SHA-256 fingerprint of the receiver's self-signed certificate.
	ReceiverFingerprint string `yaml:"receiver_fingerprint"`

	// MaxRate, e.g. "2MB/s", "10Mbit/s", "0" (unlimited). The receiver can override it.
	MaxRate string `yaml:"max_rate"`

	ScanInterval Duration `yaml:"scan_interval"`
	PollInterval Duration `yaml:"poll_interval"`
	// A file is sent only after it has not changed for at least this long.
	MinFileAge Duration `yaml:"min_file_age"`

	StateFile string `yaml:"state_file"`
	LogFile   string `yaml:"log_file"`
}

// Receiver is the configuration of the service on the destination computer.
type Receiver struct {
	// Listen address for the agent API (exposed to the internet).
	Listen string `yaml:"listen"`
	// TLS certificate; when empty a self-signed one is generated in DataDir.
	TLSCert string `yaml:"tls_cert"`
	TLSKey  string `yaml:"tls_key"`
	// DisableTLS serves plain HTTP (only for VPN/LAN use).
	DisableTLS bool   `yaml:"disable_tls"`
	Token      string `yaml:"token"`

	// UIListen is the address of the web page. Default 127.0.0.1:8080.
	UIListen   string `yaml:"ui_listen"`
	UIUser     string `yaml:"ui_user"`
	UIPassword string `yaml:"ui_password"`

	DestDir string `yaml:"dest_dir"`
	DataDir string `yaml:"data_dir"`
	// MaxRate initial value pushed to the agent. Empty = use the agent's own setting.
	MaxRate string `yaml:"max_rate"`
	LogFile string `yaml:"log_file"`
}

// Duration is a time.Duration that unmarshals from strings like "30s".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("bad duration %q: %w", n.Value, err)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) D() time.Duration { return time.Duration(d) }

func load(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// resolve makes p absolute relative to the directory of the config file.
func resolve(base, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(filepath.Dir(base), p)
}

// PeekAgent reads the agent configuration without validating it (for the
// settings form, which must open even when required fields are empty).
func PeekAgent(path string) *Agent {
	c := agentDefaults()
	load(path, c)
	return c
}

func agentDefaults() *Agent {
	return &Agent{
		ScanInterval: Duration(time.Minute),
		PollInterval: Duration(15 * time.Second),
		MinFileAge:   Duration(30 * time.Second),
		MaxRate:      "0",
		StateFile:    "agent-state.json",
		Extensions:   []string{".fit", ".fits", ".fts", ".jpg", ".jpeg", ".png", ".tif", ".tiff", ".xisf"},
		Exclude:      []string{"Thumbnail"},
	}
}

// LoadAgent reads and validates the agent configuration.
func LoadAgent(path string) (*Agent, error) {
	c := agentDefaults()
	if err := load(path, c); err != nil {
		return nil, err
	}
	if c.ASIAIR == "" {
		return nil, errors.New("asiair: не указана ссылка на ASIAIR")
	}
	if c.ReceiverURL == "" {
		return nil, errors.New("receiver_url: не указан адрес приёмника")
	}
	if c.Token == "" {
		return nil, errors.New("token: не указан токен")
	}
	if _, err := ParseRate(c.MaxRate); err != nil {
		return nil, err
	}
	for i, e := range c.Extensions {
		e = strings.ToLower(e)
		if !strings.HasPrefix(e, ".") {
			e = "." + e
		}
		c.Extensions[i] = e
	}
	c.StateFile = resolve(path, c.StateFile)
	c.LogFile = resolve(path, c.LogFile)
	return c, nil
}

// LoadReceiver reads and validates the receiver configuration.
func LoadReceiver(path string) (*Receiver, error) {
	c := &Receiver{
		Listen:   ":8443",
		UIListen: "127.0.0.1:8080",
		DestDir:  "photos",
		DataDir:  "receiver-data",
	}
	if err := load(path, c); err != nil {
		return nil, err
	}
	if c.Token == "" {
		return nil, errors.New("token: не указан токен")
	}
	if c.MaxRate != "" {
		if _, err := ParseRate(c.MaxRate); err != nil {
			return nil, err
		}
	}
	c.DestDir = resolve(path, c.DestDir)
	c.DataDir = resolve(path, c.DataDir)
	c.TLSCert = resolve(path, c.TLSCert)
	c.TLSKey = resolve(path, c.TLSKey)
	c.LogFile = resolve(path, c.LogFile)
	return c, nil
}

// ParseRate parses "2MB/s", "500KB/s", "10Mbit/s", "1.5M", "0", "unlimited"
// into bytes per second. 0 means unlimited. K/M/G are binary (1024) multiples
// for bytes and decimal (1000) for bits, as network speeds are usually quoted.
func ParseRate(s string) (int64, error) {
	orig := s
	s = strings.ToLower(strings.TrimSpace(s))
	bits := false
	if strings.HasSuffix(s, "bps") {
		bits = true
		s = strings.TrimSuffix(s, "bps")
	} else {
		s = strings.TrimSpace(strings.TrimSuffix(s, "/s"))
		if strings.HasSuffix(s, "bit") {
			bits = true
			s = strings.TrimSuffix(s, "bit")
		} else {
			s = strings.TrimSuffix(s, "b")
		}
	}
	s = strings.TrimSpace(s)
	if s == "" || s == "0" || s == "unlimited" || s == "none" {
		return 0, nil
	}
	mult := 1.0
	base := 1024.0
	if bits {
		base = 1000.0
	}
	switch {
	case strings.HasSuffix(s, "k"):
		mult = base
	case strings.HasSuffix(s, "m"):
		mult = base * base
	case strings.HasSuffix(s, "g"):
		mult = base * base * base
	}
	s = strings.TrimRight(s, "kmg")
	s = strings.TrimSpace(s)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("не удалось разобрать скорость %q (пример: 2MB/s, 10Mbit/s, 0)", orig)
	}
	v *= mult
	if bits {
		v /= 8
	}
	return int64(v), nil
}

// FormatRate renders bytes per second for humans.
func FormatRate(bps int64) string {
	if bps <= 0 {
		return "без ограничения"
	}
	return FormatBytes(bps) + "/s"
}

// FormatBytes renders a byte count for humans.
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}
