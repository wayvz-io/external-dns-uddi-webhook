# Research: ExternalDNS webhook provider for Infoblox Universal DDI

Collected 2026-09-13 from GitHub (via `gh api`), the Bazel Central Registry, Renovate docs,
and a read-only probe of the live Infoblox Portal API (`csp.infoblox.com`). Every fact has a source URL.
Code identifiers are quoted verbatim from the sources.

---

## 1. Upstream kubernetes-sigs/external-dns webhook contract

**Latest release:** `v0.22.0` (published 2026-08-20).
https://github.com/kubernetes-sigs/external-dns/releases/tag/v0.22.0

**Module / go.mod** (`go.mod` at v0.22.0): `module sigs.k8s.io/external-dns`, `go 1.26.6`.
No `replace` directives. Notable transitive deps: `k8s.io/api|apimachinery|client-go v0.36.3`,
`github.com/sirupsen/logrus v1.10.1`, `github.com/prometheus/client_golang v1.24.1`.
https://github.com/kubernetes-sigs/external-dns/blob/v0.22.0/go.mod
- Gotcha: importing `sigs.k8s.io/external-dns/provider` drags in k8s.io client-go + controller-runtime
  transitively (see the adguard/hetzner go.mod indirect blocks). Your `go` directive must be >= 1.26.6
  or `go mod tidy` will bump it. Depend on `sigs.k8s.io/external-dns v0.22.0`.

### Import paths

| Package | Import path |
|---|---|
| Provider interface | `sigs.k8s.io/external-dns/provider` |
| Webhook HTTP server (reference) | `sigs.k8s.io/external-dns/provider/webhook/api` |
| Webhook client (what external-dns runs) | `sigs.k8s.io/external-dns/provider/webhook` |
| Endpoints / DomainFilter | `sigs.k8s.io/external-dns/endpoint` |
| Changes | `sigs.k8s.io/external-dns/plan` |

### `provider/webhook/api/httpapi.go` (verbatim)
https://github.com/kubernetes-sigs/external-dns/blob/v0.22.0/provider/webhook/api/httpapi.go

```go
const (
	MediaTypeFormatAndVersion = "application/external.dns.webhook+json;version=1"
	ContentTypeHeader         = "Content-Type"
	UrlAdjustEndpoints        = "/adjustendpoints"
	UrlApplyChanges           = "/applychanges"
	UrlRecords                = "/records"
)

type WebhookServer struct {
	Provider provider.Provider
}

func (p *WebhookServer) RecordsHandler(w http.ResponseWriter, req *http.Request)
func (p *WebhookServer) AdjustEndpointsHandler(w http.ResponseWriter, req *http.Request)
func (p *WebhookServer) NegotiateHandler(w http.ResponseWriter, _ *http.Request)

// StartHTTPApi starts a HTTP server given any provider.
// ...
// - / (GET): initialization, negotiates headers and returns the domain filter
// - /records (GET): returns the current records
// - /records (POST): applies the changes
// - /adjustendpoints (POST): executes the AdjustEndpoints method
func StartHTTPApi(provider provider.Provider, startedChan chan struct{}, readTimeout, writeTimeout time.Duration, providerPort string)
```

Behaviour notes from the source:
- Uses `http.NewServeMux()`; routes `"/"`, `UrlRecords`, `UrlAdjustEndpoints`. `UrlApplyChanges`
  is declared but **not routed**; ApplyChanges is `POST /records` (RecordsHandler switches on method).
- `providerPort` is an `Addr` string (e.g. `"localhost:8888"`), not a bare port.
- Blocks forever; calls logrus `log.Fatal` if listen/serve fails. Signals `startedChan` (if non-nil) after listen.
- No `/healthz` and no `/metrics` — you must run those yourself on a second listener.
- NegotiateHandler: `json.NewEncoder(w).Encode(p.Provider.GetDomainFilter())` with
  `Content-Type: application/external.dns.webhook+json;version=1`.

### `provider/webhook/webhook.go` (client side, what external-dns does)
https://github.com/kubernetes-sigs/external-dns/blob/v0.22.0/provider/webhook/webhook.go
- `const acceptHeader = "Accept"; maxRetries = 5`.
- On startup it GETs `/` with `Accept: <media type>` and **rejects** the provider if the response
  `Content-Type` != `webhookapi.MediaTypeFormatAndVersion`.
- Retries only `5xx` (`isRetryableError`: `statusCode >= 500 && statusCode <= 510`).
- `func (p WebhookProvider) GetDomainFilter() endpoint.DomainFilterInterface`.
- Flag defaults (`pkg/apis/externaldns/types.go`): `--webhook-provider-url` = `http://localhost:8888`,
  `--webhook-provider-read-timeout` = `5s`, `--webhook-provider-write-timeout` = `10s`, `--webhook-server=false`.
  https://github.com/kubernetes-sigs/external-dns/blob/v0.22.0/pkg/apis/externaldns/types.go

### Endpoint table (docs/tutorials/webhook-provider.md)
https://github.com/kubernetes-sigs/external-dns/blob/v0.22.0/docs/tutorials/webhook-provider.md

| Provider method | HTTP | Route | Success code |
|---|---|---|---|
| Negotiate (DomainFilter) | GET | `/` | 200 |
| Records | GET | `/records` | 200 |
| AdjustEndpoints | POST | `/adjustendpoints` | 200 |
| ApplyChanges | POST | `/records` | **204 No Content** |

Exposed endpoints (separate listener): `GET /healthz` (probes), `GET /metrics` (optional).
"The default recommended port for the provider endpoints is `8888`, and should listen only on `localhost`";
"The default recommended port for the exposed endpoints is `8080`, and it should be bound to all interfaces (`0.0.0.0`)".
Status-code rules: `5xx` retried; `4xx` not retried; `3xx` treated as permanent failure.
Best practices added in v0.22: always write a response body (even `{}`), keep error bodies < 1 MiB
(drain cap), honour context cancellation, close request bodies. OpenAPI spec: `api/webhook.yaml`
(operationIds `negotiate`, `getRecords`, `setRecords`, `adjustRecords`).

