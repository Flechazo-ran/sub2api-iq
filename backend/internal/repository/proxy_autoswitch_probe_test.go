package repository

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProxyAutoSwitchProbeTargetsPublicChatGPTPage(t *testing.T) {
	require.Equal(t, "https://chatgpt.com/cdn-cgi/trace", ProxyAutoSwitchProbeURL)
	p, ok := NewProxyAutoSwitchProber().(*proxyAutoSwitchProber)
	require.True(t, ok)
	require.Equal(t, ProxyAutoSwitchProbeURL, p.target)
	require.Equal(t, 8*time.Second, p.timeout)
}

func TestProxyAutoSwitchProbeUsesFreshConnectionThroughProxy(t *testing.T) {
	var newConns atomic.Int64
	var sawCredentials atomic.Bool
	var wrongHost atomic.Bool
	proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			sawCredentials.Store(true)
		}
		// The plain-HTTP target reaches the proxy as an absolute-URI request.
		if r.URL.Host != "upstream.invalid" {
			wrongHost.Store(true)
		}
		_, _ = io.WriteString(w, "fl=1\nip=203.0.113.9\nloc=jp\ntls=TLSv1.3\n")
	}))
	proxy.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConns.Add(1)
		}
	}
	proxy.Start()
	t.Cleanup(proxy.Close)

	p := &proxyAutoSwitchProber{target: "http://upstream.invalid/cdn-cgi/trace", timeout: 2 * time.Second}
	for i := 0; i < 3; i++ {
		res, err := p.ProbeChatGPT(context.Background(), proxy.URL)
		require.NoError(t, err)
		require.Equal(t, "JP", res.CountryCode)
		require.GreaterOrEqual(t, res.LatencyMS, int64(0))
	}
	require.Equal(t, int64(3), newConns.Load(), "every probe opens a new connection")
	require.False(t, sawCredentials.Load())
	require.False(t, wrongHost.Load(), "request must go through the proxy")
}

func TestProxyAutoSwitchProbeErrors(t *testing.T) {
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(blocked.Close)
	p := &proxyAutoSwitchProber{target: "http://upstream.invalid/cdn-cgi/trace", timeout: 2 * time.Second}

	_, err := p.ProbeChatGPT(context.Background(), blocked.URL)
	var statusErr *service.ProxyAutoSwitchHTTPStatusError
	require.True(t, errors.As(err, &statusErr))
	require.Equal(t, http.StatusForbidden, statusErr.StatusCode)

	_, err = p.ProbeChatGPT(context.Background(), "")
	require.Error(t, err, "never probes without a proxy")

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	p.timeout = 200 * time.Millisecond
	start := time.Now()
	_, err = p.ProbeChatGPT(context.Background(), slow.URL)
	require.Error(t, err)
	require.Less(t, time.Since(start), 2*time.Second)
}
