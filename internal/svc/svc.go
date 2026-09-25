// Package svc runs a program either in the console or as a Windows service
// (also systemd/launchd on other systems) and handles install/uninstall.
package svc

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/kardianos/service"

	"github.com/arbalet1985/asiair_transmit_photo/internal/firewall"
	"github.com/arbalet1985/asiair_transmit_photo/internal/update"
)

// App describes a program.
type App struct {
	Name        string // service name, e.g. "asiair-agent"
	DisplayName string
	Description string
	// ConfigName is the default config file name next to the executable.
	ConfigName string
	// Start loads the configuration and returns the function to run and the
	// log file from the configuration (may be empty).
	Start func(configPath string) (run func(ctx context.Context) error, logFile string, err error)
	// Extra commands, e.g. "check".
	Commands map[string]Command
	// GUI, when set, runs the windowed (tray) mode. It is used for the
	// "gui" command and when the program is started without a command
	// outside of a service (e.g. by double-click).
	GUI func(configPath string) error
}

type Command struct {
	Help string
	Run  func(configPath string, args []string) error
}

type program struct {
	run    func(ctx context.Context) error
	cancel context.CancelFunc
	done   chan struct{}
}

func (p *program) Start(s service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})
	go func() {
		defer close(p.done)
		if err := p.run(ctx); err != nil {
			log.Printf("Ошибка: %v", err)
			os.Exit(1) // a service manager restarts us
		}
		if service.Interactive() {
			os.Exit(0)
		}
	}()
	return nil
}

func (p *program) Stop(s service.Service) error {
	log.Printf("Остановка...")
	p.cancel()
	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
	}
	return nil
}

func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return filepath.Dir(exe)
}

// DefaultLogFile is the log next to the executable.
func DefaultLogFile(name string) string {
	return filepath.Join(exeDir(), name+".log")
}

// setupLog writes the log to the console (when interactive) and to a file.
func setupLog(file string) {
	log.SetFlags(log.Ldate | log.Ltime)
	if file == "" {
		return
	}
	f, err := OpenLog(file)
	if err != nil {
		log.Printf("Не удалось открыть журнал %s: %v", file, err)
		return
	}
	if IsService() {
		// a service has no console: send crash reports to the log too
		crash := func(lf *os.File) { debug.SetCrashOutput(lf, debug.CrashOptions{}) }
		crash(f.File())
		f.OnRotate = crash
		log.SetOutput(f)
	} else {
		log.SetOutput(teeWriter{f, os.Stderr})
	}
}

func newService(app App, cfgPath string, prg service.Interface) (service.Service, error) {
	if prg == nil {
		prg = &program{}
	}
	return service.New(prg, &service.Config{
		Name:        app.Name,
		DisplayName: app.DisplayName,
		Description: app.Description,
		// "service" tells the program it runs as a service; detecting it
		// automatically is unreliable on some systems
		Arguments: []string{"-config", cfgPath, "service"},
		Option: service.KeyValue{
			"StartType":              "automatic",
			"OnFailure":              "restart",
			"OnFailureDelayDuration": "30s",
			"OnFailureResetPeriod":   600,
		},
	})
}

// Service states reported by ServiceState.
const (
	ServiceNotInstalled = "not-installed"
	ServiceRunning      = "running"
	ServiceStopped      = "stopped"
)

// ServiceState reports whether the app is installed as a service and running.
func ServiceState(app App, cfgPath string) string {
	s, err := newService(app, cfgPath, nil)
	if err != nil {
		return ServiceNotInstalled
	}
	st, err := s.Status()
	switch {
	case err != nil:
		return ServiceNotInstalled
	case st == service.StatusRunning:
		return ServiceRunning
	}
	return ServiceStopped
}

// ServiceControl performs "install", "uninstall", "start", "stop" or
// "restart" on the app's service (administrator rights required).
func ServiceControl(app App, cfgPath, action string) error {
	s, err := newService(app, cfgPath, nil)
	if err != nil {
		return err
	}
	if err := service.Control(s, action); err != nil {
		return err
	}
	if action == "install" {
		AllowFirewall(app)
	}
	return nil
}

// AllowFirewall adds the inbound rule for the program (administrator
// rights). A service cannot be asked by Windows and would be blocked
// silently without it; the tray program is not asked again either.
func AllowFirewall(app App) error {
	if !firewall.Supported() {
		return firewall.ErrUnsupported
	}
	exe, err := update.Executable()
	if err != nil {
		return err
	}
	if err := firewall.Allow(app.DisplayName, exe); err != nil {
		log.Printf("Брандмауэр: %v", err)
		return err
	}
	log.Printf("Брандмауэр Windows: добавлено разрешение для %s", exe)
	return nil
}

// FirewallAllowed reports whether the program is allowed in the firewall.
func FirewallAllowed(app App) bool {
	exe, err := update.Executable()
	return err == nil && firewall.Allowed(app.DisplayName, exe)
}