### `DomainFilter` JSON shape (`endpoint/domain_filter.go`)
https://github.com/kubernetes-sigs/external-dns/blob/v0.22.0/endpoint/domain_filter.go
```go
type DomainFilterInterface interface { Match(domain string) bool }

type DomainFilter struct {
	Filters []string
	exclude []string
	regex *regexp.Regexp
	regexExclusion *regexp.Regexp
}

type domainFilterSerde struct {
	Include      []string `json:"include,omitempty"`
	Exclude      []string `json:"exclude,omitempty"`
	RegexInclude string   `json:"regexInclude,omitempty"`
	RegexExclude string   `json:"regexExclude,omitempty"`
}
func (df *DomainFilter) MarshalJSON() ([]byte, error)   // nil receiver marshals to {}
```
Constructors used by webhooks: `endpoint.NewDomainFilterWithExclusions(filters, exclude []string)`,
`endpoint.NewRegexDomainFilter(re, reExclusion *regexp.Regexp)`.

### `provider.Provider` (`provider/provider.go`)
https://github.com/kubernetes-sigs/external-dns/blob/v0.22.0/provider/provider.go
```go
type Provider interface {
	Records(ctx context.Context) ([]*endpoint.Endpoint, error)
	ApplyChanges(ctx context.Context, changes *plan.Changes) error
	AdjustEndpoints(endpoints []*endpoint.Endpoint) ([]*endpoint.Endpoint, error)
	GetDomainFilter() endpoint.DomainFilterInterface
}
type BaseProvider struct{}   // default AdjustEndpoints (identity) + GetDomainFilter (&endpoint.DomainFilter{})
var SoftError error; func NewSoftError(err error) error; func NewSoftErrorf(format string, a ...any) error
```

### `plan.Changes` (`plan/plan.go`)
https://github.com/kubernetes-sigs/external-dns/blob/v0.22.0/plan/plan.go
```go
type Changes struct {
	Create    []*endpoint.Endpoint `json:"create,omitempty"`
	UpdateOld []*endpoint.Endpoint `json:"updateOld,omitempty"`
	UpdateNew []*endpoint.Endpoint `json:"updateNew,omitempty"`
	Delete    []*endpoint.Endpoint `json:"delete,omitempty"`
}
```

### `endpoint.Endpoint` (`endpoint/endpoint.go`, `endpoint/labels.go`)
https://github.com/kubernetes-sigs/external-dns/blob/v0.22.0/endpoint/endpoint.go
```go
type TTL int64                       // func (ttl TTL) IsConfigured() bool { return ttl > 0 }
type Targets []string
type Labels map[string]string
type ProviderSpecificProperty struct {
	Name  string `json:"name,omitempty"`
	Value string `json:"value,omitempty"`
}
type ProviderSpecific []ProviderSpecificProperty

type Endpoint struct {
	DNSName          string           `json:"dnsName,omitempty"`
	Targets          Targets          `json:"targets,omitempty"`
	RecordType       string           `json:"recordType,omitempty"`
	SetIdentifier    string           `json:"setIdentifier,omitempty"`
	RecordTTL        TTL              `json:"recordTTL,omitempty"`
	Labels           Labels           `json:"labels,omitempty"`
	ProviderSpecific ProviderSpecific `json:"providerSpecific,omitempty"`
	refObjects []*ObjectRef `json:"-"`
}
func NewEndpoint(dnsName, recordType string, targets ...string) *Endpoint
func NewEndpointWithTTL(dnsName, recordType string, ttl TTL, targets ...string) *Endpoint
```
Label constants (`labels.go`): `heritage = "external-dns"`, `OwnerLabelKey = "owner"`,
`ResourceLabelKey = "resource"`.
TXT registry (`registry/txt/registry.go`, mapper `registry/mapper/mapper.go`, template `"%{record_type}"`)
writes TXT targets via `Labels.Serialize(true, ...)` -> `SerializePlain(withQuotes=true)` which produces
`"heritage=external-dns,external-dns/owner=<id>,external-dns/resource=<kind/ns/name>"` **including the
surrounding double quotes**; parsing does `strings.Trim(labelText, "\"")`.
https://github.com/kubernetes-sigs/external-dns/blob/v0.22.0/endpoint/labels.go

### Helm chart sidecar wiring (chart `external-dns-helm-chart-1.22.0`)
https://github.com/kubernetes-sigs/external-dns/blob/external-dns-helm-chart-1.22.0/charts/external-dns/templates/deployment.yaml
- `provider.name: webhook` adds a container `webhook` with `image`, `env`, `args`, `extraVolumeMounts`,
  `resources`, `securityContext`, `livenessProbe`, `readinessProbe`.
- The container declares **only** `ports: - name: http-webhook, containerPort: 8080`; probes default to
  `httpGet.path: /healthz, port: http-webhook`. external-dns reaches the provider via its default
  `http://localhost:8888` (same pod); 8888 is never exposed. `provider.webhook.service.port: 8080` and an
  optional `serviceMonitor` exist for metrics.

---

## 2. Reference webhook implementations

Star counts (2026-09-13): adguard 78, hetzner 62, AbsaOSS infoblox 27, ionos 22, stackit 19;
`glesys/external-dns-glesys-webhook` redirects to `glesys/external-dns-glesys` (0 stars);
`kubernetes-sigs/external-dns-provider-template` does not exist (404). Picked: **adguard, hetzner, AbsaOSS infoblox**.

### 2a. muhlba91/external-dns-provider-adguard (v11.1.3, 2026-08-15)
https://github.com/muhlba91/external-dns-provider-adguard
- Layout: `cmd/webhook/main.go`, `cmd/webhook/init/{configuration,dnsprovider,logging,server}/`,
  `internal/adguard/{client,configuration,provider}.go`, `pkg/webhook/{webhook,mediatype}.go`.
- Deps: `sigs.k8s.io/external-dns v0.22.0`, `github.com/caarlos0/env/v11`, `github.com/go-chi/chi/v5`,
  `github.com/sirupsen/logrus`, `github.com/stretchr/testify`, `go 1.26.6`.
- Config via `caarlos0/env`:
  ```go
  ServerHost  string `env:"SERVER_HOST" envDefault:"localhost"`
  ServerPort  int    `env:"SERVER_PORT" envDefault:"8888"`
  HealthzHost string `env:"HEALTHZ_HOST" envDefault:"0.0.0.0"`
  HealthzPort int    `env:"HEALTHZ_PORT" envDefault:"8080"`
  ServerReadTimeout/ServerWriteTimeout time.Duration `env:"SERVER_READ_TIMEOUT"` / `env:"SERVER_WRITE_TIMEOUT"`
  DomainFilter []string `env:"DOMAIN_FILTER"`; ExcludeDomains `env:"EXCLUDE_DOMAIN_FILTER"`;
  RegexDomainFilter `env:"REGEXP_DOMAIN_FILTER"`; RegexDomainExclusion `env:"REGEXP_DOMAIN_FILTER_EXCLUSION"`
  ```
