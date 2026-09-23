package receiver

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/arbalet1985/asiair_transmit_photo/configs"
	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
)

// placeholderToken is the token value in the example configuration.
const placeholderToken = "ВСТАВЬТЕ_ТОКЕН"

// NewToken returns a random token for the agent API.
func NewToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// EnsureConfig prepares receiver.yaml for the first start: a missing file is
// created from the example with a fresh token, and the example placeholder
// token is replaced. It reports whether anything was written.
func EnsureConfig(path string) (bool, error) {
	_, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, err
		}
		return true, config.CreateReceiverFile(path, configs.ReceiverExample, map[string]string{"token": NewToken()})
	}
	if err != nil {
		return false, err
	}
	c, err := config.LoadReceiver(path)
	if err == nil && c.Token != placeholderToken {
		return false, nil
	}
	// empty or placeholder token: generate one, keep everything else
	if c != nil || errors.Is(err, config.ErrNoToken) {
		return true, config.UpdateReceiverFile(path, map[string]string{"token": NewToken()})
	}
	return false, err
}
