// asiair-receiver runs on the destination computer. It accepts images from
// asiair-agent and serves a web page from which images can be deleted on the ASIAIR.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/receiver"
	"github.com/arbalet1985/asiair_transmit_photo/internal/svc"
)

// localAddr turns a listen address into one a local browser can open.
func localAddr(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func main() {
	svc.Main(svc.App{
		Name:        "asiair-receiver",
		DisplayName: "ASIAIR Photo Receiver",
		Description: "Принимает снимки с ASIAIR и позволяет удалять их на ASIAIR",
		ConfigName:  "receiver.yaml",
		Start: func(path string) (func(context.Context) error, string, error) {
			created, err := receiver.EnsureConfig(path)
			if err != nil {
				return nil, "", err
			}
			cfg, err := config.LoadReceiver(path)
			if err != nil {
				return nil, "", err
			}
			s, err := receiver.New(cfg)
			if err != nil {
				return nil, cfg.LogFile, err
			}
			page := "http://" + localAddr(cfg.UIListen)
			run := func(ctx context.Context) error {
				if created {
					log.Printf("Приёмник: создан файл настроек %s с новым токеном", path)
				}
				log.Printf("Приёмник: откройте %s — там папка для снимков и данные для подключения агента", page)
				if created && svc.Interactive() {
					go func() {
						time.Sleep(time.Second)
						svc.OpenBrowser(page)
					}()
				}
				return s.Run(ctx)
			}
			return run, cfg.LogFile, nil
		},
		Commands: map[string]svc.Command{
			"token": {Help: "сгенерировать случайный токен для настроек", Run: func(string, []string) error {
				fmt.Println(receiver.NewToken())
				return nil
			}},
			"fingerprint": {Help: "показать отпечаток сертификата для receiver_fingerprint агента", Run: func(path string, _ []string) error {
				if _, err := receiver.EnsureConfig(path); err != nil {
					return err
				}
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