- Logging: logrus, JSON formatter by default, `LOG_LEVEL`/`LOG_FORMAT` env.
- Server: **own chi router** (does not use upstream `StartHTTPApi`): `r.Get("/")`, `r.Get("/records")`,
  `r.Post("/records")`, `r.Post("/adjustendpoints")` on `SERVER_HOST:SERVER_PORT`; second chi server
  `r.Get("/healthz")`, `r.Get("/metrics", promhttp.Handler())` on `HEALTHZ_HOST:HEALTHZ_PORT`;
  graceful shutdown on SIGHUP/INT/TERM/QUIT with 30 s timeout. Its `pkg/webhook` validates `Accept`/`Content-Type`
  (406 / 415 on failure), sets `Vary: Content-Type`, returns 204 for ApplyChanges.
- Provider embeds `provider.BaseProvider`; has a `Health(ctx) bool` used by `/healthz`.
- Tests: testify `require`, table tests with `t.Run`, a hand-written `MockHTTPClient` implementing the
  client interface (no httptest).
- Build/publish: goreleaser v2 (`dockers_v2`, images `ghcr.io/muhlba91/external-dns-provider-adguard`, tags
  `latest`, tag, sha; linux/darwin/windows amd64/arm64/armv7), cosign `sign-blob`, SBOMs, attestations.
  Dockerfile: `FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab`,
  `USER 20000:20000`, `EXPOSE 8888/tcp 8080/tcp`.
- Release: release-please (`release-type: go`, `include-v-in-tag: true`, conventional-commit sections), then
  goreleaser in `release.yml`. Renovate: `enabledManagers: github-actions, gomod, dockerfile`, actions pinned by digest.
- Helm snippet (README):
  ```yaml
  provider:
    name: webhook
    webhook:
      image: { repository: ghcr.io/muhlba91/external-dns-provider-adguard, tag: latest }
      service: { port: 8888 }
      livenessProbe:  { httpGet: { path: /healthz, port: 8080 }, initialDelaySeconds: 10, timeoutSeconds: 5 }
      readinessProbe: { httpGet: { path: /healthz, port: 8080 }, initialDelaySeconds: 10, timeoutSeconds: 5 }
      env: [ { name: ADGUARD_URL, valueFrom: { secretKeyRef: { name: adguard-configuration, key: url } } }, ... ]
  ```

### 2b. mconfalonieri/external-dns-hetzner-webhook (v1.0.2, 2026-07-25)
https://github.com/mconfalonieri/external-dns-hetzner-webhook
- Layout: `cmd/webhook/main.go`, `internal/hetzner/{configuration.go,cloud/*}`, `internal/server/`,
  `internal/metrics/`, `internal/zonefile/`, `docs/` (mkdocs). Module name `external-dns-hetzner-webhook` (non-URL).
- Deps: `sigs.k8s.io/external-dns v0.21.0`, `github.com/codingconcepts/env`, logrus, testify, `hcloud-go/v2`.
- Config: `codingconcepts/env` tags `env:"HETZNER_API_KEY" required:"true"`, `env:"DRY_RUN" default:"false"`,
  `BATCH_SIZE`, `DOMAIN_FILTER`, `EXCLUDE_DOMAIN_FILTER`, `REGEXP_DOMAIN_FILTER`, `REGEXP_DOMAIN_FILTER_EXCLUSION`.
  Socket options: `WEBHOOK_HOST=localhost`, `WEBHOOK_PORT=8888`, `METRICS_HOST=0.0.0.0`, `METRICS_PORT=8080`,
  `READ_TIMEOUT`/`WRITE_TIMEOUT` (ms, default 60000).
- Server: **uses upstream** `api.StartHTTPApi(provider, startedChan, readTimeout, writeTimeout, addr)` in a goroutine,
  waits on `startedChan`, then flips readiness. Own metrics mux: `/`, `/ready`, `/health`, `/healthz`, `/metrics`.
- Tests: testify assert, table tests, a `mockClient` implementing a narrow client interface; 19 `_test.go` files.
- Build: goreleaser v2 `dockers_v2` to `ghcr.io/mconfalonieri/external-dns-hetzner-webhook` (`{{ .Tag }}`, `latest`),
  `FROM gcr.io/distroless/static-debian11:nonroot`, `USER 20000:20000`, `ADD --chmod=555 ${TARGETPLATFORM}/...`.
  Release on `push: tags:` (manual semver tags), QEMU + buildx, `goreleaser-action@v7`.
- Helm snippet (`docs/deployment.md`): `provider.name: webhook`, `provider.webhook.image.repository`,
  `env[HETZNER_API_KEY from secret]`, probes `/health` and `/ready` on `port: http-webhook`,
  `extraArgs: ["--txt-prefix=reg-%{record_type}."]`.

### 2c. AbsaOSS/external-dns-infoblox-webhook (v1.7.2, 2026-07-21) — NIOS WAPI, not UDDI
https://github.com/AbsaOSS/external-dns-infoblox-webhook
- Layout mirrors adguard (`cmd/webhook/init/{configuration,dnsprovider,logging,server}`, `internal/infoblox/`,
  `internal/metrics/`). Deps: `sigs.k8s.io/external-dns v0.21.0`, `infoblox-go-client/v2 v2.12.0`,
  `github.com/alecthomas/kong` (flags + env: `name:"server-host" env:"SERVER_HOST" default:"127.0.0.1"`,
  `SERVER_PORT=8888`, `HEALTH_CHECK_PORT=8080`, `REGEXP_NAME_FILTER`), logrus, testify.
- Server: **uses upstream** `api.StartHTTPApi(p, wh.Channel, 0, 0, "host:port")`; health mux on `0.0.0.0:<HEALTH_CHECK_PORT>`
  with `/metrics` (promhttp) and `/healthz` that returns 200 only after `StartHTTPApi` signals the channel.
