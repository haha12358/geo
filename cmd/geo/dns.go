package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// fakeIPHint is printed when the system resolver returned a fake-ip answer.
const fakeIPHint = " (fake-ip range, re-querying through DNS)"

// fallbackDNSServers is used when --dns is not given and a fake-ip answer is
// detected: public resolvers queried from a physical interface.
var fallbackDNSServers = []string{
	"223.5.5.5",
	"119.29.29.29",
}

// fakeIPNets covers the default fake-ip ranges of common proxy cores:
// mihomo/Clash 198.18.0.0/15, sing-box 198.18.0.0/15 + fc00::/18
// (sing-box 1.12+), and old FakeIP default 28.0.0.0/8.
var fakeIPNets = func() []*net.IPNet {
	nets := make([]*net.IPNet, 0, 3)
	for _, cidr := range []string{"198.18.0.0/15", "28.0.0.0/8", "fc00::/18"} {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err == nil {
			nets = append(nets, ipNet)
		}
	}
	return nets
}()

func isFakeIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, ipNet := range fakeIPNets {
		if ipNet.Contains(ip) {
			return true
		}
	}
	return false
}

// lookupIPWithNSLookup resolves domain through the system nslookup command.
func lookupIPWithNSLookup(domain, server string) ([]net.IP, error) {
	var args []string
	if runtime.GOOS == "windows" {
		args = append(args, "-type=A")
	}
	// nslookup syntax: nslookup [type] domain [server] — the domain goes
	// before the optional server, otherwise the server is treated as the
	// lookup target.
	args = append(args, domain)
	if server != "" {
		args = append(args, server)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "nslookup", args...).Output()
	if err != nil {
		// nslookup exits non-zero on some failures even when it printed a
		// usable answer; keep whatever output exists.
		if len(output) == 0 {
			return nil, err
		}
	}
	return parseNSLookupOutput(string(output)), nil
}

// parseNSLookupOutput extracts answer addresses from nslookup output. The
// output starts with a server block ("服务器/Server:" + its "Address:"), so
// parsing skips the first address line and takes the rest. Matching is
// locale-independent: the labels are garbled under non-UTF-8 codepages, but
// "Address"/"Addresses" keywords and the line structure stay ASCII.
func parseNSLookupOutput(output string) []net.IP {
	lines := strings.Split(output, "\n")
	var ips []net.IP
	seen := make(map[string]struct{})
	// find the end of the server block: the first "Address:" line
	serverBlockEnd := -1
	for i, line := range lines {
		if strings.Contains(strings.ToLower(line), "address") {
			serverBlockEnd = i
			break
		}
	}
	if serverBlockEnd < 0 {
		return nil
	}
	for _, line := range lines[serverBlockEnd+1:] {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		idx := strings.Index(lower, "address")
		if idx < 0 {
			continue
		}
		rest := strings.TrimLeft(line[idx+len("address"):], "es")
		rest = strings.TrimLeft(rest, ":： \t")
		ip := net.ParseIP(rest)
		if ip == nil {
			continue
		}
		key := ip.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		ips = append(ips, ip)
	}
	return ips
}

// physicalBindAddrs returns source addresses of interfaces that are not in a
// physicalBindAddrs returns IPv4 source addresses of interfaces that are not
// in a fake-ip range. A TUN adapter in fake-ip mode owns an address like
// 198.18.0.1; binding a physical address makes DNS packets leave through the
// default gateway instead of the tunnel, dodging its DNS hijack.
func physicalBindAddrs() ([]net.IP, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []net.IP
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP
		// fallback servers are IPv4 literals, so only IPv4 binds make sense
		if ip.To4() == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || isFakeIP(ip) {
			continue
		}
		out = append(out, ip)
	}
	return out, nil
}

// newQuery builds a single-question A or AAAA query. One question per message
// keeps compatibility with public DNS servers that answer only the first
// question of a multi-question message.
func newQuery(domain string, qtype dnsmessage.Type) *dnsmessage.Message {
	return &dnsmessage.Message{
		Header: dnsmessage.Header{
			ID:               queryID(),
			RecursionDesired: true,
		},
		Questions: []dnsmessage.Question{
			{
				Name:  dnsmessage.MustNewName(domain + "."),
				Type:  qtype,
				Class: dnsmessage.ClassINET,
			},
		},
	}
}

