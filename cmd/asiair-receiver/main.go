// asiair-receiver runs on the destination computer. It accepts images from
// asiair-agent and serves a web page from which images can be deleted on the
// ASIAIR and the receiver itself is set up.
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

var app = svc.App{
	Name:        "asiair-receiver",
	DisplayName: "ASIAIR Photo Receiver",
	Description: "Принимает снимки с ASIAIR и позволяет удалять их на ASIAIR",
	ConfigName:  "receiver.yaml",
}

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

// prepared is a receiver ready to run.
type prepared struct {
	cfg     *config.Receiver
	srv     *receiver.Server
	page    string // web page address
	created bool   // receiver.yaml was just created
	control *startupControl
}

// prepare creates the configuration on first start and builds the server.
func prepare(path, mode string) (*prepared, error) {
	created, err := receiver.EnsureConfig(path)
	if err != nil {
		return nil, err
	}
	cfg, err := config.LoadReceiver(path)
	if err != nil {
		return nil, err
	}
	s, err := receiver.New(cfg)
	if err != nil {
		return &prepared{cfg: cfg}, err
	}
	p := &prepared{cfg: cfg, srv: s, page: "http://" + localAddr(cfg.UIListen), created: created}
	p.control = &startupControl{cfgPath: path, mode: mode}
	s.Startup = p.control
	return p, nil
}

// run starts the server with the usual log messages.
func (p *prepared) run(ctx context.Context) error {
	if p.created {
		log.Printf("Приёмник: создан файл настроек %s с новым токеном", p.cfg.Path)
	}
	log.Printf("Приёмник: откройте %s — там папка для снимков и данные для подключения агента", p.page)
	return p.srv.Run(ctx)
}

func main() {
	app.Start = func(path string) (func(context.Context) error, string, error) {
		mode := receiver.ModeConsole
		if svc.IsService() {
			mode = receiver.ModeService
		}
		p, err := prepare(path, mode)
		if err != nil {
			logFile := ""
			if p != nil {
				logFile = p.cfg.LogFile
			}
			return nil, logFile, err
		}
		p.control.quit = func() { exitSoon() }
		run := func(ctx context.Context) error {
			if p.created && svc.Interactive() {
				go func() {
					time.Sleep(time.Second)
					svc.OpenBrowser(p.page)
				}()
			}
			return p.run(ctx)
		}
		return run, p.cfg.LogFile, nil
	}
	app.GUI = guiMain
	app.Commands = map[string]svc.Command{
		"token": {Help: "сгенерировать случайный токен для настроек", Run: func(string, []string) error {
			fmt.Println(receiver.NewToken())
			return nil
		}},
		"fingerprint": {Help: "показать отпечаток сертификата для receiver_fingerprint агента", Run: func(path string, _ []string) error {
			p, err := prepare(path, receiver.ModeConsole)
			if err != nil {
				return err
			}
			if p.srv.Fingerprint() == "" {
				fmt.Println("TLS отключён (disable_tls: true)")
				return nil
			}
			fmt.Println(p.srv.Fingerprint())
			return nil
		}},
		"service-handover": {Help: "(служебная) установить службу и передать ей работу", Run: serviceHandover},
		"service-remove":   {Help: "(служебная) остановить и удалить службу", Run: serviceRemove},
	}
	svc.Main(app)
}
