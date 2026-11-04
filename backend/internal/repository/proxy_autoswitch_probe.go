package repository

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyurl"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyutil"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ProxyAutoSwitchProbeURL is a public Cloudflare trace page on chatgpt.com: no
// login, no credentials, no quota. Its body carries loc=<country>.
const ProxyAutoSwitchProbeURL = "https://chatgpt.com/cdn-cgi/trace"

const proxyAutoSwitchProbeBodyLimit = 4 * 1024

type proxyAutoSwitchProber struct {
	target  string
	timeout time.Duration
}

func NewProxyAutoSwitchProber() service.ProxyAutoSwitchProber {
	return &proxyAutoSwitchProber{target: ProxyAutoSwitchProbeURL, timeout: service.ProxyAutoSwitchProbeTimeout}
}

// ProbeChatGPT opens a brand-new connection through the proxy (no pooling, no
// keep-alive) and measures the time until response headers arrive.
func (p *proxyAutoSwitchProber) ProbeChatGPT(ctx context.Context, proxyURL string) (service.ProxyAutoSwitchProbeResult, error) {
	var result service.ProxyAutoSwitchProbeResult
	_, parsed, err := proxyurl.Parse(proxyURL)
	if err != nil {
		return result, err
	}
	if parsed == nil {
		return result, errors.New("proxy url is required")
	}
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: p.timeout}).DialContext,
		TLSHandshakeTimeout:   p.timeout,
		ResponseHeaderTimeout: p.timeout,
		DisableKeepAlives:     true,
		ForceAttemptHTTP2:     true,
	}
	if err := proxyutil.ConfigureTransportProxy(transport, parsed); err != nil {
		return result, err
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   p.timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.target, nil)
	if err != nil {
		return result, err
	}
	req.Header.Set("Accept", "text/plain")
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return result, err
	}
	result.LatencyMS = time.Since(start).Milliseconds()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, proxyAutoSwitchProbeBodyLimit))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return result, &service.ProxyAutoSwitchHTTPStatusError{StatusCode: resp.StatusCode}
	}
	for _, line := range strings.Split(string(body), "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok && key == "loc" {
			result.CountryCode = strings.ToUpper(strings.TrimSpace(value))
		}
	}
	return result, nil
}
