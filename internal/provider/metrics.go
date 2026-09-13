package provider

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const metricsNamespace = "external_dns_uddi_webhook"

var (
	changesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "changes_total",
		Help:      "Record changes attempted against Universal DDI by action and result.",
	}, []string{"action", "result"})

	apiErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "api_errors_total",
		Help:      "Universal DDI API failures by retryability.",
	}, []string{"retryable"})

	recordsGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Name:      "records",
		Help:      "Number of records returned by the last successful Records call.",
	})

	zonesGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Name:      "zones",
		Help:      "Number of manageable zones discovered in the configured view.",
	})
)
