package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultReceiverPort is used when the receiver address has no port.
const DefaultReceiverPort = 8443

// SplitReceiverURL splits "https://host:port" into its parts for a form.
func SplitReceiverURL(u string) (scheme, host string, port int) {
	scheme, port = "https", DefaultReceiverPort
	p, err := url.Parse(strings.TrimSpace(u))
	if err != nil || p.Host == "" {
		return scheme, "", port
	}
	if p.Scheme == "http" {
		scheme = "http"
	}
	host = p.Hostname()
	if n, err := strconv.Atoi(p.Port()); err == nil {
		port = n
	}
	return scheme, host, port
}

// NormalizeReceiverURL adds the default port to a receiver address written
// without one ("https://home.example.ru" → "https://home.example.ru:8443"),
// so the agent goes to the same port the settings form shows.
func NormalizeReceiverURL(u string) string {
	p, err := url.Parse(strings.TrimSpace(u))
	if err != nil || p.Host == "" || p.Port() != "" {
		return strings.TrimSpace(u)
	}
	p.Host = net.JoinHostPort(p.Hostname(), strconv.Itoa(DefaultReceiverPort))
	return p.String()
}

// ReceiverHostPort returns "host:port" of a receiver address.
func ReceiverHostPort(u string) string {
	p, err := url.Parse(NormalizeReceiverURL(u))
	if err != nil {
		return ""
	}
	return p.Host
}

// JoinReceiverURL builds the receiver URL from form fields. The address may
// also be typed as "host:port" or with a scheme; those take precedence.
func JoinReceiverURL(scheme, host string, port int) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", errors.New("не указан адрес приёмника")
	}
	if strings.Contains(host, "://") {
		p, err := url.Parse(host)
		if err != nil || p.Host == "" {
			return "", fmt.Errorf("неверный адрес приёмника %q", host)
		}
		scheme, host = p.Scheme, p.Hostname()
		if n, err := strconv.Atoi(p.Port()); err == nil {
			port = n
		}
	} else if h, p, err := net.SplitHostPort(host); err == nil {
		host = h
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}
	host = strings.Trim(host, "[]/ ")
	if host == "" || strings.ContainsAny(host, "/\\ ") {
		return "", fmt.Errorf("неверный адрес приёмника %q", host)
	}
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("неверный порт %d", port)
	}
	if scheme != "http" {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port)), nil
}

// UpdateAgentFile sets the given keys in the agent YAML file, keeping the
// other settings and comments. The result is validated before it replaces
// the file. A missing file is created.
func UpdateAgentFile(path string, values map[string]string) error {
	return updateFile(path, nil, values, func(p string) error {
		_, err := LoadAgent(p)
		return err
	})
}

// UpdateReceiverFile is UpdateAgentFile for receiver.yaml.
func UpdateReceiverFile(path string, values map[string]string) error {
	return updateFile(path, nil, sealValues(values), func(p string) error {
		_, err := LoadReceiver(p)
		return err
	})
}

// CreateReceiverFile writes a new receiver.yaml from the example text with
// the given values filled in.
func CreateReceiverFile(path string, example []byte, values map[string]string) error {
	return updateFile(path, example, sealValues(values), func(p string) error {
		_, err := LoadReceiver(p)
		return err
	})
}

// updateFile edits the YAML file at path (or base, when given) and replaces
// the file after validate accepts the result.
func updateFile(path string, base []byte, values map[string]string, validate func(string) error) error {
	var doc yaml.Node
	b, err := base, error(nil)
	if b == nil {
		b, err = os.ReadFile(path)
	}
	if err == nil {
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	m := doc.Content[0]
	if m.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: ожидался список параметров", path)
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		// single quotes keep Windows paths like \\host\share literal
		val := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: values[k], Style: yaml.SingleQuotedStyle}
		found := false
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == k {
				old := m.Content[i+1]
				val.LineComment, val.HeadComment, val.FootComment = old.LineComment, old.HeadComment, old.FootComment
				m.Content[i+1] = val
				found = true
				break
			}
		}
		if !found {
			m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}, val)
		}
	}
	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	enc.Close()

	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(sb.String()), 0o644); err != nil {
		return err
	}
	if err := validate(tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	os.Remove(path)
	return os.Rename(tmp, path)
}
