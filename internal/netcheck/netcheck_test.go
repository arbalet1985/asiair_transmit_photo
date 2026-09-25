package netcheck

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestCheck(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	open := l.Addr().String()
	l2, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := l2.Addr().String()
	l2.Close()
	defer l.Close()
	ctx := context.Background()

	if r := Check(ctx, open, time.Second); !r.OK() || !strings.Contains(r.Summary(), "отвечает") {
		t.Fatalf("open: %+v %s", r, r.Summary())
	}
	r := Check(ctx, closed, time.Second)
	if r.Step != StepTCP || !r.Refused || !strings.Contains(r.Summary(), "закрыт") || !strings.Contains(r.Advice("receiver"), "никто не слушает") {
		t.Fatalf("closed: %+v %s", r, r.Summary())
	}
	if r := Check(ctx, "no-such-host.invalid:8443", time.Second); r.Step != StepDNS || !strings.Contains(r.Summary(), "не найдено") {
		t.Fatalf("dns: %+v", r)
	}
	if r := Check(ctx, "8443", time.Second); r.Step != StepAddress {
		t.Fatalf("address: %+v", r)
	}
	// an address that swallows packets: a timeout (or an unreachable network in a sandbox)
	r = Check(ctx, "192.0.2.1:8443", 300*time.Millisecond)
	if r.OK() || r.Step != StepTCP || r.Advice("receiver") == "" {
		t.Fatalf("blackhole: %+v", r)
	}
	if r.Timeout && !strings.Contains(r.Advice("receiver"), "проброс порта") {
		t.Fatalf("timeout advice: %s", r.Advice("receiver"))
	}
}
