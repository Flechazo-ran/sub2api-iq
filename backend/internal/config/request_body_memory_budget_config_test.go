package config

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func stubMemoryLimit(t *testing.T, limit uint64) {
	t.Helper()
	original := detectMemoryLimitBytes
	detectMemoryLimitBytes = func() uint64 { return limit }
	t.Cleanup(func() { detectMemoryLimitBytes = original })
}

func TestAutoRequestBodyMemoryBudget(t *testing.T) {
	require.Equal(t, int64(1<<30), autoRequestBodyMemoryBudget(0))
	require.Equal(t, int64(1<<30), autoRequestBodyMemoryBudget(2<<30))
	require.Equal(t, int64(2<<30), autoRequestBodyMemoryBudget(8<<30))
	// 147：容器 8GiB、GOMEMLIMIT=4000MiB 时取三者最小值 4000MiB，1/4 是 1000MiB，按下限取 1GiB。
	require.Equal(t, int64(1<<30), autoRequestBodyMemoryBudget(4000<<20))
	require.Equal(t, int64(1500<<20), autoRequestBodyMemoryBudget(6000<<20))
	require.Equal(t, int64(math.MaxUint64/4), autoRequestBodyMemoryBudget(math.MaxUint64))
}

func TestLoadRequestBodyMemoryBudget(t *testing.T) {
	t.Run("auto-sized from memory limit when unset", func(t *testing.T) {
		resetViperWithJWTSecret(t)
		stubMemoryLimit(t, 128<<30)
		cfg, err := Load()
		require.NoError(t, err)
		require.Equal(t, int64(32<<30), cfg.Gateway.RequestBodyMemoryBudgetBytes)
	})

	t.Run("unknown memory limit falls back to 1 GiB", func(t *testing.T) {
		resetViperWithJWTSecret(t)
		stubMemoryLimit(t, 0)
		cfg, err := Load()
		require.NoError(t, err)
		require.Equal(t, int64(1<<30), cfg.Gateway.RequestBodyMemoryBudgetBytes)
	})

	t.Run("explicit environment value wins, including 0", func(t *testing.T) {
		resetViperWithJWTSecret(t)
		stubMemoryLimit(t, 128<<30)
		t.Setenv("GATEWAY_REQUEST_BODY_MEMORY_BUDGET_BYTES", "0")
		cfg, err := Load()
		require.NoError(t, err)
		require.Zero(t, cfg.Gateway.RequestBodyMemoryBudgetBytes)
	})

	t.Run("empty environment value is ignored like viper does", func(t *testing.T) {
		resetViperWithJWTSecret(t)
		stubMemoryLimit(t, 128<<30)
		t.Setenv("GATEWAY_REQUEST_BODY_MEMORY_BUDGET_BYTES", "")
		cfg, err := Load()
		require.NoError(t, err)
		require.Equal(t, int64(32<<30), cfg.Gateway.RequestBodyMemoryBudgetBytes)
	})

	t.Run("explicit environment value wins over auto-sizing", func(t *testing.T) {
		resetViperWithJWTSecret(t)
		stubMemoryLimit(t, 128<<30)
		t.Setenv("GATEWAY_REQUEST_BODY_MEMORY_BUDGET_BYTES", "2147483648")
		cfg, err := Load()
		require.NoError(t, err)
		require.Equal(t, int64(2<<30), cfg.Gateway.RequestBodyMemoryBudgetBytes)
	})

	t.Run("explicit config file 0 disables the budget", func(t *testing.T) {
		resetViperWithJWTSecret(t)
		stubMemoryLimit(t, 128<<30)
		configFile := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(configFile, []byte("gateway:\n  request_body_memory_budget_bytes: 0\n"), 0o600))
		t.Setenv("CONFIG_FILE", configFile)
		cfg, err := Load()
		require.NoError(t, err)
		require.Zero(t, cfg.Gateway.RequestBodyMemoryBudgetBytes)
	})

	t.Run("config file without the key is auto-sized", func(t *testing.T) {
		resetViperWithJWTSecret(t)
		stubMemoryLimit(t, 128<<30)
		configFile := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(configFile, []byte("gateway:\n  max_body_size: 268435456\n"), 0o600))
		t.Setenv("CONFIG_FILE", configFile)
		cfg, err := Load()
		require.NoError(t, err)
		require.Equal(t, int64(32<<30), cfg.Gateway.RequestBodyMemoryBudgetBytes)
	})

	t.Run("explicit config file value wins", func(t *testing.T) {
		resetViperWithJWTSecret(t)
		stubMemoryLimit(t, 128<<30)
		configFile := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(configFile, []byte("gateway:\n  request_body_memory_budget_bytes: 1073741824\n"), 0o600))
		t.Setenv("CONFIG_FILE", configFile)
		cfg, err := Load()
		require.NoError(t, err)
		require.Equal(t, int64(1<<30), cfg.Gateway.RequestBodyMemoryBudgetBytes)
	})

	t.Run("negative value is rejected", func(t *testing.T) {
		resetViperWithJWTSecret(t)
		t.Setenv("GATEWAY_REQUEST_BODY_MEMORY_BUDGET_BYTES", "-1")
		_, err := Load()
		require.ErrorContains(t, err, "gateway.request_body_memory_budget_bytes")
	})
}

func TestLoadKeepsPerRequestBodyLimitsAt256MB(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, int64(256<<20), cfg.Gateway.MaxBodySize)
	require.Equal(t, int64(256<<20), cfg.Server.MaxRequestBodySize)
}