- Record model (`internal/infoblox/infoblox.go`, `common.go`):
  - `Records()` lists zones (`ibclient.ZoneAuth` filtered by `View` + `domainFilter.Match(zone.Fqdn)`), then per zone
    fetches A, Host, CNAME, TXT, NS (and PTR for reverse zones) with paging, and groups by name into
    `ResponseMap{Map map[string]ResponseDetails, RecordType}` -> `ToEndpoints()` (one Endpoint per name+type,
    multiple targets merged).
  - Zone mapping: `findZone(zones, name)` picks the **longest zone FQDN that is a suffix** (`strings.HasSuffix(name, "."+zone.Fqdn)`
    or equal-fold); `ChangesByZone` groups changes per zone and skips records with no matching zone.
  - CNAME vs A: separate WAPI object types (`RecordCNAME.Canonical`, `RecordA.Ipv4Addr`); one object per target.
  - TXT: `// The Infoblox API strips enclosing double quotes from TXT records lacking whitespace.`
    `if target, err2 := strconv.Unquote(ep.Targets[0]); err2 == nil && !strings.Contains(ep.Targets[0], " ") { ep.Targets = endpoint.Targets{target} }`
    i.e. it unquotes external-dns registry TXT targets before writing, so stored text matches what the API returns.
  - `AdjustEndpoints` only adds `ProviderSpecific` `infoblox-ptr-record=true` on A records when CreatePTR is on.
  - View handling: set `View` only on create ("If View is set for the other actions, Infoblox will complain").
- Tests: `mockIBConnector` implementing `ibclient.IBConnector` (CreateObject/GetObject/DeleteObject/UpdateObject)
  with request verification helpers; testify assert.
- Build: goreleaser (`dockers` buildx per-arch + `docker_manifests` to `ghcr.io/absaoss/external-dns-infoblox-webhook`),
  cosign, SBOM, `FROM gcr.io/distroless/static-debian12:nonroot`, `USER 20000:20000`. Release-please (same config as adguard).
- README shows the wire protocol: `curl -H 'Accept: application/external.dns.webhook+json;version=1' localhost:8888/records`
  and POST body `{"Create":null,"UpdateOld":null,"UpdateNew":[{"dnsName":"test.cloud.example.com","targets":["1.3.2.1"],"recordType":"A","recordTTL":300}],"Delete":null}`.

---

## 3. Infoblox Universal DDI Go client

**`github.com/infobloxopen/bloxone-go-client` is deprecated.** Its `go.mod` (v0.4.0) reads:
`// Deprecated: bloxone-go-client is deprecated. Use github.com/infobloxopen/universal-ddi-go-client instead.`
https://github.com/infobloxopen/bloxone-go-client/blob/v0.4.0/go.mod

**Use `github.com/infobloxopen/universal-ddi-go-client`**, latest tag `v0.4.0` (2026-07-15), `go 1.23`.
https://github.com/infobloxopen/universal-ddi-go-client/releases/tag/v0.4.0
CHANGELOG v0.4.0: all identifiers renamed BloxOne -> Universal DDI; env vars `INFOBLOX_PORTAL_KEY` / `INFOBLOX_PORTAL_URL`
(`BLOXONE_API_KEY` / `BLOXONE_CSP_URL` still accepted as deprecated fallbacks).

### Packages
`client` (aggregate), `option`, `dnsconfig`, `dnsdata`, `ipam`, `dfp`, `fw`, `anycast`, `inframgmt`, `infraprovision`,
`keys`, `redirect`, `upgradepolicy`, `clouddiscovery`, `ipamfederation`, `dtc`, `internal` (vendored deps in `vendor/`).

### Client construction (`client/client.go`, `option/option.go`)
https://github.com/infobloxopen/universal-ddi-go-client/blob/main/client/client.go
https://github.com/infobloxopen/universal-ddi-go-client/blob/main/option/option.go
```go
import (
	uddiclient "github.com/infobloxopen/universal-ddi-go-client/client"
	"github.com/infobloxopen/universal-ddi-go-client/option"
	"github.com/infobloxopen/universal-ddi-go-client/dnsconfig"
	"github.com/infobloxopen/universal-ddi-go-client/dnsdata"
)
type APIClient struct { DNSConfigurationAPI *dnsconfig.APIClient; DNSDataAPI *dnsdata.APIClient; /* ... */ }
func NewAPIClient(options ...option.ClientOption) *APIClient
func dnsdata.NewAPIClient(options ...option.ClientOption) *dnsdata.APIClient   // per-service alternative

type ClientOption func(configuration *internal.Configuration)
func WithCSPUrl(cspURL string) ClientOption            // "Optional. Default is https://csp.infoblox.com"
func WithAPIKey(apiKey string) ClientOption
func WithHTTPClient(httpClient *http.Client) ClientOption
func WithDefaultTags(defaultTags map[string]string) ClientOption
func WithClientName(clientName string) ClientOption     // default "universal-ddi-go-client"
func WithDebug(debug bool) ClientOption
func WithRateLimit(requestsPerSecond float64, burst int) ClientOption   // default 25 rps (INFOBLOX_RATE_LIMIT)
func WithRateLimitDisabled() ClientOption
func WithRateLimiter(limiter RateLimiter) ClientOption
func WithRetry(maxRetries int, minWait, maxWait time.Duration) ClientOption  // defaults 1s..30s
func WithRetryDisabled() ClientOption
```
Internals (`internal/client.go`): auth header is `cfg.DefaultHeader["Authorization"] = "Token " + cfg.APIKey`;
also sends `x-infoblox-client` and `x-infoblox-sdk`. Generated request paths are `localBasePath + "/dns/record"` where
the base path resolves to `<CSP URL>/api/ddi/v1` (verified live: `https://csp.infoblox.com/api/ddi/v1/dns/record`).

### `dnsdata.RecordAPI` (`dnsdata/api_record.go`)
https://github.com/infobloxopen/universal-ddi-go-client/blob/main/dnsdata/api_record.go
```go
func (a *RecordAPIService) List(ctx context.Context) RecordAPIListRequest
func (r RecordAPIListRequest) Fields(fields string) RecordAPIListRequest        // -> _fields
func (r RecordAPIListRequest) Filter(filter string) RecordAPIListRequest        // -> _filter
func (r RecordAPIListRequest) Offset(offset int32) RecordAPIListRequest         // -> _offset
func (r RecordAPIListRequest) Limit(limit int32) RecordAPIListRequest           // -> _limit
func (r RecordAPIListRequest) PageToken(pageToken string) RecordAPIListRequest  // -> _page_token
func (r RecordAPIListRequest) OrderBy(orderBy string) RecordAPIListRequest
func (r RecordAPIListRequest) Tfilter(tfilter string) RecordAPIListRequest
func (r RecordAPIListRequest) Inherit(inherit string) RecordAPIListRequest
func (r RecordAPIListRequest) Execute() (*ListRecordResponse, *http.Response, error)

func (a *RecordAPIService) Create(ctx context.Context) RecordAPICreateRequest
func (r RecordAPICreateRequest) Body(body Record) RecordAPICreateRequest
func (r RecordAPICreateRequest) Execute() (*CreateRecordResponse, *http.Response, error)

func (a *RecordAPIService) Read(ctx context.Context, id string) RecordAPIReadRequest     // GET /dns/record/{id}
func (a *RecordAPIService) Update(ctx context.Context, id string) RecordAPIUpdateRequest // PATCH /dns/record/{id}
func (r RecordAPIUpdateRequest) Body(body Record) RecordAPIUpdateRequest
func (r RecordAPIUpdateRequest) Execute() (*UpdateRecordResponse, *http.Response, error)
func (a *RecordAPIService) Delete(ctx context.Context, id string) RecordAPIDeleteRequest // DELETE /dns/record/{id}
func (r RecordAPIDeleteRequest) Execute() (*http.Response, error)

type ListRecordResponse   struct { Results []Record `json:"results,omitempty"`; AdditionalProperties map[string]interface{} }
type CreateRecordResponse struct { Result *Record  `json:"result,omitempty"`;  AdditionalProperties map[string]interface{} }
```
Note: `ListRecordResponse` has **no typed page/total field**; paginate with `_offset`/`_limit` until a short page
(or read `page` out of `AdditionalProperties` if present).

