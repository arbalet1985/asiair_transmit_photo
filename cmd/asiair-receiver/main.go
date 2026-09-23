// asiair-receiver runs on the destination computer. It accepts images from
// asiair-agent and serves a web page from which images can be deleted on the ASIAIR.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/receiver"
	"github.com/arbalet1985/asiair_transmit_photo/internal/svc"
)

func main() {
	svc.Main(svc.App{
		Name:        "asiair-receiver",
		DisplayName: "ASIAIR Photo Receiver",
		Description: "Принимает снимки с ASIAIR и позволяет удалять их на ASIAIR",
		ConfigName:  "receiver.yaml",
		Start: func(path string) (func(context.Context) error, string, error) {
			cfg, err := config.LoadReceiver(path)
			if err != nil {
				return nil, "", err
			}
			s, err := receiver.New(cfg)
			if err != nil {
				return nil, cfg.LogFile, err
			}
			return s.Run, cfg.LogFile, nil
		},
		Commands: map[string]svc.Command{
			"token": {Help: "сгенерировать случайный токен для настроек", Run: func(string, []string) error {
				b := make([]byte, 24)
				rand.Read(b)
				fmt.Println(base64.RawURLEncoding.EncodeToString(b))
				return nil
			}},
			"fingerprint": {Help: "показать отпечаток сертификата для receiver_fingerprint агента", Run: func(path string, _ []string) error {
				cfg, err := config.LoadReceiver(path)
				if err != nil {
					return err
				}
				s, err := receiver.New(cfg)
				if err != nil {
					return err
				}
				if s.Fingerprint() == "" {
					fmt.Println("TLS отключён (disable_tls: true)")
					return nil
				}
				fmt.Println(s.Fingerprint())
				return nil
			}},
		},
	})
}
