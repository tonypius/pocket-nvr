// Package discover finds cameras on the local network: ONVIF WS-Discovery
// multicast probe plus a TCP sweep for RTSP (554) across the /24 the
// appliance sits on. Powers the UI's "Scan network" (add-camera flow).
package discover

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"sync"
	"time"
)

type Result struct {
	IP    string `json:"ip"`
	RTSP  bool   `json:"rtsp"`           // TCP 554 answered
	ONVIF string `json:"onvif"`          // XAddrs from WS-Discovery ("" if none)
	Name  string `json:"name,omitempty"` // device name/scope hint
}

// Subnet24 returns the /24 CIDR of the first non-loopback IPv4 address.
func Subnet24() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLoopback() || ipn.IP.To4() == nil {
			continue
		}
		ip := ipn.IP.To4()
		return fmt.Sprintf("%d.%d.%d.0/24", ip[0], ip[1], ip[2]), nil
	}
	return "", fmt.Errorf("no non-loopback IPv4 interface")
}

// Scan runs the RTSP sweep and WS-Discovery probe concurrently. Budgets
// its own time; returns whatever it found by the ctx deadline.
func Scan(ctx context.Context) []Result {
	subnet, err := Subnet24()
	if err != nil {
		return nil
	}
	var (
		mu   sync.Mutex
		byIP = map[string]*Result{}
		wg   sync.WaitGroup
	)
	upsert := func(ip string) *Result {
		mu.Lock()
		defer mu.Unlock()
		if byIP[ip] == nil {
			byIP[ip] = &Result{IP: ip}
		}
		return byIP[ip]
	}

	// RTSP sweep
	hosts := hostsOf(subnet)
	wg.Add(1)
	go func() {
		defer wg.Done()
		sem := make(chan struct{}, 128)
		var inner sync.WaitGroup
		for _, h := range hosts {
			if ctx.Err() != nil {
				return
			}
			inner.Add(1)
			sem <- struct{}{}
			go func(host string) {
				defer inner.Done()
				defer func() { <-sem }()
				if tcpOpen(host, 554, 400*time.Millisecond) {
					upsert(host).RTSP = true
				}
			}(h)
		}
		inner.Wait()
	}()

	// WS-Discovery (ONVIF NetworkVideoTransmitter probe)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for _, x := range wsDiscover(ctx, 4*time.Second) {
			r := upsert(x.ip)
			if r.ONVIF == "" {
				r.ONVIF = x.xaddr
			}
			if r.Name == "" {
				r.Name = x.name
			}
		}
	}()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}

	mu.Lock()
	defer mu.Unlock()
	out := make([]Result, 0, len(byIP))
	for _, r := range byIP {
		if r.RTSP || r.ONVIF != "" {
			out = append(out, *r)
		}
	}
	sortResults(out)
	return out
}

func hostsOf(cidr string) []string {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil
	}
	var out []string
	for cur := ip.Mask(ipnet.Mask); ipnet.Contains(cur); inc(cur) {
		out = append(out, cur.String())
	}
	return out
}

func inc(ip net.IP) {
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			break
		}
	}
}

func tcpOpen(host string, port int, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprint(port)), timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

type wsHit struct {
	ip    string
	xaddr string
	name  string
}

var (
	xaddrRe = regexp.MustCompile(`<d:XAddrs>([^<]+)</d:XAddrs>`)
	addrRe  = regexp.MustCompile(`<a:Address>([^<]+)</a:Address>`)
)

// wsDiscover sends an ONVIF-scoped WS-Discovery Probe to the multicast
// group and collects XAddrs from ProbeMatches.
func wsDiscover(ctx context.Context, wait time.Duration) []wsHit {
	gaddr := &net.UDPAddr{IP: net.ParseIP("239.255.255.250"), Port: 3702}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil
	}
	defer conn.Close()

	msg := `<?xml version="1.0" encoding="UTF-8"?>
<e:Envelope xmlns:e="http://www.w3.org/2003/05/soap-envelope"
 xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing"
 xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery"
 xmlns:dn="http://www.onvif.org/ver10/network/wsdl">
 <e:Header>
  <a:MessageID>uuid:pocketnvr-` + fmt.Sprint(time.Now().UnixNano()) + `</a:MessageID>
  <a:To e:mustUnderstand="true">urn:schemas-xmlsoap-org:ws:2005:04:discovery</a:To>
  <a:Action e:mustUnderstand="true">http://schemas.xmlsoap.org/ws/2005/04/discovery/Probe</a:Action>
 </e:Header>
 <e:Body>
  <d:Probe><d:Types>dn:NetworkVideoTransmitter</d:Types></d:Probe>
 </e:Body>
</e:Envelope>`

	if _, err := conn.WriteToUDP([]byte(msg), gaddr); err != nil {
		return nil
	}

	deadline := time.Now().Add(wait)
	var hits []wsHit
	buf := make([]byte, 65536)
	for {
		conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		n, ra, err := conn.ReadFromUDP(buf)
		if err != nil {
			if time.Now().After(deadline) || ctx.Err() != nil {
				return hits
			}
			continue
		}
		body := string(buf[:n])
		m := xaddrRe.FindStringSubmatch(body)
		xaddr := ""
		if len(m) > 1 {
			xaddr = m[1]
		}
		name := ""
		if am := addrRe.FindStringSubmatch(body); len(am) > 1 {
			name = am[1]
		}
		// prefer the xaddr host; else the responder's source IP
		ip := ra.IP.String()
		if len(m) > 1 {
			if h, _, err := net.SplitHostPort(hostOfXAddr(xaddr)); err == nil && h != "" {
				ip = h
			}
		}
		hits = append(hits, wsHit{ip: ip, xaddr: xaddr, name: shortName(name)})
		if time.Now().After(deadline) {
			return hits
		}
	}
}

func hostOfXAddr(x string) string {
	// http://192.168.0.20:8899/onvif/device_service
	re := regexp.MustCompile(`//([^/]+)/`)
	if m := re.FindStringSubmatch(x); len(m) > 1 {
		return m[1]
	}
	return x
}

func shortName(addr string) string {
	// urn:uuid:... → keep the uuid tail only
	if len(addr) > 30 {
		return addr[:30]
	}
	return addr
}

func sortResults(r []Result) {
	for i := 1; i < len(r); i++ {
		for j := i; j > 0 && r[j].IP < r[j-1].IP; j-- {
			r[j], r[j-1] = r[j-1], r[j]
		}
	}
}
