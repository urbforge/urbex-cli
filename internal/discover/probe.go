package discover

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"
	"time"
)

// probePorts are what a host on a home LAN usually answers on: SSH, web,
// DNS, Proxmox, SMB. A port that refuses the connection counts as much
// as one that accepts it: something is at the address.
var probePorts = []string{"22", "80", "443", "53", "8006", "445"}

// Probe reports whether something answers at ip: an ICMP echo, when this
// process may send one (root, as in urbex-op's container), or a TCP
// connection, accepted or refused, on a usual port. A host that answers
// neither looks free: hence the warning about the DHCP range.
func Probe(ctx context.Context, ip string) bool {
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	found := make(chan bool, len(probePorts)+1)
	go func() { found <- ping(ctx, ip) }()
	for _, port := range probePorts {
		go func(port string) {
			conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(ip, port))
			if err == nil {
				conn.Close()
				found <- true
				return
			}
			found <- errors.Is(err, syscall.ECONNREFUSED)
		}(port)
	}
	for i := 0; i < len(probePorts)+1; i++ {
		select {
		case up := <-found:
			if up {
				return true
			}
		case <-ctx.Done():
			return false
		}
	}
	return false
}

// ping sends one ICMP echo and waits for the reply, over a raw socket
// (root or CAP_NET_RAW); false when it can't.
func ping(ctx context.Context, ip string) bool {
	conn, err := net.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return false
	}
	defer conn.Close()
	dst, err := net.ResolveIPAddr("ip4", ip)
	if err != nil {
		return false
	}
	id := os.Getpid() & 0xffff
	seq := int(dst.IP.To4()[3])
	msg := echoRequest(id, seq)
	if _, err := conn.WriteTo(msg, dst); err != nil {
		return false
	}
	deadline, _ := ctx.Deadline()
	conn.SetReadDeadline(deadline)
	buf := make([]byte, 1500)
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			return false
		}
		// An echo reply (type 0) from dst, for our id and sequence; a raw
		// ip4:icmp socket hands over the ICMP message without the IP header.
		if from.String() == dst.String() && n >= 8 && buf[0] == 0 &&
			int(buf[4])<<8|int(buf[5]) == id && int(buf[6])<<8|int(buf[7]) == seq {
			return true
		}
	}
}

// echoRequest is an ICMP echo request (type 8) with its checksum.
func echoRequest(id, seq int) []byte {
	b := []byte{8, 0, 0, 0, byte(id >> 8), byte(id), byte(seq >> 8), byte(seq), 'u', 'r', 'b', 'e', 'x'}
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	cs := ^uint16(sum)
	b[2], b[3] = byte(cs>>8), byte(cs)
	return b
}
