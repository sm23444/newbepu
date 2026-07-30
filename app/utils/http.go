package utils

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

func WithCallbackRedirectPolicy(client *http.Client) *http.Client {
	if client == nil {
		client = NewHttpClient()
	}

	clone := *client
	clone.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	return &clone
}

func NewCallbackHttpClient(timeout time.Duration) *http.Client {
	client := NewHttpClient()
	if timeout > 0 {
		client.Timeout = timeout
	}
	if transport, ok := client.Transport.(*http.Transport); ok {
		transport = transport.Clone()
		transport.DialContext = dialPublicCallback
		client.Transport = transport
	}

	return WithCallbackRedirectPolicy(client)
}

func dialPublicCallback(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}

	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, ip := range ips {
		if isBlockedCallbackIP(ip) {
			lastErr = fmt.Errorf("callback address resolves to a private or local IP: %s", ip)
			continue
		}

		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("callback host %q has no public IP", host)
	}

	return nil, lastErr
}

func isBlockedCallbackIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}

	ipv4 := ip.To4()
	return ipv4 != nil && ipv4[0] == 100 && ipv4[1] >= 64 && ipv4[1] <= 127
}
