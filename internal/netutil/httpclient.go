package netutil

import (
	"net"
	"net/http"
	"time"
)

var sharedHTTPTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	TLSHandshakeTimeout:   5 * time.Second,
	ResponseHeaderTimeout: 30 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
	MaxIdleConns:          100,
	MaxIdleConnsPerHost:   10,
	IdleConnTimeout:       90 * time.Second,
}

func SharedHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Transport: sharedHTTPTransport, Timeout: timeout}
}

func SharedHTTPTransport() http.RoundTripper {
	return sharedHTTPTransport
}