// Main parses the command line and runs the app.
func Main(app App) {
	fs := flag.NewFlagSet(app.Name, flag.ExitOnError)
	cfgPath := fs.String("config", filepath.Join(exeDir(), app.ConfigName), "путь к файлу настроек")
	fs.Usage = func() {
		out := fs.Output()
		fmt.Fprintf(out, "%s — %s\n\nИспользование:\n  %s [-config файл.yaml] [команда]\n\nКоманды:\n", app.Name, app.Description, app.Name)
		if app.GUI != nil {
			fmt.Fprintln(out, "  gui        окно со значком в трее (по умолчанию в Windows)")
			fmt.Fprintln(out, "  run        запустить в консоли")
		} else {
			fmt.Fprintln(out, "  run        запустить в консоли (по умолчанию)")
		}
		fmt.Fprintln(out, "  install    установить как службу Windows (автозапуск)")
		fmt.Fprintln(out, "  uninstall  удалить службу")
		fmt.Fprintln(out, "  start      запустить службу")
		fmt.Fprintln(out, "  stop       остановить службу")
		fmt.Fprintln(out, "  restart    перезапустить службу")
		fmt.Fprintln(out, "  status     состояние службы")
		fmt.Fprintln(out, "  firewall-allow  разрешить программу в брандмауэре Windows (от имени администратора)")
		for name, c := range app.Commands {
			fmt.Fprintf(out, "  %-10s %s\n", name, c.Help)
		}
		fmt.Fprintln(out, "\nПараметры:")
		fs.PrintDefaults()
	}
	fs.Parse(os.Args[1:])
	cmd := "run"
	if app.GUI != nil && service.Interactive() && runtime.GOOS == "windows" {
		cmd = "gui"
	}
	if fs.NArg() > 0 {
		cmd = strings.ToLower(fs.Arg(0))
	}
	abs, err := filepath.Abs(*cfgPath)
	if err == nil {
		*cfgPath = abs
	}

	if cmd == "gui" && app.GUI != nil {
		if err := app.GUI(*cfgPath); err != nil {
			log.Printf("Ошибка: %v", err)
			os.Exit(1)
		}
		return
	}

	if cmd == "firewall-allow" { // run elevated by the program itself
		if err := AllowFirewall(app); err != nil {
			fmt.Fprintln(os.Stderr, "Ошибка:", err)
			os.Exit(1)
		}
		return
	}

	if c, ok := app.Commands[cmd]; ok {
		log.SetFlags(0)
		if err := c.Run(*cfgPath, fs.Args()[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "Ошибка:", err)
			os.Exit(1)
		}
		return
	}

	prg := &program{}
	s, err := newService(app, *cfgPath, prg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}

	switch cmd {
	case "run", "service":
		if cmd == "service" {
			asService = true
		}
		run, logFile, err := app.Start(*cfgPath)
		if logFile == "" && IsService() {
			logFile = filepath.Join(exeDir(), app.Name+".log")
		}
		setupLog(logFile)
		if err != nil {
			log.Printf("Ошибка запуска: %v", err)
			os.Exit(1)
		}
		release, ok := SingleInstance(app.Name)
		if !ok {
			log.Printf("%s уже запущен (служба или окно в трее)", app.Name)
			os.Exit(1)
		}
		defer release()
		prg.run = run
		if cmd == "service" {
			if err := runService(app.Name, run); err != nil {
				log.Printf("Ошибка службы: %v", err)
				os.Exit(1)
			}
			return
		}
		if err := s.Run(); err != nil {
			log.Printf("Ошибка: %v", err)
			os.Exit(1)
		}
	case "install":
		if _, err := os.Stat(*cfgPath); err != nil {
			fmt.Fprintf(os.Stderr, "Файл настроек %s не найден: создайте его до установки службы\n", *cfgPath)
			os.Exit(1)
		}
		if _, _, err := app.Start(*cfgPath); err != nil {
			fmt.Fprintln(os.Stderr, "Ошибка в настройках:", err)
			os.Exit(1)
		}
		control(s, cmd)
		AllowFirewall(app)
		fmt.Printf("Служба %s установлена (настройки: %s). Запуск: %s start\n", app.Name, *cfgPath, app.Name)
	case "uninstall", "start", "stop", "restart":
		control(s, cmd)
		fmt.Println("Готово:", cmd)
	case "status":
		st, err := s.Status()
		if err != nil {
			fmt.Println("Служба не установлена или недоступна:", err)
			os.Exit(1)
		}
		fmt.Println(map[service.Status]string{
			service.StatusRunning: "работает", service.StatusStopped: "остановлена",
		}[st])
	default:
		fs.Usage()
		os.Exit(2)
	}
}

func control(s service.Service, cmd string) {
	if err := service.Control(s, cmd); err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка (%s): %v\nЗапустите командную строку от имени администратора.\n", cmd, err)
		os.Exit(1)
	}
}

// asService is set when the program was started with the "service" command.
var asService bool

// Interactive reports whether the program runs in a console or window
// rather than as a service.
func Interactive() bool { return !IsService() }

// IsService reports whether the program runs as a service.
func IsService() bool { return asService || !service.Interactive() }

// teeWriter writes to the log file and copies to the console; a missing
// console (write error) never loses the file copy.
type teeWriter struct{ file, console io.Writer }

func (t teeWriter) Write(p []byte) (int, error) {
	n, err := t.file.Write(p)
	t.console.Write(p)
	return n, err
}

// OpenBrowser opens url in the default browser (best effort).
func OpenBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start()
}
