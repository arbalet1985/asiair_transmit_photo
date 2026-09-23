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
	"strings"
	"time"

	"github.com/kardianos/service"
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

// OpenLog opens (and rotates when large) a log file for appending.
func OpenLog(file string) (io.Writer, error) {
	os.MkdirAll(filepath.Dir(file), 0o755)
	if st, err := os.Stat(file); err == nil && st.Size() > 20<<20 {
		os.Remove(file + ".old")
		os.Rename(file, file+".old")
	}
	return os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
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
	if service.Interactive() {
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	} else {
		log.SetOutput(f)
	}
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

	if c, ok := app.Commands[cmd]; ok {
		log.SetFlags(0)
		if err := c.Run(*cfgPath, fs.Args()[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "Ошибка:", err)
			os.Exit(1)
		}
		return
	}

	prg := &program{}
	s, err := service.New(prg, &service.Config{
		Name:        app.Name,
		DisplayName: app.DisplayName,
		Description: app.Description,
		Arguments:   []string{"-config", *cfgPath, "run"},
		Option: service.KeyValue{
			"StartType":              "automatic",
			"DelayedAutoStart":       true,
			"OnFailure":              "restart",
			"OnFailureDelayDuration": "30s",
			"OnFailureResetPeriod":   600,
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}

	switch cmd {
	case "run":
		run, logFile, err := app.Start(*cfgPath)
		if logFile == "" && !service.Interactive() {
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

// Interactive reports whether the program runs in a console or window
// rather than as a service.
func Interactive() bool { return service.Interactive() }

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
