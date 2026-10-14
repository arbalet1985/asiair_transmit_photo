package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// The passwords and tokens of receiver.yaml are kept encrypted: "dpapi:"
// and the data protected by Windows for this computer (any program on it
// may read them, but not a copy of the file on another computer). Plain
// values written by hand are read too and encrypted at the next start.
const sealPrefix = "dpapi:"

var errNoProtect = errors.New("шифрование есть только в Windows")

// receiverSecrets: the keys of receiver.yaml kept encrypted.
var receiverSecrets = []string{"ui_password", "update_token", "tailscale_authkey", "remote_totp",
	"spacetrack_password", "opensky_secret"}

func isReceiverSecret(key string) bool {
	for _, k := range receiverSecrets {
		if k == key {
			return true
		}
	}
	return false
}

// Seal encrypts a secret; empty stays empty. Where encryption is not
// available the value is kept as it is.
func Seal(v string) string {
	if v == "" || strings.HasPrefix(v, sealPrefix) {
		return v
	}
	b, err := protect([]byte(v))
	if err == errNoProtect {
		return v
	}
	if err != nil {
		log.Printf("Настройки: секрет не зашифрован: %v", err)
		return v
	}
	return sealPrefix + base64.StdEncoding.EncodeToString(b)
}

// Sealed: the value is encrypted (or there is none).
func Sealed(v string) bool { return v == "" || strings.HasPrefix(v, sealPrefix) }

// Unseal decrypts a secret written by Seal ("" when it cannot be).
func Unseal(v string) string { return unseal("секрет", v) }

// unseal decrypts a secret; a plain value is returned as it is. One that
// cannot be decrypted (receiver.yaml copied from another computer) is
// empty: it is to be entered again.
func unseal(key, v string) string {
	if !strings.HasPrefix(v, sealPrefix) {
		return v
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(v, sealPrefix))
	if err == nil {
		b, err = unprotect(b)
	}
	if err != nil {
		log.Printf("Настройки: %s не расшифрован (файл настроек с другого компьютера?) — введите заново: %v", key, err)
		return ""
	}
	return string(b)
}

// unsealSecrets decrypts the secrets of a loaded configuration.
func (c *Receiver) unsealSecrets() {
	for _, f := range []struct {
		key string
		p   *string
	}{
		{"ui_password", &c.UIPassword}, {"update_token", &c.UpdateToken}, {"tailscale_authkey", &c.TailscaleAuthKey},
		{"remote_totp", &c.RemoteTOTP}, {"spacetrack_password", &c.SpaceTrackPassword}, {"opensky_secret", &c.OpenSkySecret},
	} {
		*f.p = unseal(f.key, *f.p)
	}
}

// rawValues: the scalar values of a YAML file as written.
func rawValues(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := map[string]string{}
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out, nil
}

// sealValues: values with the secrets encrypted.
func sealValues(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for k, v := range values {
		if isReceiverSecret(k) {
			v = Seal(v)
		}
		out[k] = v
	}
	return out
}

// SealReceiverFile encrypts the secrets written in receiver.yaml in plain.
func SealReceiverFile(path string) error {
	raw, err := rawValues(path)
	if err != nil {
		return err
	}
	values := map[string]string{}
	for _, k := range receiverSecrets {
		if v, ok := raw[k]; ok && Seal(v) != v {
			values[k] = v
		}
	}
	if len(values) == 0 {
		return nil
	}
	return UpdateReceiverFile(path, values)
}
