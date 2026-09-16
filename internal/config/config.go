// Package config loads and validates the webhook's runtime configuration from
// environment variables.
package config

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
	"sigs.k8s.io/external-dns/endpoint"
)

const redacted = "[REDACTED]"

// Config is the full runtime configuration of the webhook.
type Config struct {
	ServerHost         string        `env:"SERVER_HOST" envDefault:"localhost"`
	ServerPort         int           `env:"SERVER_PORT" envDefault:"8888"`
	HealthzHost        string        `env:"HEALTHZ_HOST" envDefault:"0.0.0.0"`
	HealthzPort        int           `env:"HEALTHZ_PORT" envDefault:"8080"`
	ServerReadTimeout  time.Duration `env:"SERVER_READ_TIMEOUT" envDefault:"5s"`
	ServerWriteTimeout time.Duration `env:"SERVER_WRITE_TIMEOUT" envDefault:"10s"`

	IncludeDomains             []string `env:"DOMAIN_FILTER"`
	ExcludeDomains             []string `env:"EXCLUDE_DOMAIN_FILTER"`
	RegexDomainFilter          string   `env:"REGEXP_DOMAIN_FILTER"`
	RegexDomainFilterExclusion string   `env:"REGEXP_DOMAIN_FILTER_EXCLUSION"`

	DryRun    bool   `env:"DRY_RUN" envDefault:"false"`
	LogLevel  string `env:"LOG_LEVEL" envDefault:"info"`
	LogFormat string `env:"LOG_FORMAT" envDefault:"json"`

	// PortalKey is the Infoblox Portal API key. It is never logged.
	PortalKey string `env:"INFOBLOX_PORTAL_KEY"`
	PortalURL string `env:"INFOBLOX_PORTAL_URL" envDefault:"https://csp.infoblox.com"`

	// View is the DNS view *name*; it is resolved to a resource id at startup.
	View string `env:"UDDI_VIEW" envDefault:"default"`
	// ZoneFilter optionally restricts which auth zones are managed, by FQDN.
	// Empty means every zone in the view that the domain filter allows.
	ZoneFilter    []string      `env:"UDDI_ZONE_FILTER"`
	ZoneCacheTTL  time.Duration `env:"UDDI_ZONE_CACHE_TTL" envDefault:"5m"`
	PageLimit     int           `env:"UDDI_PAGE_LIMIT" envDefault:"1000"`
	DefaultTTL    int64         `env:"UDDI_DEFAULT_TTL" envDefault:"0"`
	RecordComment string        `env:"UDDI_RECORD_COMMENT" envDefault:"managed by external-dns"`
	// RawTags is the UDDI_TAGS list of key=value pairs; use Tags() for the parsed map.
	RawTags []string `env:"UDDI_TAGS" envDefault:"external-dns=true"`
}

