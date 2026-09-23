package svc

import (
	"context"
	"log"
	"os"
	"time"

	"golang.org/x/sys/windows/svc"
)

// winService runs the program under the Windows service manager.
type winService struct {
	run func(ctx context.Context) error
}

func (h winService) Execute(_ []string, req <-chan svc.ChangeRequest, st chan<- svc.Status) (bool, uint32) {
	st <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.run(ctx) }()
	st <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				st <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				log.Printf("Остановка службы...")
				st <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case <-done:
				case <-time.After(15 * time.Second):
				}
				return false, 0
			}
		case err := <-done:
			if err != nil {
				log.Printf("Ошибка: %v", err)
				// an unexpected end lets the service manager restart us
				os.Exit(1)
			}
			return false, 0
		}
	}
}

// runService runs fn as the Windows service name.
func runService(name string, fn func(ctx context.Context) error) error {
	return svc.Run(name, winService{run: fn})
}
