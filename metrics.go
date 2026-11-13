package main

import (
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
)

func renderMetrics(results []accountResult) string {
	registry := prometheus.NewRegistry()
	accountLabels := []string{"provider", "account"}
	quotaLabels := []string{"provider", "account", "quota", "window"}
	success := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "llm_quota_fetch_success", Help: "Whether the latest account quota fetch succeeded."}, accountLabels)
	remaining := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "llm_quota_remaining_ratio", Help: "Fraction of quota remaining."}, quotaLabels)
	reset := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "llm_quota_reset_timestamp_seconds", Help: "Quota reset time as Unix seconds."}, quotaLabels)
	value := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "llm_quota_summary_value", Help: "Provider quota summary value in the labeled unit."}, []string{"provider", "account", "quota", "unit"})
	resets := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "llm_quota_resets_available", Help: "Number of available quota reset credits."}, accountLabels)
	registry.MustRegister(success, remaining, reset, value, resets)
	for _, r := range results {
		success.WithLabelValues(r.Provider, r.Account).Set(boolFloat(r.Success))
		if !r.Success {
			continue
		}
		if r.ResetsAvailable != nil {
			resets.WithLabelValues(r.Provider, r.Account).Set(float64(*r.ResetsAvailable))
		}
		for _, q := range r.Quotas {
			labels := []string{r.Provider, r.Account, q.Group, q.Window}
			if q.Remaining != nil {
				remaining.WithLabelValues(labels...).Set(*q.Remaining)
			}
			if q.Reset > 0 {
				reset.WithLabelValues(labels...).Set(float64(q.Reset))
			}
		}
		for _, v := range r.Values {
			value.WithLabelValues(r.Provider, r.Account, v.Name, v.Unit).Set(v.Value)
		}
	}
	families, _ := registry.Gather()
	var b strings.Builder
	for _, f := range families {
		_, _ = expfmt.MetricFamilyToText(&b, f)
	}
	return b.String()
}

func boolFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
