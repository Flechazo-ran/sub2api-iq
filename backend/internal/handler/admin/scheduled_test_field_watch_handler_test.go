package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestFieldWatchStatusHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewScheduledTestHandler(service.NewScheduledTestService(nil, nil))
	r := gin.New()
	r.GET("/account-quality-field-watch", h.FieldWatchStatus)

	type envelope struct {
		Code int `json:"code"`
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	get := func(target string) (*httptest.ResponseRecorder, envelope) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		var body envelope
		if w.Code == http.StatusOK {
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
		}
		return w, body
	}

	w, body := get("/account-quality-field-watch")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Zero(t, body.Code)
	require.NotNil(t, body.Data.Items, "items is always a list")
	require.Empty(t, body.Data.Items)

	w, body = get("/account-quality-field-watch?account_ids=58,57,%2057,")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, body.Data.Items, 2)
	first := body.Data.Items[0]
	require.EqualValues(t, 57, first["account_id"])
	for _, key := range []string{"observed", "streak", "last_observed_at", "last_seen_at", "last_miss_at", "verdict", "reason", "confirming", "settled"} {
		require.Contains(t, first, key, "the frontend reads %s", key)
	}
	require.Nil(t, first["last_observed_at"])
	require.Equal(t, false, first["settled"])

	for _, bad := range []string{"?account_ids=abc", "?account_ids=-1", "?account_ids=0"} {
		w, _ = get("/account-quality-field-watch" + bad)
		require.Equal(t, http.StatusBadRequest, w.Code, bad)
	}
}