### `dnsdata.Record` (`dnsdata/model_record.go`, key fields verbatim)
https://github.com/infobloxopen/universal-ddi-go-client/blob/main/dnsdata/model_record.go
```go
type Record struct {
	AbsoluteNameSpec   *string                `json:"absolute_name_spec,omitempty"`  // "Synthetic field, used to determine _zone_ and/or _name_in_zone_"
	AbsoluteZoneName   *string                `json:"absolute_zone_name,omitempty"`
	Comment            *string                `json:"comment,omitempty"`
	Disabled           *bool                  `json:"disabled,omitempty"`
	DnsAbsoluteNameSpec *string               `json:"dns_absolute_name_spec,omitempty"`
	DnsRdata           *string                `json:"dns_rdata,omitempty"`            // read-only presentation form
	Id                 *string                `json:"id,omitempty"`
	NameInZone         *string                `json:"name_in_zone,omitempty"`
	Options            map[string]interface{} `json:"options,omitempty"`              // A/AAAA: create_ptr, check_rmz
	ProviderMetadata   map[string]interface{} `json:"provider_metadata,omitempty"`
	Rdata              map[string]interface{} `json:"rdata"`                           // required
	Source             []string               `json:"source,omitempty"`                // STATIC / SYSTEM / DYNAMIC / DELEGATED / DTC
	Tags               map[string]interface{} `json:"tags,omitempty"`
	Ttl                *int64                 `json:"ttl,omitempty"`                   // 0..2147483647; default = zone SOA TTL
	Type               *string                `json:"type,omitempty"`                  // "A","AAAA","CAA","CNAME","DNAME","DHCID","HTTPS","MX","NAPTR","NS","PTR","SOA","SRV","SVCB","TXT","IBMETA"
	View               *string                `json:"view,omitempty"`                  // resource id "dns/view/<uuid>"
	ViewName           *string                `json:"view_name,omitempty"`             // read-only display name
	Zone               *string                `json:"zone,omitempty"`                  // resource id "dns/auth_zone/<uuid>"
	AdditionalProperties map[string]interface{}
}
func NewRecord(rdata map[string]interface{}) *Record
```
Live API schema (`GET /dns/record` method doc, csp.infoblox.com): "For creating a DNS resource record, one of the following
pairs of fields is required: `name_in_zone` and `zone` ... or `absolute_name_spec` and `view` ... The `zone` and `view`
fields cannot be modified while updating a DNS resource record. The `name_in_zone` and `absolute_name_spec` fields can be modified."
`required: ["type","rdata"]`.

### `dnsconfig.AuthZoneAPI` / `AuthZone` / `View`
https://github.com/infobloxopen/universal-ddi-go-client/blob/main/dnsconfig/api_auth_zone.go
https://github.com/infobloxopen/universal-ddi-go-client/blob/main/dnsconfig/model_auth_zone.go
```go
func (a *AuthZoneAPIService) List(ctx context.Context) AuthZoneAPIListRequest   // GET /dns/auth_zone
   .Fields(string).Filter(string).Offset(int32).Limit(int32).PageToken(string).OrderBy(string).Tfilter(string).Inherit(string).Execute() (*ListAuthZoneResponse, *http.Response, error)
func (a *AuthZoneAPIService) Read(ctx context.Context, id string) AuthZoneAPIReadRequest

type AuthZone struct {
	Fqdn         *string `json:"fqdn,omitempty"`          // "Zone FQDN. ... converted to canonical form. Read-only after creation."
	Id           *string `json:"id,omitempty"`
	PrimaryType  *string `json:"primary_type,omitempty"`  // "_external_" | "_cloud_"
	ProtocolFqdn *string `json:"protocol_fqdn,omitempty"` // punycode
	View         *string `json:"view,omitempty"`          // "dns/view/<uuid>"
	Disabled *bool; Comment *string; Tags map[string]interface{}; Nsgs []string; Parent *string; Version *string; ...
}
// dnsconfig/model_view.go
type View struct { Id *string `json:"id,omitempty"`; Name string `json:"name"`; Disabled *bool; Comment *string; ... }
func (a *ViewAPIService) List(ctx context.Context) ViewAPIListRequest            // GET /dns/view, same chain
```

### Filter / paging syntax (verbatim from the live `GET /dns/record` method doc and `dnsdata/docs/RecordAPI.md`)
https://github.com/infobloxopen/universal-ddi-go-client/blob/main/dnsdata/docs/RecordAPI.md
- `_filter`: "A collection of response resources can be filtered by a logical expression string that includes JSON tag
  references to values in each resource, literal values, and logical operators ... Literal values include numbers ...
  and quoted (both single- or double-quoted) literal strings, and 'null'." Operators: `==`, `!=`, `>`, `>=`, `<`, `<=`,
  `and`, `or`, `not`, `~` (regex), `!~`, `()`.
  Examples that work against the live API: `type=='TXT' or type=='CNAME'`; by zone: `zone=="dns/auth_zone/<uuid>"`;
  by name: `absolute_name_spec=="foo.example.com."`; zones: `fqdn=="example.com."`, `view=="dns/view/<uuid>"`.
- `_fields`: comma-separated JSON tags (e.g. `id,name_in_zone,absolute_name_spec,zone,view,type,rdata,ttl`).
- `_offset`: "zero-origin ... If omitted or null the value is assumed to be '0'." `_limit`: "The service may impose maximum value."
  `_page_token`: "service-defined string ... A null value indicates the first page." `_order_by`: `"<tag> asc|desc,..."`.
  `_tfilter` / `_torder_by`: tag filtering/sorting. `_inherit=none|partial|full`.

