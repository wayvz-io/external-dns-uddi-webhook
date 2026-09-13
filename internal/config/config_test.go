package config

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func baseEnv() map[string]string {
	return map[string]string{"INFOBLOX_PORTAL_KEY": "secret-key-123"}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(baseEnv())
	require.NoError(t, err)

	assert.Equal(t, "localhost", cfg.ServerHost)
	assert.Equal(t, 8888, cfg.ServerPort)
	assert.Equal(t, "0.0.0.0", cfg.HealthzHost)
	assert.Equal(t, 8080, cfg.HealthzPort)
	assert.Equal(t, 5*time.Second, cfg.ServerReadTimeout)
	assert.Equal(t, 10*time.Second, cfg.ServerWriteTimeout)
	assert.Equal(t, "https://csp.infoblox.com", cfg.PortalURL)
	assert.Equal(t, "default", cfg.View)
	assert.Equal(t, 5*time.Minute, cfg.ZoneCacheTTL)
	assert.Equal(t, 1000, cfg.PageLimit)
	assert.Equal(t, int64(0), cfg.DefaultTTL)
	assert.Equal(t, "info", cfg.LogLevel)
	assert.Equal(t, "json", cfg.LogFormat)
	assert.False(t, cfg.DryRun)
	assert.Equal(t, "localhost:8888", cfg.ServerAddr())
	assert.Equal(t, "0.0.0.0:8080", cfg.HealthzAddr())

	tags, err := cfg.Tags()
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"external-dns": "true"}, tags)
}

func TestLoadOverrides(t *testing.T) {
	environ := baseEnv()
	environ["SERVER_HOST"] = "127.0.0.1"
	environ["SERVER_PORT"] = "9999"
	environ["HEALTHZ_PORT"] = "9090"
	environ["SERVER_READ_TIMEOUT"] = "2s"
	environ["DOMAIN_FILTER"] = "example.com, example.org"
	environ["EXCLUDE_DOMAIN_FILTER"] = "internal.example.com"
	environ["DRY_RUN"] = "true"
	environ["LOG_LEVEL"] = "debug"
	environ["LOG_FORMAT"] = "text"
	environ["INFOBLOX_PORTAL_URL"] = "https://portal.example.net/"
	environ["UDDI_VIEW"] = "lab"
	environ["UDDI_ZONE_CACHE_TTL"] = "30s"
	environ["UDDI_PAGE_LIMIT"] = "50"
	environ["UDDI_DEFAULT_TTL"] = "300"
	environ["UDDI_RECORD_COMMENT"] = "k8s"
	environ["UDDI_TAGS"] = "owner=platform, env = prod"

	cfg, err := Load(environ)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:9999", cfg.ServerAddr())
	assert.Equal(t, "0.0.0.0:9090", cfg.HealthzAddr())
	assert.Equal(t, 2*time.Second, cfg.ServerReadTimeout)
	assert.Equal(t, []string{"example.com", " example.org"}, cfg.IncludeDomains)
	assert.True(t, cfg.DryRun)
	assert.Equal(t, "lab", cfg.View)
	assert.Equal(t, 30*time.Second, cfg.ZoneCacheTTL)
	assert.Equal(t, 50, cfg.PageLimit)
	assert.Equal(t, int64(300), cfg.DefaultTTL)
	assert.Equal(t, "k8s", cfg.RecordComment)
	tags, err := cfg.Tags()
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"owner": "platform", "env": "prod"}, tags)

	df := cfg.DomainFilter()
	assert.True(t, df.Match("www.example.com"))
	assert.True(t, df.Match("www.example.org"))
	assert.False(t, df.Match("db.internal.example.com"))
	assert.False(t, df.Match("other.net"))
}

func TestLoadParseError(t *testing.T) {
	environ := baseEnv()
	environ["SERVER_PORT"] = "not-a-number"
	_, err := Load(environ)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse environment")
}

