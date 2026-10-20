//go:build dev

package main

import (
	"flag"
	"io"
	"log"
	"net"
	"time"
)

func main() {
	listen := flag.String("listen", ":5900", "TCP address to listen on")
	mode := flag.String("mode", "echo", "echo | banner | rst | drop")
	delay := flag.Duration("delay", 0, "delay before handling each connection")
	banner := flag.String("banner", "RFB 003.008\n", "RFB banner to send")
	flag.Parse()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("listen %s: %v", *listen, err)
	}
	defer ln.Close()
	log.Printf("synthetic VNC target listening on %s mode=%s", *listen, *mode)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			return
		}
		go handle(conn, *mode, *delay, *banner)
	}
}

func handle(conn net.Conn, mode string, delay time.Duration, banner string) {
	defer conn.Close()

	if delay > 0 {
		time.Sleep(delay)
	}

	switch mode {
	case "rst":
		if tcpConn, ok := conn.(*net.TCPConn); ok {
			_ = tcpConn.SetLinger(0)
		}
		return
	case "drop":
		// Accept but never answer, then close after the harness timeout.
		time.Sleep(30 * time.Second)
		return
	case "banner":
		_, _ = io.WriteString(conn, banner)
		_, _ = io.Copy(io.Discard, conn)
	default: // echo
		_, _ = io.WriteString(conn, banner)
		_, _ = io.Copy(conn, conn)
	}
}
