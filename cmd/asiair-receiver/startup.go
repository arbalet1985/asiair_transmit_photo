package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"runtime"
	"strconv"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/autorun"
	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/receiver"
	"github.com/arbalet1985/asiair_transmit_photo/internal/svc"
)

// runName is the per-user autostart entry of the receiver.
const runName = "ASIAIR Receiver"

// startupControl implements receiver.Startup for the web page.
type startupControl struct {
	cfgPath string
	mode    string
	quit    func() // stops this process (used when handing over to the service)
}

func (c *startupControl) Info() receiver.StartupInfo {
	return receiver.StartupInfo{
		Supported: runtime.GOOS == "windows",
		Mode:      c.mode,
		Autostart: autorun.Enabled(runName),
		Service:   svc.ServiceState(app, c.cfgPath),
	}
}

// autostartArg marks a start from the Windows "Run" key.
const autostartArg = "autostart"

func (c *startupControl) SetAutostart(on bool) error {
	cmd, err := autorun.Command(c.cfgPath, "gui "+autostartArg)
	if err != nil {
		return err
	}
	if err := autorun.Set(runName, cmd, on); err != nil {
		return err
	}
	if on {
		log.Printf("Приёмник: включён запуск при входе в Windows")
	} else {
		log.Printf("Приёмник: выключен запуск при входе в Windows")
	}
	return nil
}

// InstallService starts an elevated copy of the program that installs the
// service, waits for this process to exit and starts the service.
func (c *startupControl) InstallService() error {
	if c.mode == receiver.ModeService {
		return errors.New("приёмник уже работает как служба")
	}
	if err := autorun.RunElevated("-config", c.cfgPath, "service-handover", strconv.Itoa(os.Getpid())); err != nil {
		return err
	}
	log.Printf("Приёмник: устанавливается служба Windows…")
	go func() {
		for i := 0; i < 120; i++ {
			time.Sleep(500 * time.Millisecond)
			if svc.ServiceState(app, c.cfgPath) != svc.ServiceNotInstalled {
				log.Printf("Приёмник: служба установлена, передаю ей работу")
				autorun.Set(runName, "", false) // the service replaces starting at logon
				time.Sleep(time.Second)         // let the page see the answer
				if c.quit != nil {
					c.quit()
				}
				return
			}
		}
		log.Printf("Приёмник: служба так и не установилась (см. журнал)")
	}()
	return nil
}

// RemoveService deletes the service. The service can remove itself (it runs
// with system rights); otherwise administrator rights are requested.
func (c *startupControl) RemoveService() error {
	if c.mode != receiver.ModeService {
		return autorun.RunElevated("-config", c.cfgPath, "service-remove")
	}
	if err := svc.ServiceControl(app, c.cfgPath, "uninstall"); err != nil {
		return err
	}
	log.Printf("Приёмник: служба удалена, приёмник останавливается")
	exitSoon()
	return nil
}

// exitSoon ends the process after the current web request has been answered.
func exitSoon() {
	go func() {
		time.Sleep(time.Second)
		os.Exit(0)
	}()
}

// helperLog sends the log of the hidden elevated helper to the receiver log.
func helperLog(cfgPath string) {
	logFile := svc.DefaultLogFile(app.Name)
	if cfg, err := config.LoadReceiver(cfgPath); err == nil && cfg.LogFile != "" {
		logFile = cfg.LogFile
	}
	if f, err := svc.OpenLog(logFile); err == nil {
		log.SetOutput(f)
	}
	log.SetFlags(log.Ldate | log.Ltime)
}

// serviceHandover runs elevated: install the service, wait for the process
// that asked for it to exit (it holds the ports), then start the service.
func serviceHandover(cfgPath string, args []string) error {
	helperLog(cfgPath)
	if svc.ServiceState(app, cfgPath) == svc.ServiceNotInstalled {
		if err := svc.ServiceControl(app, cfgPath, "install"); err != nil {
			log.Printf("Приёмник: не удалось установить службу: %v", err)
			return err
		}
	}
	// the service replaces starting at logon
	autorun.Set(runName, "", false)
	if len(args) > 0 {
		if pid, err := strconv.Atoi(args[0]); err == nil {
			autorun.WaitExit(pid, time.Minute)
		}
	}
	if err := svc.ServiceControl(app, cfgPath, "start"); err != nil {
		log.Printf("Приёмник: служба установлена, но не запустилась: %v", err)
		return err
	}
	log.Printf("Приёмник: установлен и запущен как служба Windows")
	return nil
}

// serviceRemove runs elevated: stop and delete the service.
func serviceRemove(cfgPath string, _ []string) error {
	helperLog(cfgPath)
	if svc.ServiceState(app, cfgPath) == svc.ServiceRunning {
		svc.ServiceControl(app, cfgPath, "stop")
	}
	if err := svc.ServiceControl(app, cfgPath, "uninstall"); err != nil {
		log.Printf("Приёмник: не удалось удалить службу: %v", err)
		return fmt.Errorf("не удалось удалить службу: %w", err)
	}
	log.Printf("Приёмник: служба удалена")
	return nil
}
