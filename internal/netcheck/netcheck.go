// Package netcheck tells step by step whether a host answers on a TCP port:
// the name is resolved, then a connection is opened to each address. The
// result says at which step it failed, in words a user can act on.
package netcheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"time"
)

// Steps where a check can stop.
const (
	StepAddress = "address" // the address itself is wrong
	StepDNS     = "dns"     // the name was not found
	StepTCP     = "tcp"     // nothing accepted the connection
	StepOK      = "ok"      // the port answers
)

// Result of a check.
type Result struct {
	Host    string   // as given
	Port    string   //
	IPs     []string // addresses the name resolved to
	IP      string   // the address that answered (or was tried last)
	Step    string
	Refused bool // a host answered but nothing listens on the port
	Timeout bool // no answer at all: packets are dropped on the way
	Err     error
	Elapsed time.Duration
}

// Check resolves host and opens a TCP connection to host:port, trying each
// address for up to perTry.
func Check(ctx context.Context, hostport string, perTry time.Duration) Result {
	start := time.Now()
	host, port, err := net.SplitHostPort(hostport)
	r := Result{Host: host, Port: port}
	if err != nil || host == "" || port == "" {
		r.Step, r.Err = StepAddress, fmt.Errorf("неверный адрес %q", hostport)
		return r
	}
	if ip := net.ParseIP(host); ip != nil {
		r.IPs = []string{ip.String()}
	} else {
		addrs, err := net.DefaultResolver.LookupHost(ctx, host)
		if err != nil || len(addrs) == 0 {
			r.Step, r.Err, r.Elapsed = StepDNS, err, time.Since(start)
			return r
		}
		// IPv4 first: that is what port forwarding on home routers covers
		for _, a := range addrs {
			if !strings.Contains(a, ":") {
				r.IPs = append(r.IPs, a)
			}
		}
		for _, a := range addrs {
			if strings.Contains(a, ":") {
				r.IPs = append(r.IPs, a)
			}
		}
	}
	d := net.Dialer{Timeout: perTry}
	for _, ip := range r.IPs {
		r.IP = ip
		c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, port))
		if err == nil {
			c.Close()
			r.Step, r.Err, r.Refused, r.Timeout = StepOK, nil, false, false
			r.Elapsed = time.Since(start)
			return r
		}
		r.Err = err
		r.Refused = errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(strings.ToLower(err.Error()), "refused")
		var ne net.Error
		r.Timeout = errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) ||
			(errors.As(err, &ne) && ne.Timeout())
		if ctx.Err() != nil {
			break
		}
	}
	r.Step, r.Elapsed = StepTCP, time.Since(start)
	return r
}

// OK reports whether the port answered.
func (r Result) OK() bool { return r.Step == StepOK }

// where names the host with its address, e.g. "home.example.ru (212.1.2.3)".
func (r Result) where() string {
	if r.IP != "" && r.IP != r.Host {
		return r.Host + " (" + r.IP + ")"
	}
	return r.Host
}

// Summary is a short line for the settings form.
func (r Result) Summary() string {
	switch r.Step {
	case StepAddress:
		return r.Err.Error()
	case StepDNS:
		return "имя " + r.Host + " не найдено (DNS)"
	case StepOK:
		return fmt.Sprintf("%s: порт %s отвечает", r.where(), r.Port)
	}
	switch {
	case r.Refused:
		return fmt.Sprintf("%s: порт %s закрыт (подключение отклонено)", r.where(), r.Port)
	case r.Timeout:
		return fmt.Sprintf("%s: порт %s не отвечает (нет ответа за %d с)", r.where(), r.Port, int(r.Elapsed.Round(time.Second)/time.Second))
	}
	return fmt.Sprintf("%s: порт %s недоступен: %v", r.where(), r.Port, r.Err)
}

// Advice explains what to check; role is "receiver" or "asiair".
func (r Result) Advice(role string) string {
	switch r.Step {
	case StepOK:
		return ""
	case StepAddress:
		return "Проверьте, как записан адрес."
	case StepDNS:
		if role == "receiver" {
			return "Проверьте имя; если это имя DDNS, проверьте, что служба DDNS обновляет адрес. Можно указать внешний IP напрямую."
		}
		return "Укажите IP-адрес устройства или нажмите «Найти в сети…»."
	}
	if role == "receiver" {
		switch {
		case r.Refused:
			return "Компьютер или роутер по этому адресу отвечает, но на порту " + r.Port +
				" никто не слушает: приёмник не запущен, в нём другой порт (listen в receiver.yaml) или проброс на роутере ведёт не на тот компьютер или порт."
		case r.Timeout:
			return "Пакеты до порта " + r.Port + " не доходят. Проверьте по порядку: проброс порта " + r.Port +
				" на роутере (на компьютер с приёмником); брандмауэр Windows на компьютере с приёмником (разрешён ли вход на порт " + r.Port +
				"); «белый» ли IP у домашнего интернета; не закрывает ли сеть здесь (в обсерватории) исходящие подключения на порт " + r.Port +
				" — для этого откройте https://" + r.Host + ":" + r.Port + " в браузере на этом компьютере: ответ «unauthorized» значит, что сеть в порядке."
		}
		return "Проверьте подключение к интернету на этом компьютере."
	}
	switch {
	case r.Refused:
		return "Устройство по этому адресу отвечает, но общий доступ к файлам (SMB, порт " + r.Port + ") на нём не работает — возможно, это не ASIAIR/StellaVita. Нажмите «Найти в сети…»."
	case r.Timeout:
		return "Устройство не отвечает: оно выключено, в другой сети или сменило адрес. Нажмите «Найти в сети…»."
	}
	return "Проверьте подключение этого компьютера к сети обсерватории."
}
