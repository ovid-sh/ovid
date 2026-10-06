package main

import (
	"reflect"
	"testing"
)

func TestNamesDir(t *testing.T) {
	const dir = "/tmp/ovid"
	for s, want := range map[string]bool{
		`{"command":"ls /tmp/ovid"}`:                    true,
		`{"command":"cat /tmp/ovid/README.md"}`:         true,
		`{"command":"cd /tmp/ovid; ls"}`:                true,
		`{"command":"cd /tmp/ovid&&cat goal.json"}`:     true,
		`{"command":"ls /tmp/ovid|wc -l"}`:              true,
		`{"command":"(cd /tmp/ovid)"}`:                  true,
		`{"file_path":"/tmp/ovid"}`:                     true,
		`/tmp/ovid`:                                     true,
		`{"command":"ls /tmp/ovid-out/work"}`:           false,
		`{"command":"ls /tmp/ovid.bak"}`:                false,
		`{"command":"ls /tmp/ovid+copy /tmp/ovid@old"}`: false,
		`{"command":"ls /tmp/ovid_2"}`:                  false,
		`{"command":"ls /tmp/ovid/"}`:                   true,
		`{"command":"cd '/tmp/ovid'"}`:                  true,
		`{"command":"X=/tmp/ovid ls"}`:                  true,
		`{"command":"ls /tmp/ovid>out"}`:                true,
		`{"command":"ls /tmp/ovid-out /tmp/ovid"}`:      true,
		`{"command":"ls /tmp/ovidian /tmp/other"}`:      false,
		`{"command":"cat /var/tmp/ovid/file"}`:          false,
		`{"command":"cat /var/tmp/ovid /tmp/ovid/x"}`:   true,
		`{"command":"ovid check"}`:                      false,
	} {
		if got := namesDir(s, dir); got != want {
			t.Errorf("namesDir(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestOvidSubs(t *testing.T) {
	for cmd, want := range map[string]map[string]int{
		"ovid check":                                {"check": 1},
		"ovid -C /tmp/mod check":                    {"check": 1},
		`ovid -C "/tmp/my module" check`:            {"check": 1},
		`ovid -C '/tmp/my module' -C x outline`:     {"outline": 1},
		"ovid -C a -C b outline --pkg p":            {"outline": 1},
		"ovid check -C mod && ovid build -C mod":    {"check": 1, "build": 1},
		"cd m; ovid show Sum | head; (ovid refs X)": {"show": 1, "refs": 1},
		"echo void check; myovid check":             {},
	} {
		got := map[string]int{}
		ovidSubs(cmd, got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ovidSubs(%q) = %v, want %v", cmd, got, want)
		}
	}
}
