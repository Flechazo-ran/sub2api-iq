package middleware

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/gin-gonic/gin"
)

// RequestBodyMemoryOverloadMessage is the 503 message for an exhausted request
// body memory budget, kept identical to the downstream CN gateway.
const RequestBodyMemoryOverloadMessage = "Request body buffering capacity is exhausted; retry later"

const requestBodyMemoryOverloadLogInterval = 10 * time.Second

var requestBodyMemoryOverloadLastLog atomic.Int64

// RejectRequestBodyReadError handles early ingress termination before the route
// handler runs. Cancelled clients have no response body and an uncommitted
// response is marked 499, matching the gateway handler's cancellation path.
func RejectRequestBodyReadError(c *gin.Context, err error) bool {
	if err != nil && c != nil && c.Request != nil && c.Request.Context().Err() != nil {
		if !c.Writer.Written() {
			c.Status(499)
		}
		c.Abort()
		return true
	}
	return RejectRequestBodyMemoryOverload(c, err)
}

// RejectRequestBodyMemoryOverload keeps temporary capacity errors distinct
// from invalid JSON and the per-request 413 size limit: an exhausted budget
// answers 503 with Retry-After so the client retries.
func RejectRequestBodyMemoryOverload(c *gin.Context, err error) bool {
	if c == nil || !errors.Is(err, pkghttputil.ErrRequestBodyMemoryBudget) {
		return false
	}
	logRequestBodyMemoryOverload(c)
	c.Header("Retry-After", "1")
	requestBodyMemoryOverloadErrorWriter(c)(c, http.StatusServiceUnavailable, RequestBodyMemoryOverloadMessage)
	c.Abort()
	return true
}

// requestBodyMemoryOverloadErrorWriter picks the protocol format the same way
// as groupModelAllowlistErrorWriter, except that OpenAI clients get a
// server_error: the shared OpenAIErrorWriter labels everything model_not_found,
// and a downstream gateway would then count a capacity error as a client error
// or a missing model instead of failing over.
func requestBodyMemoryOverloadErrorWriter(c *gin.Context) GatewayErrorWriter {
	path := ""
	if c.Request != nil && c.Request.URL != nil {
		path = c.Request.URL.Path
	}
	switch {
	case strings.HasPrefix(path, "/v1beta") || strings.HasPrefix(path, "/antigravity/v1beta"):
		return GoogleErrorWriter
	case strings.Contains(path, "/messages"):
		return AnthropicErrorWriter
	default:
		return openAIServerErrorWriter
	}
}

func openAIServerErrorWriter(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"error": gin.H{"message": message, "type": "server_error"}})
}

// logRequestBodyMemoryOverload writes at most one warning per interval; the
// per-minute ops metrics carry the full rejection count.
func logRequestBodyMemoryOverload(c *gin.Context) {
	now := time.Now().UnixNano()
	last := requestBodyMemoryOverloadLastLog.Load()
	if now-last < int64(requestBodyMemoryOverloadLogInterval) || !requestBodyMemoryOverloadLastLog.CompareAndSwap(last, now) {
		return
	}
	stats, _ := pkghttputil.ActiveBodyMemoryBudgetStats()
	path := ""
	contentLength := int64(-1)
	if c.Request != nil {
		contentLength = c.Request.ContentLength
		if c.Request.URL != nil {
			path = c.Request.URL.Path
		}
	}
	slog.Warn("request_body_memory_budget_exhausted",
		"path", path,
		"limit_bytes", stats.LimitBytes,
		"used_bytes", stats.UsedBytes,
		"rejected_in_interval", stats.Rejected,
		"content_length", contentLength)
}
