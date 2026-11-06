package repository

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// A proxy change (what 代理优选 does through the admin edit path) only affects
// requests that pick a client afterwards. A stream already flowing through the
// old proxy keeps its connection until it finishes, in both pool isolation
// modes: account_proxy keys the pool by proxy, and account mode only calls
// CloseIdleConnections on the replaced entry, which never closes active ones.
func TestHTTPUpstreamProxyChangeKeepsInFlightStream(t *testing.T) {
	for _, isolation := range []string{config.ConnectionPoolIsolationAccountProxy, config.ConnectionPoolIsolationAccount} {
		t.Run(isolation, func(t *testing.T) {
			release := make(chan struct{})
			oldProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				flusher, ok := w.(http.Flusher)
				if !ok {
					t.Error("missing Flusher")
					return
				}
				_, _ = io.WriteString(w, "data: first\n\n")
				flusher.Flush()
				<-release
				_, _ = io.WriteString(w, "data: second\n\n")
				flusher.Flush()
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			}))
			t.Cleanup(oldProxy.Close)
			newProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "via-new-proxy")
			}))
			t.Cleanup(newProxy.Close)

			cfg := &config.Config{Gateway: config.GatewayConfig{ConnectionPoolIsolation: isolation}}
			cfg.Security.URLAllowlist.AllowPrivateHosts = true
			upstream := NewHTTPUpstream(cfg)
			const accountID = 65

			streamReq, err := http.NewRequest(http.MethodGet, "http://upstream.invalid/v1/responses", nil)
			require.NoError(t, err)
			stream, err := upstream.Do(streamReq, oldProxy.URL, accountID, 1)
			require.NoError(t, err)
			defer func() { _ = stream.Body.Close() }()
			reader := bufio.NewReader(stream.Body)
			line, err := reader.ReadString('\n')
			require.NoError(t, err)
			require.Equal(t, "data: first\n", line)

			// The account now uses another proxy; the next request goes there.
			nextReq, err := http.NewRequest(http.MethodGet, "http://upstream.invalid/v1/responses", nil)
			require.NoError(t, err)
			next, err := upstream.Do(nextReq, newProxy.URL, accountID, 1)
			require.NoError(t, err)
			body, err := io.ReadAll(next.Body)
			require.NoError(t, err)
			require.NoError(t, next.Body.Close())
			require.Equal(t, "via-new-proxy", string(body))
			svc, ok := upstream.(*httpUpstreamService)
			require.True(t, ok)
			svc.mu.RLock()
			pools := len(svc.clients)
			svc.mu.RUnlock()
			if isolation == config.ConnectionPoolIsolationAccount {
				require.Equal(t, 1, pools, "account mode replaced the old proxy's pool entry")
			} else {
				require.Equal(t, 2, pools, "account_proxy mode keeps one pool per proxy")
			}

			close(release)
			done := make(chan string, 1)
			go func() {
				rest, _ := io.ReadAll(reader)
				done <- string(rest)
			}()
			select {
			case rest := <-done:
				require.True(t, strings.Contains(rest, "data: second") && strings.Contains(rest, "[DONE]"), rest)
			case <-time.After(5 * time.Second):
				t.Fatal("in-flight stream on the old proxy was interrupted")
			}
		})
	}
}
