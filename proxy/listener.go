package proxy

import (
	"errors"
	"log"
	"net"
	"time"
)

func acceptLoop(listener net.Listener, cfg Config) {
	var retryDelay time.Duration
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			netErr, temporary := err.(net.Error)
			//lint:ignore SA1019 Match net/http's retry handling for temporary Accept errors.
			if !temporary || !netErr.Temporary() {
				log.Printf("accept failed: %v", err)
				return
			}
			if retryDelay == 0 {
				retryDelay = 5 * time.Millisecond
			} else {
				retryDelay *= 2
				if retryDelay > time.Second {
					retryDelay = time.Second
				}
			}
			log.Printf("accept failed; retrying in %s: %v", retryDelay, err)
			time.Sleep(retryDelay)
			continue
		}
		retryDelay = 0
		go handleSocksConn(conn, cfg)
	}
}
