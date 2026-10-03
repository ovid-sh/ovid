package main

import "testing"

func TestQueryFlags(t *testing.T) {
	opts, err := queryFlags([]string{"--pkg", "demo", "--full", "--limit", "0", "--offset", "12", "--calls-to", "fn-write"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Pkg != "demo" || !opts.Full || opts.Limit != 0 || opts.Offset != 12 || opts.CallsTo != "fn-write" {
		t.Fatalf("options: %+v", opts)
	}
	opts, err = queryFlags(nil)
	if err != nil || opts.Limit != 50 {
		t.Fatalf("defaults: %+v %v", opts, err)
	}
	for _, args := range [][]string{
		{"--limit"}, {"--limit", "-1"}, {"--offset", "-1"}, {"--limit", "2147483648"}, {"--limit", "1x"}, {"--limit", "+1"}, {"--limit", ""}, {"--wat", "x"}, {"--id", ""}, {"--full", "true"},
	} {
		if _, err := queryFlags(args); err == nil {
			t.Errorf("accepted invalid flags %q", args)
		}
	}
}
