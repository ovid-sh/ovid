package main

import "testing"

func TestNamesDir(t *testing.T) {
	const dir = "/tmp/ovid"
	for s, want := range map[string]bool{
		`{"command":"ls /tmp/ovid"}`:               true,
		`{"command":"cat /tmp/ovid/README.md"}`:    true,
		`{"command":"cd /tmp/ovid; ls"}`:           true,
		`/tmp/ovid`:                                true,
		`{"command":"ls /tmp/ovid-out/work"}`:      false,
		`{"command":"ls /tmp/ovid-out /tmp/ovid"}`: true,
		`{"command":"ls /tmp/ovidian /tmp/other"}`: false,
		`{"command":"ovid check"}`:                 false,
	} {
		if got := namesDir(s, dir); got != want {
			t.Errorf("namesDir(%q) = %v, want %v", s, got, want)
		}
	}
}