func TestValidate(t *testing.T) {
	valid := func() *Config {
		cfg, err := Load(baseEnv())
		require.NoError(t, err)
		return cfg
	}
	tests := []struct {
		name    string
		mutate  func(c *Config)
		wantErr string
	}{
		{"missing key", func(c *Config) { c.PortalKey = "" }, "INFOBLOX_PORTAL_KEY is required"},
		{"bad server port", func(c *Config) { c.ServerPort = 0 }, "SERVER_PORT"},
		{"bad healthz port", func(c *Config) { c.HealthzPort = 70000 }, "HEALTHZ_PORT"},
		{"empty server host", func(c *Config) { c.ServerHost = "" }, "SERVER_HOST"},
		{"empty healthz host", func(c *Config) { c.HealthzHost = "" }, "HEALTHZ_HOST"},
		{"negative timeout", func(c *Config) { c.ServerReadTimeout = -time.Second }, "SERVER_READ_TIMEOUT"},
		{"relative url", func(c *Config) { c.PortalURL = "csp.infoblox.com" }, "INFOBLOX_PORTAL_URL"},
		{"bad scheme", func(c *Config) { c.PortalURL = "ftp://csp.infoblox.com" }, "INFOBLOX_PORTAL_URL"},
		{"empty view", func(c *Config) { c.View = "" }, "UDDI_VIEW"},
		{"negative cache ttl", func(c *Config) { c.ZoneCacheTTL = -1 }, "UDDI_ZONE_CACHE_TTL"},
		{"page limit low", func(c *Config) { c.PageLimit = 0 }, "UDDI_PAGE_LIMIT"},
		{"page limit high", func(c *Config) { c.PageLimit = 10001 }, "UDDI_PAGE_LIMIT"},
		{"negative ttl", func(c *Config) { c.DefaultTTL = -1 }, "UDDI_DEFAULT_TTL"},
		{"huge ttl", func(c *Config) { c.DefaultTTL = 1 << 40 }, "UDDI_DEFAULT_TTL"},
		{"bad log level", func(c *Config) { c.LogLevel = "loud" }, "LOG_LEVEL"},
		{"bad log format", func(c *Config) { c.LogFormat = "yaml" }, "LOG_FORMAT"},
		{"bad tag", func(c *Config) { c.RawTags = []string{"novalue"} }, "UDDI_TAGS"},
		{"empty tag key", func(c *Config) { c.RawTags = []string{"=v"} }, "UDDI_TAGS"},
		{"bad regex", func(c *Config) { c.RegexDomainFilter = "(" }, "REGEXP_DOMAIN_FILTER"},
		{"bad exclusion regex", func(c *Config) { c.RegexDomainFilterExclusion = "[" }, "REGEXP_DOMAIN_FILTER_EXCLUSION"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid()
			tc.mutate(cfg)
			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}

	t.Run("multiple errors are joined", func(t *testing.T) {
		cfg := valid()
		cfg.PortalKey = ""
		cfg.ServerPort = -1
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "INFOBLOX_PORTAL_KEY")
		assert.Contains(t, err.Error(), "SERVER_PORT")
	})

	t.Run("valid log levels and formats", func(t *testing.T) {
		for _, lvl := range []string{"debug", "INFO", "warn", "warning", "error", ""} {
			cfg := valid()
			cfg.LogLevel = lvl
			assert.NoError(t, cfg.Validate(), lvl)
		}
		for _, f := range []string{"json", "TEXT"} {
			cfg := valid()
			cfg.LogFormat = f
			assert.NoError(t, cfg.Validate(), f)
		}
	})

	t.Run("blank tags are ignored", func(t *testing.T) {
		cfg := valid()
		cfg.RawTags = []string{"", " a=b "}
		tags, err := cfg.Tags()
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"a": "b"}, tags)
	})
}

func TestDomainFilter(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		match   []string
		noMatch []string
	}{
		{
			name:  "no filter matches everything",
			match: []string{"anything.example.com", "x.y"},
		},
		{
			name:    "include list",
			env:     map[string]string{"DOMAIN_FILTER": "example.com"},
			match:   []string{"example.com", "a.example.com"},
			noMatch: []string{"example.org", "notexample.com"},
		},
		{
			name:    "include with exclusion",
			env:     map[string]string{"DOMAIN_FILTER": "example.com", "EXCLUDE_DOMAIN_FILTER": "private.example.com"},
			match:   []string{"web.example.com"},
			noMatch: []string{"db.private.example.com"},
		},
		{
			name:    "regex include",
			env:     map[string]string{"REGEXP_DOMAIN_FILTER": `^.*\.lab\.example\.com$`},
			match:   []string{"a.lab.example.com"},
			noMatch: []string{"a.example.com"},
		},
		{
			name:    "regex takes precedence over include list",
			env:     map[string]string{"DOMAIN_FILTER": "example.org", "REGEXP_DOMAIN_FILTER": `example\.com$`},
			match:   []string{"a.example.com"},
			noMatch: []string{"a.example.org"},
		},
		{
			name:    "regex exclusion only",
			env:     map[string]string{"REGEXP_DOMAIN_FILTER_EXCLUSION": `^ignore\.`},
			match:   []string{"keep.example.com"},
			noMatch: []string{"ignore.example.com"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			environ := baseEnv()
			for k, v := range tc.env {
				environ[k] = v
			}
			cfg, err := Load(environ)
			require.NoError(t, err)
			df := cfg.DomainFilter()
			for _, d := range tc.match {
				assert.True(t, df.Match(d), "should match %s", d)
			}
			for _, d := range tc.noMatch {
				assert.False(t, df.Match(d), "should not match %s", d)
			}
		})
	}

	t.Run("panics on invalid regex when Validate skipped", func(t *testing.T) {
		cfg := &Config{RegexDomainFilter: "("}
		assert.Panics(t, func() { cfg.DomainFilter() })
	})
}

func TestRedaction(t *testing.T) {
	cfg, err := Load(baseEnv())
	require.NoError(t, err)

	s := cfg.String()
	assert.NotContains(t, s, "secret-key-123")
	assert.Contains(t, s, redacted)

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("cfg", "config", cfg)
	assert.NotContains(t, buf.String(), "secret-key-123")
	assert.Contains(t, buf.String(), `"portal_key":"[REDACTED]"`)

	empty := &Config{}
	assert.Contains(t, empty.String(), "<unset>")
}

func TestLogger(t *testing.T) {
	cfg, err := Load(baseEnv())
	require.NoError(t, err)

	var buf bytes.Buffer
	cfg.Logger(&buf).Info("hello", "k", "v")
	assert.True(t, strings.HasPrefix(buf.String(), "{"), "json by default: %s", buf.String())
	assert.Contains(t, buf.String(), `"msg":"hello"`)

	buf.Reset()
	cfg.LogFormat = "text"
	cfg.LogLevel = "warn"
	l := cfg.Logger(&buf)
	l.Info("suppressed")
	l.Warn("shown")
	assert.NotContains(t, buf.String(), "suppressed")
	assert.Contains(t, buf.String(), "msg=shown")

	buf.Reset()
	cfg.LogLevel = "garbage"
	cfg.Logger(&buf).Info("info-with-fallback-level")
	assert.Contains(t, buf.String(), "info-with-fallback-level")
}
