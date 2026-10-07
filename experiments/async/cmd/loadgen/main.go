// Command loadgen drives one server with C concurrent clients for a fixed
// time and prints one JSON line of results. Each request is its own TCP
// connection: write "GET <path> HTTP/1.1" with Connection: close, read to
// EOF, and count it ok when the response starts "HTTP/1.1 200".
//
//	loadgen -addr 127.0.0.1:8080 -path /w1 -c 100 -d 10s -label B-native
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type result struct {
	Label    string  `json:"label"`
	Path     string  `json:"path"`
	C        int     `json:"c"`
	Seconds  float64 `json:"seconds"`
	OK       int     `json:"ok"`
	Errors   int     `json:"errors"`
	RPS      float64 `json:"rps"`
	P50ms    float64 `json:"p50_ms"`
	P90ms    float64 `json:"p90_ms"`
	P99ms    float64 `json:"p99_ms"`
	MaxMs    float64 `json:"max_ms"`
	FirstErr string  `json:"first_error,omitempty"`
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "server host:port (raw TCP), or an http(s):// URL base")
	path := flag.String("path", "/w1", "request path")
	c := flag.Int("c", 10, "concurrent clients")
	d := flag.Duration("d", 10*time.Second, "how long to run")
	warm := flag.Duration("warmup", time.Second, "run this long first, uncounted")
	label := flag.String("label", "", "label for the result line")
	timeout := flag.Duration("timeout", 10*time.Second, "per-request timeout")
	flag.Parse()

	do := rawGet(*addr, *path, *timeout)
	if strings.HasPrefix(*addr, "http://") || strings.HasPrefix(*addr, "https://") {
		do = httpGet(*addr+*path, *timeout, *c)
	}
	if *warm > 0 {
		run(do, *c, *warm)
	}
	lat, errs, first, el := run(do, *c, *d)
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	r := result{Label: *label, Path: *path, C: *c, Seconds: el.Seconds(), OK: len(lat), Errors: errs, FirstErr: first}
	if len(lat) > 0 {
		r.RPS = float64(len(lat)) / el.Seconds()
		r.P50ms = ms(lat[len(lat)*50/100])
		r.P90ms = ms(lat[len(lat)*90/100])
		r.P99ms = ms(lat[len(lat)*99/100])
		r.MaxMs = ms(lat[len(lat)-1])
	}
	json.NewEncoder(os.Stdout).Encode(r)
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// run calls do from c goroutines until d has passed: the latencies of the
// requests that succeeded, the count that failed, and the first failure.
func run(do func() error, c int, d time.Duration) ([]time.Duration, int, string, time.Duration) {
	var mu sync.Mutex
	var lat []time.Duration
	errs := 0
	first := ""
	start := time.Now()
	end := start.Add(d)
	var wg sync.WaitGroup
	for i := 0; i < c; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var mine []time.Duration
			bad := 0
			var firstErr error
			for time.Now().Before(end) {
				t := time.Now()
				if err := do(); err != nil {
					bad++
					if firstErr == nil {
						firstErr = err
					}
					continue
				}
				mine = append(mine, time.Since(t))
			}
			mu.Lock()
			lat = append(lat, mine...)
			errs += bad
			if first == "" && firstErr != nil {
				first = firstErr.Error()
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	return lat, errs, first, time.Since(start)
}

func rawGet(addr, path string, timeout time.Duration) func() error {
	req := []byte("GET " + path + " HTTP/1.1\r\nHost: bench\r\nConnection: close\r\n\r\n")
	return func() error {
		conn, err := net.DialTimeout("tcp", addr, timeout)
		if err != nil {
			return err
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(timeout))
		if _, err := conn.Write(req); err != nil {
			return err
		}
		body, err := io.ReadAll(conn)
		if err != nil {
			return err
		}
		if !bytes.HasPrefix(body, []byte("HTTP/1.1 200")) {
			return fmt.Errorf("response %q", truncate(body))
		}
		return nil
	}
}

// httpGet is for a server behind a real HTTP stack (wrangler dev): keep-
// alive connections, one per client.
func httpGet(u string, timeout time.Duration, c int) func() error {
	if _, err := url.Parse(u); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(64)
	}
	client := &http.Client{Timeout: timeout, Transport: &http.Transport{MaxIdleConnsPerHost: c, MaxConnsPerHost: c}}
	return func() error {
		resp, err := client.Get(u)
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("status %d: %q", resp.StatusCode, truncate(body))
		}
		return nil
	}
}

func truncate(b []byte) string {
	if len(b) > 80 {
		b = b[:80]
	}
	return string(b)
}
