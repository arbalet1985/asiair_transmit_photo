package source

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hirochachacha/go-smb2"
)

// StellaVita default SMB login.
const (
	StellaVitaUser     = "as"
	StellaVitaPassword = "astrostation"
)

// Device kinds found by Discover.
const (
	KindASIAIR     = "ASIAIR"
	KindStellaVita = "StellaVita"
)

// Device is an astro computer found in the local network.
type Device struct {
	Kind     string // KindASIAIR or KindStellaVita
	IP       string
	Name     string   // host name from reverse DNS, may be empty
	Shares   []string // disk shares visible after login
	Username string   // login to put into the settings ("" = guest)
	Password string
}

// Link returns the network path to put into the "asiair" setting.
func (d *Device) Link() string {
	if d.Kind == KindASIAIR {
		return `\\` + d.IP + `\` + DefaultShare
	}
	if sh := d.photoShare(); sh != "" {
		return `\\` + d.IP + `\` + sh
	}
	return d.IP
}

// photoShare picks the share that most likely holds the images.
func (d *Device) photoShare() string {
	for _, want := range []string{"image", "photo", "astro", "capture", "data"} {
		for _, sh := range d.Shares {
			if strings.Contains(strings.ToLower(sh), want) {
				return sh
			}
		}
	}
	if len(d.Shares) > 0 {
		return d.Shares[0]
	}
	return ""
}

// LocalSubnets returns the IPv4 networks of the active non-loopback
// interfaces, each narrowed to at most a /24 around the own address.
func LocalSubnets() []*net.IPNet {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var nets []*net.IPNet
	seen := map[string]bool{}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipn.IP.To4()
			if ip == nil || ip.IsLinkLocalUnicast() {
				continue
			}
			n := narrow(ip, ipn.Mask)
			if !seen[n.String()] {
				seen[n.String()] = true
				nets = append(nets, n)
			}
		}
	}
	return nets
}

// narrow returns the network of ip with the given mask, but never wider than /24.
func narrow(ip net.IP, mask net.IPMask) *net.IPNet {
	if ones, bits := mask.Size(); bits != 32 || ones < 24 {
		mask = net.CIDRMask(24, 32)
	}
	return &net.IPNet{IP: ip.Mask(mask), Mask: mask}
}

// hosts lists the usable host addresses of n (without network and broadcast).
func hosts(n *net.IPNet) []net.IP {
	ones, _ := n.Mask.Size()
	size := 1 << (32 - ones)
	base := n.IP.To4()
	var out []net.IP
	for i := 1; i < size-1; i++ {
		v := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
		v += uint32(i)
		out = append(out, net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)))
	}
	return out
}

// Discover scans the local networks for hosts with SMB (port 445) and
// recognises the ASIAIR (guest share "EMMC Images") and the StellaVita
// (login as/astrostation). progress, when not nil, receives short status texts.
func Discover(ctx context.Context, progress func(string)) ([]Device, error) {
	nets := LocalSubnets()
	if len(nets) == 0 {
		return nil, errors.New("не найдено ни одного сетевого подключения")
	}
	var all []net.IP
	var names []string
	for _, n := range nets {
		all = append(all, hosts(n)...)
		names = append(names, n.String())
	}
	if progress != nil {
		progress("Поиск в сетях " + strings.Join(names, ", ") + "…")
	}

	// 1. Which hosts have SMB open.
	var (
		mu   sync.Mutex
		open []string
		wg   sync.WaitGroup
		sem  = make(chan struct{}, 128)
	)
	for _, ip := range all {
		wg.Add(1)
		sem <- struct{}{}
		go func(ip string) {
			defer func() { <-sem; wg.Done() }()
			d := net.Dialer{Timeout: 1500 * time.Millisecond}
			c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, "445"))
			if err != nil {
				return
			}
			c.Close()
			mu.Lock()
			open = append(open, ip)
			mu.Unlock()
		}(ip.String())
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if progress != nil {
		progress(fmt.Sprintf("Найдено компьютеров с общими папками: %d, проверка…", len(open)))
	}

	// 2. Identify them.
	var found []Device
	for _, ip := range open {
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			if d := identify(ctx, ip); d != nil {
				mu.Lock()
				found = append(found, *d)
				mu.Unlock()
			}
		}(ip)
	}
	wg.Wait()
	sort.Slice(found, func(i, j int) bool {
		a, b := net.ParseIP(found[i].IP).To4(), net.ParseIP(found[j].IP).To4()
		return string(a) < string(b)
	})
	return found, ctx.Err()
}

// identify checks one SMB host.
func identify(ctx context.Context, ip string) *Device {
	name := ""
	if n, err := net.DefaultResolver.LookupAddr(ctx, ip); err == nil && len(n) > 0 {
		name = strings.TrimSuffix(n[0], ".")
	}
	lname := strings.ToLower(name)

	guestShares, guestErr := listShares(ctx, ip, guestAttempts[0][0], guestAttempts[0][1])
	if guestErr != nil {
		// some ASIAIR firmware refuses a plain guest login
		if sh, err := listShares(ctx, ip, guestAttempts[1][0], guestAttempts[1][1]); err == nil && hasShare(sh, DefaultShare) {
			guestShares, guestErr = sh, nil
		}
	}
	if guestErr == nil && (hasShare(guestShares, DefaultShare) || strings.Contains(lname, "asiair")) {
		return &Device{Kind: KindASIAIR, IP: ip, Name: name, Shares: guestShares}
	}
	// A host that lets any guest in would also "accept" as/astrostation,
	// so the StellaVita login alone counts only when the guest was refused.
	svShares, svErr := listShares(ctx, ip, StellaVitaUser, StellaVitaPassword)
	if svErr == nil && (guestErr != nil || strings.Contains(lname, "stella")) {
		return &Device{Kind: KindStellaVita, IP: ip, Name: name, Shares: svShares,
			Username: StellaVitaUser, Password: StellaVitaPassword}
	}
	if strings.Contains(lname, "stella") {
		return &Device{Kind: KindStellaVita, IP: ip, Name: name, Shares: guestShares,
			Username: StellaVitaUser, Password: StellaVitaPassword}
	}
	return nil
}

func hasShare(shares []string, want string) bool {
	for _, s := range shares {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}

// listShares logs in and returns the disk shares (without IPC$ and print$).
func listShares(ctx context.Context, ip, user, pass string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, "445"))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(8 * time.Second))
	sd := &smb2.Dialer{Initiator: &smb2.NTLMInitiator{User: user, Password: pass}}
	sess, err := sd.DialContext(ctx, conn)
	if err != nil {
		return nil, err
	}
	defer sess.Logoff()
	names, err := sess.ListSharenames()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		if strings.HasSuffix(n, "$") {
			continue
		}
		out = append(out, n)
	}
	return out, nil
}
