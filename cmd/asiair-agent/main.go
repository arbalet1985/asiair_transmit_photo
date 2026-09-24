// asiair-agent runs on a computer in the observatory network. It copies new
// images from the ASIAIR (SMB share) to the receiver over the internet and
// deletes images on the ASIAIR when asked by the receiver.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/agent"
	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/source"
	"github.com/arbalet1985/asiair_transmit_photo/internal/svc"
)

func main() {
	svc.Main(svc.App{
		Name:        "asiair-agent",
		DisplayName: "ASIAIR Photo Agent",
		Description: "Передаёт снимки с ASIAIR на удалённый компьютер",
		ConfigName:  "agent.yaml",
		Start: func(path string) (func(context.Context) error, string, error) {
			cfg, err := config.LoadAgent(path)
			if err != nil {
				return nil, "", err
			}
			a, err := agent.New(cfg)
			if err != nil {
				return nil, cfg.LogFile, err
			}
			a.Restart = svc.Restart
			return a.Run, cfg.LogFile, nil
		},
		GUI: guiMain,
		Commands: map[string]svc.Command{
			"check": {Help: "проверить подключение к ASIAIR и приёмнику", Run: check},
		},
	})
}

func check(path string, _ []string) error {
	cfg, err := config.LoadAgent(path)
	if err != nil {
		return err
	}
	fmt.Printf("ASIAIR: %s\n", cfg.ASIAIR)
	if cfg.Username == "" {
		fmt.Println("Логин не задан — пробую гостевой вход (так ASIAIR работает по умолчанию)")
	}
	src, err := source.New(cfg.ASIAIR, cfg.Shares, cfg.Username, cfg.Password, cfg.Domain)
	if err != nil {
		return err
	}
	defer src.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var n, sz int64
	perShare := map[string]int{}
	err = src.Walk(ctx, nil, func(f source.File) error {
		n++
		sz += f.Size
		sh, _, _ := source.SplitPath(f.Path)
		perShare[sh]++
		return nil
	})
	if err != nil {
		return fmt.Errorf("ASIAIR: %w", err)
	}
	fmt.Printf("ASIAIR: OK, файлов: %d, объём: %s\n", n, config.FormatBytes(sz))
	for sh, c := range perShare {
		fmt.Printf("  %s: %d\n", sh, c)
	}

	fmt.Printf("Приёмник: %s\n", cfg.ReceiverURL)
	if err := agent.CheckReceiver(ctx, cfg.ReceiverURL, cfg.Token, cfg.ReceiverFingerprint); err != nil {
		return fmt.Errorf("приёмник: %w", err)
	}
	fmt.Println("Приёмник: OK")
	return nil
}
