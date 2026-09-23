package source

import (
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
