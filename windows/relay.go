//go:build windows

package main

import (
	"net"
	"sync"
	"time"
)

// UDPRelay forwards UDP traffic arriving on the Windows port to the game
// server inside the engine. WSL2 (in its default NAT networking mode) does not
// forward UDP from the Windows network into Linux, so without this relay
// nobody outside the PC could reach the server.
type UDPRelay struct {
	front   *net.UDPConn
	target  *net.UDPAddr
	mu      sync.Mutex
	clients map[string]*relayClient
	closed  chan struct{}
}

type relayClient struct {
	addr     *net.UDPAddr
	back     *net.UDPConn
	lastSeen time.Time
}

const relayIdle = 2 * time.Minute

func startRelay(port int, targetIP string) (*UDPRelay, error) {
	front, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
	if err != nil {
		return nil, err
	}
	front.SetReadBuffer(4 << 20)
	front.SetWriteBuffer(4 << 20)
	r := &UDPRelay{
		front:   front,
		target:  &net.UDPAddr{IP: net.ParseIP(targetIP), Port: port},
		clients: map[string]*relayClient{},
		closed:  make(chan struct{}),
	}
	go r.loop()
	go r.reaper()
	return r, nil
}

func (r *UDPRelay) loop() {
	buf := make([]byte, 65535)
	for {
		n, from, err := r.front.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-r.closed:
				return
			default:
				continue
			}
		}
		c := r.client(from)
		if c == nil {
			continue
		}
		c.back.Write(buf[:n])
	}
}

func (r *UDPRelay) client(from *net.UDPAddr) *relayClient {
	key := from.String()
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.clients[key]; ok {
		c.lastSeen = time.Now()
		return c
	}
	back, err := net.DialUDP("udp4", nil, r.target)
	if err != nil {
		return nil
	}
	back.SetReadBuffer(1 << 20)
	c := &relayClient{addr: from, back: back, lastSeen: time.Now()}
	r.clients[key] = c
	go func() {
		buf := make([]byte, 65535)
		for {
			n, err := back.Read(buf)
			if err != nil {
				return
			}
			r.mu.Lock()
			c.lastSeen = time.Now()
			r.mu.Unlock()
			r.front.WriteToUDP(buf[:n], c.addr)
		}
	}()
	return c
}

func (r *UDPRelay) reaper() {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-r.closed:
			return
		case <-t.C:
		}
		r.mu.Lock()
		for k, c := range r.clients {
			if time.Since(c.lastSeen) > relayIdle {
				c.back.Close()
				delete(r.clients, k)
			}
		}
		r.mu.Unlock()
	}
}

func (r *UDPRelay) Close() {
	close(r.closed)
	r.front.Close()
	r.mu.Lock()
	for k, c := range r.clients {
		c.back.Close()
		delete(r.clients, k)
	}
	r.mu.Unlock()
}
