package httpguard

import (
	"net"
	"net/http"
	"syscall"
	"time"
)

// Address classes an outbound connection refuses.
const (
	ClassMetadata   = "metadata"   // cloud metadata servers, never
	ClassReserved   = "reserved"   // link-local, multicast, unspecified, 0.0.0.0/8, never
	ClassPrivate    = "private"    // loopback, private and CGNAT, only when allowed
	ClassUnresolved = "unresolved" // the dialler did not get an IP address
	ClassRemote     = "remote"     // not loopback, refused by LoopbackClient
)

// BlockedAddress is the refusal of one outbound destination address.
type BlockedAddress struct {
	IP    string
	Class string
}

func (e *BlockedAddress) Error() string { return "outbound address refused (" + e.Class + "): " + e.IP }

var (
	cgnat = mustCIDR("100.64.0.0/10") // Tailscale and carrier NAT
	// Cloud metadata endpoints outside link-local, refused even when private addresses are allowed.
	metadataIPs = []net.IP{net.ParseIP("100.100.100.200"), net.ParseIP("fd00:ec2::254")}
	thisNet     = mustCIDR("0.0.0.0/8")
)

func mustCIDR(s string) *net.IPNet { _, n, _ := net.ParseCIDR(s); return n }

// CheckOutboundIP: link-local (169.254.0.0/16 metadata included), multicast, unspecified and metadata
// addresses never; loopback, private and CGNAT only with allowPrivate. Returns *BlockedAddress.
func CheckOutboundIP(ip net.IP, allowPrivate bool) error {
	if ip == nil {
		return &BlockedAddress{Class: ClassUnresolved}
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, m := range metadataIPs {
		if ip.Equal(m) {
			return &BlockedAddress{IP: ip.String(), Class: ClassMetadata}
		}
	}
	if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || thisNet.Contains(ip) {
		return &BlockedAddress{IP: ip.String(), Class: ClassReserved}
	}
	if !allowPrivate && PrivateIP(ip) {
		return &BlockedAddress{IP: ip.String(), Class: ClassPrivate}
	}
	return nil
}

// PrivateIP: a loopback, private (RFC 1918 / ULA) or CGNAT address; false for nil.
func PrivateIP(ip net.IP) bool {
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || cgnat.Contains(ip))
}

// OutboundClient dials only addresses CheckOutboundIP allows, checked at connect time after DNS (so a
// DNS answer cannot move it), ignores proxy settings so the check sees the real destination, and never
// follows a redirect. mapErr (optional) rewrites a refusal for the caller's own messages.
func OutboundClient(allowPrivate bool, timeout time.Duration, mapErr func(error) error) *http.Client {
	return guardedClient(timeout, func(ip net.IP) error {
		err := CheckOutboundIP(ip, allowPrivate)
		if err != nil && mapErr != nil {
			return mapErr(err)
		}
		return err
	})
}

// LoopbackClient is OutboundClient that connects to loopback addresses only, checked after DNS,
// so a host name that resolves elsewhere cannot carry an unauthenticated call off this machine.
func LoopbackClient(timeout time.Duration) *http.Client {
	return guardedClient(timeout, func(ip net.IP) error {
		if ip == nil || !ip.IsLoopback() {
			return &BlockedAddress{IP: ip.String(), Class: ClassRemote}
		}
		return nil
	})
}

func guardedClient(timeout time.Duration, check func(net.IP) error) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		h, _, err := net.SplitHostPort(address)
		if err != nil {
			h = ""
		}
		return check(net.ParseIP(h))
	}}
	tr := &http.Transport{Proxy: nil, DialContext: d.DialContext, TLSHandshakeTimeout: 10 * time.Second, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second}
	return &http.Client{Timeout: timeout, Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
