package middleware

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRequestBodyMemoryRejectsOverloadAndReleasesAfterHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	budget := pkghttputil.NewBodyMemoryBudget(1024)
	router := gin.New()
	held := make(chan struct{})
	release := make(chan struct{})
	router.POST("/v1/responses", RequestBodyLimit(2048, budget), func(c *gin.Context) {
		_, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
		if RejectRequestBodyMemoryOverload(c, err) {
			return
		}
		if err != nil {
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
		if c.Query("hold") == "1" {
			close(held)
			<-release
		}
		c.Status(http.StatusNoContent)
	})
	request := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(strings.Repeat("x", 700))))
		return w
	}
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { firstDone <- request("/v1/responses?hold=1") }()
	releaseHeld := sync.OnceFunc(func() { close(release) })
	defer releaseHeld()
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not reach handler")
	}
	w := request("/v1/responses")
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Equal(t, "1", w.Header().Get("Retry-After"))
	require.Equal(t, int64(1), budget.Stats().Rejected)
	releaseHeld()
	select {
	case first := <-firstDone:
		require.Equal(t, http.StatusNoContent, first.Code)
	case <-time.After(5 * time.Second):
		t.Fatal("held request did not finish")
	}
	require.Zero(t, budget.Stats().UsedBytes, "handler return must refund the whole request")
	require.Equal(t, http.StatusNoContent, request("/v1/responses").Code)
}

func TestRequestBodyMemoryOverloadResponseFormatPerProtocol(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		path      string
		wantType  string
		anthropic bool
		google    bool
	}{
		{path: "/v1/responses", wantType: "server_error"},
		{path: "/v1/chat/completions", wantType: "server_error"},
		{path: "/v1/messages", wantType: "api_error", anthropic: true},
		{path: "/v1beta/models/gemini:generateContent", google: true},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, nil)
			require.True(t, RejectRequestBodyMemoryOverload(c, pkghttputil.ErrRequestBodyMemoryBudget))
			require.True(t, c.IsAborted())
			require.Equal(t, http.StatusServiceUnavailable, w.Code)
			require.Equal(t, "1", w.Header().Get("Retry-After"))
			var body map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			errObj, _ := body["error"].(map[string]any)
			require.Equal(t, RequestBodyMemoryOverloadMessage, errObj["message"])
			switch {
			case tc.google:
				require.EqualValues(t, http.StatusServiceUnavailable, errObj["code"])
			case tc.anthropic:
				require.Equal(t, "error", body["type"])
				require.Equal(t, tc.wantType, errObj["type"])
			default:
				require.Equal(t, tc.wantType, errObj["type"])
				// A capacity error must never look like a missing model to a client or a downstream gateway.
				require.NotContains(t, w.Body.String(), "model_not_found")
			}
		})
	}
}

func TestRejectRequestBodyMemoryOverloadIgnoresOtherErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, err := range []error{nil, errors.New("connection reset"), &http.MaxBytesError{Limit: 10}} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		require.False(t, RejectRequestBodyMemoryOverload(c, err))
		require.False(t, c.IsAborted())
		require.Equal(t, http.StatusOK, w.Code)
	}
}

func TestRequestBodyLimitNestedUsesOneReservation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	budget := pkghttputil.NewBodyMemoryBudget(4096)
	router := gin.New()
	router.Use(RequestBodyLimit(1<<20, budget))
	router.POST("/v1/embeddings", RequestBodyLimit(1<<20, budget), func(c *gin.Context) {
		body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
		require.NoError(t, err)
		require.Equal(t, int64(cap(body)), budget.Stats().UsedBytes, "nested limits must not open a second reservation")
		c.Status(http.StatusNoContent)
	})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(strings.Repeat("x", 1000))))
	require.Equal(t, http.StatusNoContent, w.Code)
	require.Zero(t, budget.Stats().UsedBytes)
}

func TestRequestBodyLimitWithoutBudgetNeverRejects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, budget := range []*pkghttputil.BodyMemoryBudget{nil, pkghttputil.NewBodyMemoryBudget(0)} {
		router := gin.New()
		router.POST("/v1/responses", RequestBodyLimit(1<<20, budget), func(c *gin.Context) {
			_, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
			require.NoError(t, err)
			c.Status(http.StatusNoContent)
		})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(strings.Repeat("x", 100000))))
		require.Equal(t, http.StatusNoContent, w.Code)
	}
}

func TestAdmissionReadMapsBudgetExhaustionTo503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	budget := pkghttputil.NewBodyMemoryBudget(512)
	router := gin.New()
	reached := false
	router.POST("/v1/responses", RequestBodyLimit(1<<20, budget), func(c *gin.Context) {
		if _, ok := readAdmissionRequestBody(c); !ok {
			return
		}
		reached = true
		c.Status(http.StatusNoContent)
	})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(strings.Repeat("x", 4096))))
	require.False(t, reached)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Equal(t, "1", w.Header().Get("Retry-After"))
	require.Contains(t, w.Body.String(), RequestBodyMemoryOverloadMessage)
	require.Zero(t, budget.Stats().UsedBytes)
}
