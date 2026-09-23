package source

import (
	"net"
	"testing"
)

func TestNarrowAndHosts(t *testing.T) {
	n := narrow(net.ParseIP("10.1.2.3").To4(), net.CIDRMask(8, 32))
	if n.String() != "10.1.2.0/24" {
		t.Fatalf("narrow = %s", n)
	}
	h := hosts(n)
	if len(h) != 254 || h[0].String() != "10.1.2.1" || h[253].String() != "10.1.2.254" {
		t.Fatalf("hosts: %d %v %v", len(h), h[0], h[len(h)-1])
	}
	n = narrow(net.ParseIP("192.168.5.77").To4(), net.CIDRMask(28, 32))
	if n.String() != "192.168.5.64/28" || len(hosts(n)) != 14 {
		t.Fatalf("narrow /28 = %s, %d hosts", n, len(hosts(n)))
	}
}

func TestDeviceLink(t *testing.T) {
	a := Device{Kind: KindASIAIR, IP: "192.168.1.5"}
	if a.Link() != `\\192.168.1.5\EMMC Images` {
		t.Fatal(a.Link())
	}
	s := Device{Kind: KindStellaVita, IP: "192.168.1.56", Shares: []string{"share", "Astrostation"}}
	if s.Link() != `\\192.168.1.56\Astrostation` {
		t.Fatal(s.Link())
	}
	s.Shares = nil
	if s.Link() != `\\192.168.1.56\Astrostation` {
		t.Fatal(s.Link())
	}
	s.Shares = []string{"share", "Images"}
	if s.Link() != `\\192.168.1.56\Images` {
		t.Fatal(s.Link())
	}
}