// Load parses the configuration from the given environment and validates it.
// When environ is nil the process environment is used.
func Load(environ map[string]string) (*Config, error) {
	cfg := &Config{}
	opts := env.Options{}
	if environ != nil {
		opts.Environment = environ
	}
	if err := env.ParseWithOptions(cfg, opts); err != nil {
		return nil, fmt.Errorf("parse environment: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks the configuration for internal consistency.
func (c *Config) Validate() error {
	var errs []error
	errs = append(errs, c.validateServer()...)
	errs = append(errs, c.validateUDDI()...)
	if _, err := c.Tags(); err != nil {
		errs = append(errs, err)
	}
	if _, err := c.DomainFilterSpec(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// validateServer checks the HTTP-server-facing settings: listen addresses,
// timeouts and logging.
func (c *Config) validateServer() []error {
	var errs []error
	if err := validatePort("SERVER_PORT", c.ServerPort); err != nil {
		errs = append(errs, err)
	}
	if err := validatePort("HEALTHZ_PORT", c.HealthzPort); err != nil {
		errs = append(errs, err)
	}
	if c.ServerHost == "" {
		errs = append(errs, errors.New("SERVER_HOST must not be empty"))
	}
	if c.HealthzHost == "" {
		errs = append(errs, errors.New("HEALTHZ_HOST must not be empty"))
	}
	if c.ServerReadTimeout < 0 || c.ServerWriteTimeout < 0 {
		errs = append(errs, errors.New("SERVER_READ_TIMEOUT and SERVER_WRITE_TIMEOUT must not be negative"))
	}
	if _, err := parseLevel(c.LogLevel); err != nil {
		errs = append(errs, err)
	}
	switch strings.ToLower(c.LogFormat) {
	case "json", "text":
	default:
		errs = append(errs, fmt.Errorf("LOG_FORMAT %q must be json or text", c.LogFormat))
	}
	return errs
}

// validateUDDI checks the Infoblox Portal / Universal DDI settings.
func (c *Config) validateUDDI() []error {
	var errs []error
	if c.PortalKey == "" {
		errs = append(errs, errors.New("INFOBLOX_PORTAL_KEY is required"))
	}
	if u, err := url.Parse(c.PortalURL); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		errs = append(errs, fmt.Errorf("INFOBLOX_PORTAL_URL %q must be an absolute http(s) URL", c.PortalURL))
	}
	if c.View == "" {
		errs = append(errs, errors.New("UDDI_VIEW must not be empty"))
	}
	if c.ZoneCacheTTL < 0 {
		errs = append(errs, errors.New("UDDI_ZONE_CACHE_TTL must not be negative"))
	}
	if c.PageLimit < 1 || c.PageLimit > 10000 {
		errs = append(errs, fmt.Errorf("UDDI_PAGE_LIMIT %d must be between 1 and 10000", c.PageLimit))
	}
	if c.DefaultTTL < 0 || c.DefaultTTL > 2147483647 {
		errs = append(errs, fmt.Errorf("UDDI_DEFAULT_TTL %d must be between 0 and 2147483647", c.DefaultTTL))
	}
	return errs
}

func validatePort(name string, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("%s %d must be between 1 and 65535", name, port)
	}
	return nil
}

// Tags parses UDDI_TAGS (key=value entries) into a map.
func (c *Config) Tags() (map[string]string, error) {
	tags := make(map[string]string, len(c.RawTags))
	for _, raw := range c.RawTags {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		k, v, ok := strings.Cut(raw, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("UDDI_TAGS entry %q must be key=value", raw)
		}
		tags[k] = strings.TrimSpace(v)
	}
	return tags, nil
}

// DomainFilterSpec compiles the regular expressions and returns the filter.
// A regex filter takes precedence over the plain include/exclude lists, which
// mirrors external-dns's own flag handling.
func (c *Config) DomainFilterSpec() (*endpoint.DomainFilter, error) {
	if c.RegexDomainFilter != "" || c.RegexDomainFilterExclusion != "" {
		var include, exclude *regexp.Regexp
		var err error
		if c.RegexDomainFilter != "" {
			include, err = regexp.Compile(c.RegexDomainFilter)
			if err != nil {
				return nil, fmt.Errorf("REGEXP_DOMAIN_FILTER: %w", err)
			}
		}
		if c.RegexDomainFilterExclusion != "" {
			exclude, err = regexp.Compile(c.RegexDomainFilterExclusion)
			if err != nil {
				return nil, fmt.Errorf("REGEXP_DOMAIN_FILTER_EXCLUSION: %w", err)
			}
		}
		return endpoint.NewRegexDomainFilter(include, exclude), nil
	}
	return endpoint.NewDomainFilterWithExclusions(clean(c.IncludeDomains), clean(c.ExcludeDomains)), nil
}

// DomainFilter returns the compiled domain filter. It panics only if Validate
// was skipped and the regexes are invalid; call Validate (or Load) first.
func (c *Config) DomainFilter() *endpoint.DomainFilter {
	df, err := c.DomainFilterSpec()
	if err != nil {
		panic(err)
	}
	return df
}

func clean(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ServerAddr is the listen address for the webhook API.
func (c *Config) ServerAddr() string {
	return net.JoinHostPort(c.ServerHost, strconv.Itoa(c.ServerPort))
}

// HealthzAddr is the listen address for the health/metrics endpoints.
func (c *Config) HealthzAddr() string {
	return net.JoinHostPort(c.HealthzHost, strconv.Itoa(c.HealthzPort))
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("LOG_LEVEL %q must be one of debug, info, warn, error", s)
}

// Logger builds a slog.Logger according to LOG_LEVEL and LOG_FORMAT.
func (c *Config) Logger(w io.Writer) *slog.Logger {
	level, err := parseLevel(c.LogLevel)
	if err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if strings.EqualFold(c.LogFormat, "text") {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h)
}

// String renders the configuration with the API key redacted.
func (c *Config) String() string {
	return fmt.Sprintf("server=%s healthz=%s portal_url=%s portal_key=%s view=%s dry_run=%t domain_filter=%v exclude=%v regex=%q regex_exclusion=%q zone_cache_ttl=%s page_limit=%d default_ttl=%d tags=%v",
		c.ServerAddr(), c.HealthzAddr(), c.PortalURL, c.keyState(), c.View, c.DryRun,
		c.IncludeDomains, c.ExcludeDomains, c.RegexDomainFilter, c.RegexDomainFilterExclusion,
		c.ZoneCacheTTL, c.PageLimit, c.DefaultTTL, c.RawTags)
}

// LogValue implements slog.LogValuer with the API key redacted.
func (c *Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("server_addr", c.ServerAddr()),
		slog.String("healthz_addr", c.HealthzAddr()),
		slog.Duration("read_timeout", c.ServerReadTimeout),
		slog.Duration("write_timeout", c.ServerWriteTimeout),
		slog.String("portal_url", c.PortalURL),
		slog.String("portal_key", c.keyState()),
		slog.String("view", c.View),
		slog.Bool("dry_run", c.DryRun),
		slog.Any("domain_filter", c.IncludeDomains),
		slog.Any("exclude_domain_filter", c.ExcludeDomains),
		slog.String("regex_domain_filter", c.RegexDomainFilter),
		slog.String("regex_domain_filter_exclusion", c.RegexDomainFilterExclusion),
		slog.Duration("zone_cache_ttl", c.ZoneCacheTTL),
		slog.Int("page_limit", c.PageLimit),
		slog.Int64("default_ttl", c.DefaultTTL),
		slog.String("record_comment", c.RecordComment),
		slog.Any("tags", c.RawTags),
	)
}

func (c *Config) keyState() string {
	if c.PortalKey == "" {
		return "<unset>"
	}
	return redacted
}
