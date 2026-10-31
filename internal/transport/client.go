package transport

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"time"
)

// NewClient isolates standard transports and rejects redirects.
func NewClient(source *http.Client, insecureTLS bool) (*http.Client, error) {
	client := http.Client{}
	if source != nil {
		client = *source
	}
	var transport *http.Transport
	if client.Transport == nil {
		transport = &http.Transport{
			DialContext:       (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2: true, MaxIdleConns: 100, IdleConnTimeout: 90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second,
		}
	} else if existing, ok := client.Transport.(*http.Transport); ok {
		transport = existing.Clone()
	} else if insecureTLS {
		return nil, errors.New("insecure TLS requires a standard HTTP transport")
	}
	if transport != nil {
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		} else {
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		}
		transport.TLSClientConfig.InsecureSkipVerify = insecureTLS // Explicit per-deployment compatibility requirement.
		client.Transport = transport
	}
	// Redirects could send credentials to a different origin, including for custom clients.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client, nil
}