### Live observations (read-only probe, 2026-09-13)
- IDs: `dns/auth_zone/ae84c3c7-...`, `dns/view/f0e0aca3-...`, `dns/record/004226f0-...`.
- `fqdn` and `absolute_name_spec` are returned **with trailing dot** (`wayvz.io.`, `_tailscale-challenge.wayvz.io.`);
  apex records have `name_in_zone: ""`.
- CNAME: `rdata: {"cname": "sig1.dkim.rorychatterton.com.at.icloudmailadmin.com."}` (trailing dot preserved).
- TXT: `rdata: {"text": "v2=r2ia5UC6Mg..."}` and `dns_rdata: "\"v2=r2ia5UC6Mg...\""`. A record stored with
  `text: "v=spf1 -all"` is presented as `dns_rdata: "\"v=spf1\" \"-all\""` — **UDDI splits unquoted whitespace in
  `text` into separate character-strings**. To keep one string, the `text` value itself must carry the quotes
  (`text: "\"v=spf1 -all\""`). The Terraform provider (`record_txt.go`) passes `text` through untouched (no quoting logic).
  https://github.com/infobloxopen/terraform-provider-bloxone/blob/master/internal/service/dns_data/record_txt.go

### rdata shapes (from the `Record.Rdata` doc; cross-checked with Terraform `bloxone_dns_*_record` docs, v1.6.0)
https://github.com/infobloxopen/terraform-provider-bloxone/tree/master/docs/resources

| Type | rdata keys (required unless noted) |
|---|---|
| A | `address` (IPv4) |
| AAAA | `address` (IPv6) |
| CNAME | `cname` |
| TXT | `text` (optional per API doc; single string) |
| SRV | `priority`, `weight` (optional, default 0), `port`, `target` |
| MX | `exchange`, `preference` |
| NS | `dname` |
| PTR | `dname` (+ `options.address` shortcut) |
| CAA | `flags` (opt, default 0), `tag`, `value` |
| NAPTR | `order`, `preference`, `services`, `replacement`, `flags` (opt), `regexp` (opt) |
| generic | `subfields: [{type, value, length_kind}]` |

Terraform HCL confirms names: `rdata = { address = "10.0.0.10" }`, `rdata = { cname = "example.com" }`,
`rdata = { text = "example.com" }`, `rdata = { port = 80, priority = 10, target = "example.com", weight = 10 }`,
`rdata = { exchange = "mail.example.com", preference = 10 }`; zone passed as `zone = bloxone_dns_auth_zone.example.id`.

---

## 4. Build tooling (Bazel, bzlmod)

BCR `metadata.json` latest versions (queried 2026-09-13):

| Module | Latest | Source |
|---|---|---|
| `rules_go` | **0.63.0** | https://github.com/bazelbuild/bazel-central-registry/blob/main/modules/rules_go/metadata.json |
| `gazelle` | **0.54.0** | .../modules/gazelle/metadata.json |
| `rules_oci` | **2.3.0** | .../modules/rules_oci/metadata.json |
| `aspect_bazel_lib` | **2.22.5** | .../modules/aspect_bazel_lib/metadata.json |
| `rules_pkg` | **1.3.0** | .../modules/rules_pkg/metadata.json |
| `platforms` | **1.1.0** | .../modules/platforms/metadata.json |
| `bazel_skylib` | **1.9.2** | .../modules/bazel_skylib/metadata.json |
| `buildifier_prebuilt` | **8.5.1.4** | .../modules/buildifier_prebuilt/metadata.json |
| `tar.bzl` (used by rules_oci examples) | 0.10.8 | .../modules/tar.bzl/metadata.json |
| `container_structure_test` | 1.22.1 | .../modules/container_structure_test/metadata.json |

Bazel releases: `9.2.0` (2026-07-13) is the latest 9.x; `8.8.0` (2026-08-31). https://github.com/bazelbuild/bazel/releases

### rules_go / gazelle snippets (verbatim from release notes + `docs/go/core/bzlmod.md`)
https://github.com/bazel-contrib/rules_go/releases/tag/v0.63.0
https://github.com/bazel-contrib/rules_go/blob/master/docs/go/core/bzlmod.md
```starlark
bazel_dep(name = "rules_go", version = "0.63.0")
bazel_dep(name = "gazelle", version = "0.54.0")

go_sdk = use_extension("@rules_go//go:extensions.bzl", "go_sdk")
go_sdk.from_file(go_mod = "//:go.mod")        # version from `toolchain` directive, else `go` directive
# or: go_sdk.download(version = "1.26.6")

go_deps = use_extension("@gazelle//:extensions.bzl", "go_deps")
go_deps.from_file(go_mod = "//:go.mod")
use_repo(go_deps, "com_github_caarlos0_env_v11", "io_k8s_sigs_external_dns", ...)  # direct deps only
```
- "When using Bazel 7.1.1 or higher, the `@rules_go//go` target automatically updates the `use_repo` call whenever
  the `go.mod` file changes, using `bazel mod tidy`." Run Go via `bazel run @rules_go//go -- mod tidy`.
- gazelle root `BUILD.bazel`: `load("@bazel_gazelle//:def.bzl", "gazelle")` / `# gazelle:prefix github.com/<org>/<repo>` /
  `gazelle(name = "gazelle")`; run `bazel run //:gazelle`. (With bzlmod the repo is `@gazelle`.)
  https://github.com/bazel-contrib/bazel-gazelle/blob/master/README.md
- `go_deps` returns `extension_metadata(..., reproducible = True)` (`internal/bzlmod/go_deps.bzl`), so Bazel writes **no
  go_deps entries into MODULE.bazel.lock**; go.mod/go.sum are the source of truth.
  https://github.com/bazel-contrib/bazel-gazelle/blob/master/internal/bzlmod/go_deps.bzl
