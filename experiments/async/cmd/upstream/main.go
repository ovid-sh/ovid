// Command upstream is the simulated backend the async POCs call: a TCP
// server on 127.0.0.1 that reads lines "<ms>\n", waits that long, and
// answers "ok\n", any number of times per connection.
//
//	upstream -port 9100
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"
)

func main() {
	port := flag.Int("port", 9100, "port on 127.0.0.1")
	flag.Parse()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		log.Fatal(err)
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Print(err)
			continue
		}
		go serve(c)
	}
}

func serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		ms, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || ms < 0 {
			fmt.Fprintf(c, "bad\n")
			return
		}
		time.Sleep(time.Duration(ms) * time.Millisecond)
		if _, err := c.Write([]byte("ok\n")); err != nil {
			return
		}
	}
}
