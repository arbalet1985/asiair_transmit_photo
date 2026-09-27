package source

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseLink(t *testing.T) {
	cases := []struct {
		in   string
		want Link
	}{
		{"192.168.1.50", Link{Kind: "smb", Host: "192.168.1.50"}},
		{`\\192.168.1.50\EMMC Images`, Link{Kind: "smb", Host: "192.168.1.50", Shares: []string{"EMMC Images"}}},
		{`\\asiair\EMMC Images\Autorun`, Link{Kind: "smb", Host: "asiair", Shares: []string{"EMMC Images"}}},
		{"//10.0.0.1/Udisk", Link{Kind: "smb", Host: "10.0.0.1", Shares: []string{"Udisk"}}},
		{"smb://pi:secret@10.0.0.1:1445/EMMC%20Images", Link{Kind: "smb", Host: "10.0.0.1:1445", Shares: []string{"EMMC Images"}, Username: "pi", Password: "secret"}},
		{`dir:C:\ASIAIR`, Link{Kind: "dir", Dir: `C:\ASIAIR`}},
		{`usb:E:\`, Link{Kind: "usb", Dir: `E:\`}},
		{"USB:", Link{Kind: "usb"}},
	}
	for _, c := range cases {
		got, err := ParseLink(c.in)
		if err != nil {
			t.Errorf("ParseLink(%q): %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(*got, c.want) {
			t.Errorf("ParseLink(%q) = %+v; want %+v", c.in, *got, c.want)
		}
	}
	for _, bad := range []string{"", "http://x/y", `\\`} {
		if _, err := ParseLink(bad); err == nil {
			t.Errorf("ParseLink(%q): expected error", bad)
		}
	}
}

func TestSplitPath(t *testing.T) {
	s, r, err := SplitPath("EMMC Images/Autorun/Light/a.fit")
	if err != nil || s != "EMMC Images" || r != "Autorun/Light/a.fit" {
		t.Fatalf("got %q %q %v", s, r, err)
	}
	for _, bad := range []string{"share", "share/../x", "share/a//b", "/"} {
		if _, _, err := SplitPath(bad); err == nil {
			t.Errorf("SplitPath(%q): expected error", bad)
		}
	}
}

func TestUSB(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "Autorun", "Light"), 0o755)
	os.MkdirAll(filepath.Join(root, "System Volume Information"), 0o755)
	os.WriteFile(filepath.Join(root, "Autorun", "Light", "a.fit"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "System Volume Information", "b.fit"), []byte("y"), 0o644)

	old := RemovableDrives
	defer func() { RemovableDrives = old }()
	RemovableDrives = func() []Drive { return nil }

	src, err := New("usb:", nil, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := src.Walk(context.Background(), nil, func(File) error { return nil }); !errors.Is(err, ErrNoUSB) {
		t.Fatalf("no drive: %v", err)
	}

	RemovableDrives = func() []Drive { return []Drive{{Root: root}} }
	var got []string
	if err := src.Walk(context.Background(), nil, func(f File) error {
		got = append(got, f.Path)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"Udisk/Autorun/Light/a.fit"}) {
		t.Fatalf("walk = %v", got)
	}
	r, err := src.Open("Udisk/Autorun/Light/a.fit")
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	if res, err := src.Probe(context.Background()); err != nil || res.Shares[0].Name != "Udisk" {
		t.Fatalf("probe: %+v %v", res, err)
	}

	fixed, _ := New("usb:"+root, nil, "", "", "")
	RemovableDrives = func() []Drive { return nil }
	if _, err := fixed.Probe(context.Background()); err != nil {
		t.Fatalf("fixed root: %v", err)
	}
}

func TestFolderLink(t *testing.T) {
	for in, want := range map[string]string{
		`E:\`:               `usb:E:\`,
		`e:`:                `usb:E:\`,
		`E:\Autorun`:        `dir:E:\Autorun`,
		`C:\Photos\ASIAIR\`: `dir:C:\Photos\ASIAIR\`,
	} {
		if got := FolderLink(in); got != want {
			t.Errorf("FolderLink(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestReplaceHost(t *testing.T) {
	for in, want := range map[string]string{
		`\\192.168.1.50\EMMC Images`:                  `\\192.168.1.77\EMMC Images`,
		`\\192.168.1.50\EMMC Images\`:                 `\\192.168.1.77\EMMC Images\`,
		"192.168.1.50":                                "192.168.1.77",
		`//asiair.local/Udisk`:                        `//192.168.1.77/Udisk`,
		`\\127.0.0.1:1445\EMMC Images`:                `\\192.168.1.77:1445\EMMC Images`,
		"smb://as:astrostation@10.0.0.5/Astrostation": "smb://as:astrostation@192.168.1.77/Astrostation",
		`usb:E:\`: `usb:E:\`,
	} {
		got, err := ReplaceHost(in, "192.168.1.77")
		if err != nil || got != want {
			t.Errorf("ReplaceHost(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestResolveShares(t *testing.T) {
	cases := []struct {
		link, def []string
		want      []string
		auto      bool
	}{
		{nil, nil, fallbackShares, true},
		{nil, []string{"EMMC Images", "Udisk"}, fallbackShares, true},
		{nil, []string{"*"}, fallbackShares, true},
		{[]string{"EMMC Images"}, nil, fallbackShares, true},
		{[]string{"EMMC Images"}, []string{"EMMC Images"}, []string{"EMMC Images"}, false},
		{[]string{"Udisk"}, nil, []string{"Udisk"}, false},
		{[]string{"Astrostation"}, []string{"*"}, []string{"Astrostation"}, false},
		{nil, []string{"EMMC Images", "TF"}, []string{"EMMC Images", "TF"}, false},
	}
	for _, c := range cases {
		got, auto := resolveShares(c.link, c.def)
		if auto != c.auto || !reflect.DeepEqual(got, c.want) {
			t.Errorf("resolveShares(%q, %q) = %q, %v; want %q, %v", c.link, c.def, got, auto, c.want, c.auto)
		}
	}
}

func TestSortShares(t *testing.T) {
	got := []string{"Udisk", "SD Images", "EMMC Images"}
	sortShares(got)
	if want := []string{"EMMC Images", "SD Images", "Udisk"}; !reflect.DeepEqual(got, want) {
		t.Errorf("sortShares = %q; want %q", got, want)
	}
}

func TestDirSpace(t *testing.T) {
	sp := NewDir(t.TempDir()).Space(context.Background())
	if len(sp) != 1 || sp[0].Total == 0 || sp[0].Free > sp[0].Total {
		t.Fatalf("%+v", sp)
	}
}