func queryID() uint16 {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return binary.BigEndian.Uint16(b[:])
}

// exchangeQueries sends an A and then an AAAA query through exchangeOne and
// collects the addresses from both.
func exchangeQueries(ctx context.Context, domain string, exchangeOne func(query *dnsmessage.Message, wire []byte) ([]byte, error)) ([]net.IP, error) {
	var ips []net.IP
	for _, qtype := range []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA} {
		query := newQuery(domain, qtype)
		wire, err := query.Pack()
		if err != nil {
			return nil, err
		}
		respBytes, err := exchangeOne(query, wire)
		if err != nil {
			return nil, err
		}
		var msg dnsmessage.Message
		if err := msg.Unpack(respBytes); err != nil {
			return nil, err
		}
		if err := checkResponse(&msg, query); err != nil {
			return nil, err
		}
		ips = append(ips, extractIPs(msg)...)
	}
	return ips, nil
}

func extractIPs(msg dnsmessage.Message) []net.IP {
	var ips []net.IP
	for _, answer := range msg.Answers {
		switch body := answer.Body.(type) {
		case *dnsmessage.AResource:
			ips = append(ips, net.IP(body.A[:]).To4())
		case *dnsmessage.AAAAResource:
			ips = append(ips, net.IP(body.AAAA[:]))
		}
	}
	return ips
}

func checkResponse(msg *dnsmessage.Message, query *dnsmessage.Message) error {
	if !msg.Header.Response {
		return errors.New("not a DNS response")
	}
	if msg.Header.ID != query.Header.ID {
		return errors.New("DNS response ID mismatch")
	}
	if msg.Header.RCode != dnsmessage.RCodeSuccess {
		return fmt.Errorf("DNS rcode %d", msg.Header.RCode)
	}
	return nil
}

// lookupIPBound sends UDP DNS queries to server from a physical interface
// source address, bypassing the TUN default route.
func lookupIPBound(ctx context.Context, domain, server string) ([]net.IP, error) {
	addrs, err := physicalBindAddrs()
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, errors.New("no physical interface to bind")
	}
	var d net.Dialer
	d.LocalAddr = &net.UDPAddr{IP: addrs[0]}
	d.Timeout = 3 * time.Second
	conn, err := d.DialContext(ctx, "udp", net.JoinHostPort(server, "53"))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	} else {
		conn.SetDeadline(time.Now().Add(5 * time.Second))
	}
	return exchangeQueries(ctx, domain, func(query *dnsmessage.Message, wire []byte) ([]byte, error) {
		if _, err := conn.Write(wire); err != nil {
			return nil, err
		}
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return nil, err
			}
			if n >= 12 && binary.BigEndian.Uint16(buf[:2]) == query.ID {
				return buf[:n], nil
			}
			// stale or mismatched datagram, keep waiting until deadline
		}
	})
}

// resolveDomain resolves a domain with the system resolver first (or through
// nslookup with the --dns server when given). When the system answer falls
// into a fake-ip range (TUN mode), the domain is re-queried through public
// DNS from a physical interface to get the real address.
func resolveDomain(domainName string) (net.IP, error) {
	if dnsServer != "" {
		ips, err := lookupIPWithNSLookup(domainName, dnsServer)
		if err != nil {
			return nil, fmt.Errorf("nslookup with %s: %w", dnsServer, err)
		}
		if len(ips) == 0 {
			return nil, errors.New("no address found")
		}
		return ips[0], nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	timeoutPrinted := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			fmt.Print("\n\n🌎Resolved ", domainName, " timeout")
		case <-timeoutPrinted:
		}
	}()

	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", domainName)
	close(timeoutPrinted)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("no address found")
	}
	ip := ips[0]
	if !isFakeIP(ip) {
		return ip, nil
	}

	fmt.Println("\n\n⚠Detected", ip, fakeIPHint)
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, server := range fallbackDNSServers {
		ips, err := lookupIPBound(ctx, domainName, server)
		if err != nil || len(ips) == 0 {
			continue
		}
		return ips[0], nil
	}
	return nil, errors.New("no address found")
}
