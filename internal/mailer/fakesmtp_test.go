package mailer

import (
	"bufio"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
)

// startFakeSMTP runs a minimal in-process SMTP server speaking just enough of
// the protocol for one DATA transaction; received raw messages go to the
// returned channel. No network beyond loopback.
func startFakeSMTP(t *testing.T) (addr string, received <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	ch := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		r := bufio.NewReader(conn)
		write := func(s string) { _, _ = fmt.Fprint(conn, s+"\r\n") }
		write("220 fake ESMTP")
		var data strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if inData {
				if line == "." {
					inData = false
					write("250 OK queued")
					ch <- data.String()
					continue
				}
				data.WriteString(line + "\n")
				continue
			}
			cmd := strings.ToUpper(line)
			switch {
			case strings.HasPrefix(cmd, "EHLO") || strings.HasPrefix(cmd, "HELO"):
				write("250-fake\r\n250 8BITMIME")
			case strings.HasPrefix(cmd, "MAIL FROM:"), strings.HasPrefix(cmd, "RCPT TO:"):
				write("250 OK")
			case cmd == "DATA":
				write("354 End data with <CR><LF>.<CR><LF>")
				inData = true
			case cmd == "QUIT":
				write("221 Bye")
				return
			case cmd == "RSET":
				write("250 OK")
			default:
				write("502 not implemented")
			}
		}
	}()
	return ln.Addr().String(), ch
}

func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("Atoi: %v", err)
	}
	return host, port
}