- rules_go 0.63.0 requires Go >= 1.20 toolchains; CI now tests "Bazel 8 and 9 via a matrix" (PR #4670).
- **Bazel 9 gotcha:** rules_go 0.62.0 "crashes on Bazel 9 with duplicate mingw constraints" (issue #4665). Fixed in
  0.63.0 `go/private/platforms.bzl` ("The two must not both be listed: since Bazel 8, `@bazel_tools//tools/cpp:mingw` is an
  alias..."); Bazel side fix bazelbuild/bazel#30488 targets 9.3.0. Use rules_go >= 0.63.0 on Bazel 9.x.
  https://github.com/bazel-contrib/rules_go/issues/4665
- gazelle 0.54.0 declares `bazel_dep(name = "rules_go", version = "0.59.0")`; rules_go 0.63.0 declares
  `bazel_dep(name = "gazelle", version = "0.51.3")` — both resolve upward to 0.63.0/0.54.0 via MVS. No open gazelle issues
  mention Bazel 9 breakage (searched 2026-09-13).
- `go_cross_binary(name, target, platform, sdk_version, compilation_mode)` wraps a `go_binary` for another platform;
  rules_go platform labels: `@rules_go//go/toolchain:linux_amd64`, `@rules_go//go/toolchain:linux_arm64`.
  https://github.com/bazel-contrib/rules_go/blob/master/docs/go/core/rules.md

### rules_oci (verbatim from README / docs at v2.3.0)
https://github.com/bazel-contrib/rules_oci/releases/tag/v2.3.0
https://github.com/bazel-contrib/rules_oci/blob/main/docs/pull.md
https://github.com/bazel-contrib/rules_oci/blob/main/docs/image_index.md
https://github.com/bazel-contrib/rules_oci/blob/main/docs/push.md
```starlark
bazel_dep(name = "rules_oci", version = "2.3.0")
oci = use_extension("@rules_oci//oci:extensions.bzl", "oci")
oci.pull(
    name = "distroless_static",
    image = "gcr.io/distroless/static-debian12",
    tag = "nonroot",                         # keep for Renovate's datasource; digest pins it
    digest = "sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab",
    platforms = ["linux/amd64", "linux/arm64"],
)
use_repo(oci, "distroless_static", "distroless_static_linux_amd64", "distroless_static_linux_arm64")
```
```starlark
load("@rules_go//go:def.bzl", "go_binary")
load("@rules_pkg//pkg:tar.bzl", "pkg_tar")          # or load("@tar.bzl", "tar") as rules_oci examples do
load("@rules_oci//oci:defs.bzl", "oci_image", "oci_image_index", "oci_load", "oci_push")

go_binary(name = "webhook", embed = [":webhook_lib"], pure = "on")
pkg_tar(name = "app_layer", srcs = [":webhook"], package_dir = "/")
oci_image(name = "image", base = "@distroless_static", tars = [":app_layer"], entrypoint = ["/webhook"],
          user = "65532", exposed_ports = ["8888/tcp", "8080/tcp"])
oci_image_index(name = "image_multiarch", images = [":image"],
                platforms = ["@rules_go//go/toolchain:linux_amd64", "@rules_go//go/toolchain:linux_arm64"])  # "experimental, recommended"
oci_load(name = "load", image = ":image", repo_tags = ["ghcr.io/<owner>/<image>:dev"])   # single-arch for local docker
oci_push(name = "push", image = ":image_multiarch", repository = "ghcr.io/<owner>/<image>", remote_tags = ":stamped")
```
- `oci_image_index` "Using Bazel platforms" transitions the same `oci_image` per platform (the `go_binary` inside
  cross-compiles); alternative is `examples/multi_architecture_image/transition.bzl` (`multi_arch` rule setting
  `//command_line_option:platforms`) or two `go_cross_binary` + two `oci_image`.
- `oci_load(name, format, image, loader, repo_tags)`: "Passing anything other than oci_image to the image attribute will
  lead to build time errors" (use `format = "oci"` to load an index).
- `oci_push(name, remote_tags, **kwargs)`: `remote_tags` accepts a list or a stamped text file (`expand_template_rule`).
- Digest retrieval: `crane digest gcr.io/distroless/static-debian12:nonroot` or
  `docker buildx imagetools inspect gcr.io/distroless/static-debian12:nonroot`. Verified index digest on 2026-09-13 via
  `GET https://gcr.io/v2/distroless/static-debian12/manifests/nonroot` (`docker-content-digest`):
  `sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab` (linux/amd64 `sha256:52dcfbab...`,
  linux/arm64 `sha256:06c3c14b...`). Same digest the adguard Dockerfile pins. The `nonroot` image runs as uid/gid 65532.

---

## 5. Renovate

- **`bazel-module` manager** (https://docs.renovatebot.com/modules/manager/bazel-module/): default
  `managerFilePatterns: ["/(^|/|\\.)MODULE\\.bazel$/"]`; updates `bazel_dep` (datasource `bazel`/BCR), `git_override`,
  `archive_override`, `single_version_override`, `git_repository`, `oci_pull`/`oci.pull` (datasource `docker`, fields
  `image`, `tag`, `digest`), `maven`, `crate.spec`, `rules_img_pull`. "The `bazel-module` manager updates the
  `MODULE.bazel.lock` file when dependencies change" — requires an existing lockfile, Bazelisk, and `bazelModDeps` in
  `allowedUnsafeExecutions`; it runs `bazel mod deps`. Since gazelle `go_deps` is reproducible, Go deps never enter the
  lockfile; only `bazel_dep` bumps touch it.
- **`gomod` manager** (https://docs.renovatebot.com/modules/manager/gomod/): file pattern `/(^|/)go\.mod$/`; depTypes
  `golang` (go directive — not bumped unless `rangeStrategy: "bump"`), `toolchain` (proposed by default), `require`,
  `indirect` (disabled by default), `replace`, `tool`. Runs `go get` to refresh `go.sum`.
  `postUpdateOptions` (https://docs.renovatebot.com/configuration-options/#postupdateoptions): `gomodTidy` (runs
  `go mod tidy`), `gomodTidy1.17`, `gomodTidyE`, `gomodTidyAll`, `gomodUpdateImportPaths` ("Uses the `mod` tool to update
  import paths on major updates"), `gomodMassage`, `gomodVendor`, `gomodSkipVendor`.
- Keeping Bazel `go_deps` in sync: nothing extra — `go_deps.from_file(go_mod="//:go.mod")` reads go.mod/go.sum at fetch
  time. Only when a **new direct** dependency is added does `use_repo(go_deps, ...)` need `bazel mod tidy`
  (automatic via `@rules_go//go` on Bazel >= 7.1.1); version bumps need nothing.
- **`helpers:pinGitHubActionDigests`** (https://docs.renovatebot.com/presets-helpers/): "Pin `github-action` digests."
  ```json
  { "packageRules": [ { "matchDepTypes": ["action", "workflow"], "pinDigests": true } ] }
  ```
  `helpers:pinGitHubActionDigestsToSemver` extends it with `extractVersion: "^(?<version>v?\\d+\\.\\d+\\.\\d+)$"` and a regex
  versioning so the `# vX.Y.Z` comment stays semver.
- Suggested `renovate.json`:
  ```json
  { "extends": ["config:recommended", "helpers:pinGitHubActionDigests"],
    "enabledManagers": ["gomod", "bazel-module", "github-actions"],
    "postUpdateOptions": ["gomodTidy", "gomodUpdateImportPaths"] }
  ```

---

## Recommended design decisions

1. Depend on `sigs.k8s.io/external-dns v0.22.0` and run the provider through upstream
   `api.StartHTTPApi(p, startedChan, readTimeout, writeTimeout, "localhost:8888")` (hetzner/Absa pattern); add your own
   `0.0.0.0:8080` mux with `/healthz` (200 only after `startedChan` fires) and `/metrics` (promhttp). This matches the
   Helm chart (`http-webhook` = 8080, probes `/healthz`) with zero chart overrides.
2. Config via `github.com/caarlos0/env/v11` with the de-facto env names (`SERVER_HOST`, `SERVER_PORT=8888`,
   `HEALTHZ_HOST`, `HEALTHZ_PORT=8080`, `DOMAIN_FILTER`, `EXCLUDE_DOMAIN_FILTER`, `REGEXP_DOMAIN_FILTER`,
   `REGEXP_DOMAIN_FILTER_EXCLUSION`, `DRY_RUN`) plus UDDI-specific `INFOBLOX_PORTAL_KEY`, `INFOBLOX_PORTAL_URL`
   (the SDK already reads these), `UDDI_VIEW` (view **name**, resolved to `dns/view/<uuid>` at startup),
   `UDDI_ZONE_FILTER` (optional). Use `log/slog` (JSON) unless you want logrus parity with upstream.
3. Use `github.com/infobloxopen/universal-ddi-go-client v0.4.0` (not the deprecated bloxone client); construct with
   `client.NewAPIClient(option.WithAPIKey, option.WithCSPUrl, option.WithClientName("external-dns-uddi-webhook"),
   option.WithDefaultTags(...))`. Hide it behind a small interface (`ListZones`, `ListRecords`, `CreateRecord`,
   `UpdateRecord`, `DeleteRecord`) so tests use a hand-written mock (adguard/hetzner/Absa all do this) with table tests + testify.
4. Zone discovery: `AuthZoneAPI.List(ctx).Filter(fmt.Sprintf("view==%q", viewID)).Fields("id,fqdn,view,primary_type")`,
   keep only `primary_type=="cloud"` zones whose `Fqdn` (trailing dot stripped) passes the `DomainFilter`; cache with a TTL.
   Map endpoints to zones by longest-suffix match (Absa `findZone`). Return the `DomainFilter` on `GET /` so external-dns
   pre-filters.
5. Records: one `RecordAPI.List` per zone with `Filter("zone==\"dns/auth_zone/<id>\" and (type=='A' or type=='AAAA' or
   type=='CNAME' or type=='TXT' or type=='SRV' or type=='MX' or type=='NS')")`, `Fields("id,absolute_name_spec,type,rdata,ttl,zone")`,
   page with `Offset/Limit` (e.g. 1000) until a short page. Group by (name, type) into one `Endpoint` with multiple
   `Targets`; strip the trailing dot from `absolute_name_spec` (external-dns names have none); keep the record IDs in an
   in-memory index keyed by (name, type, target) for Update/Delete.
6. Writes: create with `Record{AbsoluteNameSpec: &fqdnWithDot, View: &viewID, Type: &t, Rdata: ..., Ttl: ttl, Comment, Tags}`
   (one UDDI record per target); updates = diff `UpdateOld` vs `UpdateNew` per (name,type) into create/delete of individual
   targets (PATCH only to change TTL of an unchanged target; `zone`/`view` are immutable). Return 204 on success, 5xx on
   transient SDK errors (so external-dns retries), 4xx on bad input; never 3xx.
7. TXT handling: UDDI splits unquoted whitespace in `rdata.text` into multiple character-strings. In `AdjustEndpoints`
   normalise every TXT target to the quoted form external-dns's registry already uses (`"..."`), write `text` **with** the
   quotes, and on `Records()` return `text` as stored — both sides then agree and the plan stays stable (the registry
   `strings.Trim`s quotes on parse). Set `ProviderSpecific`/TTL defaults in `AdjustEndpoints` too (e.g. TTL 0 -> zone default).
8. rdata mapping: A/AAAA `{"address"}`, CNAME `{"cname": target + "."}`, TXT `{"text"}`, SRV
   `{"priority","weight","port","target"}` parsed from the external-dns `"prio weight port host"` target, MX
   `{"preference","exchange"}` from `"pref host"`, NS `{"dname"}`. Tag every record `external-dns=true` via
   `option.WithDefaultTags` so the Portal UI shows ownership (the TXT registry remains the source of truth).
9. Build with Bazel bzlmod: `rules_go 0.63.0` (required for Bazel 9.x), `gazelle 0.54.0`, `rules_oci 2.3.0`,
   `rules_pkg 1.3.0` (or `tar.bzl`), `platforms 1.1.0`, `bazel_skylib 1.9.2`, `aspect_bazel_lib 2.22.5`,
   `buildifier_prebuilt 8.5.1.4`; `go_sdk.from_file(go_mod="//:go.mod")` with `toolchain go1.26.6`; `pure="on"` static
   binary; base `gcr.io/distroless/static-debian12:nonroot` pinned by digest; `oci_image_index` with rules_go platform
   labels for linux/amd64+arm64; `oci_push` to `ghcr.io/<org>/external-dns-uddi-webhook` with stamped semver tags;
   `container_structure_test` smoke test.
10. Release/maintenance: release-please (`release-type: go`, `include-v-in-tag: true`) producing `vX.Y.Z` tags that trigger
    `bazel run //:push` with tags `vX.Y.Z`, `vX.Y`, `latest`; cosign keyless + provenance attestation; Renovate with
    `gomod` (`gomodTidy`, `gomodUpdateImportPaths`), `bazel-module` (bazel_dep + `oci.pull` digest), `github-actions` with
    `helpers:pinGitHubActionDigests`. Document the Helm values snippet (`provider.name: webhook`,
    `provider.webhook.image.repository`, `env[INFOBLOX_PORTAL_KEY from secret]`, default probes) and the recommended
    `--txt-prefix=reg-%{record_type}.` so registry TXT records never collide with CNAMEs.
