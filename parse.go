package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

type object = map[string]any

func obj(v any) object  { o, _ := v.(map[string]any); return o }
func array(v any) []any { a, _ := v.([]any); return a }
func str(v any) string  { s, _ := v.(string); return strings.TrimSpace(s) }
func field(o object, keys ...string) any {
	for _, k := range keys {
		if v := o[k]; v != nil {
			return v
		}
	}
	return nil
}
func number(v any) *float64 {
	var n float64
	switch v := v.(type) {
	case float64:
		n = v
	case string:
		var err error
		n, err = strconv.ParseFloat(v, 64)
		if err != nil {
			return nil
		}
	case map[string]any:
		return number(v["val"])
	default:
		return nil
	}
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return nil
	}
	return &n
}
func fraction(v any, divisor float64, used bool) *float64 {
	n := number(v)
	if n == nil || *n < 0 || divisor <= 0 {
		return nil
	}
	x := *n / divisor
	if used {
		x = 1 - x
	}
	x = math.Max(0, math.Min(1, x))
	return &x
}
func timestamp(v any) int64 {
	if s := str(v); s != "" {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil && t.Unix() > 0 {
			return t.Unix()
		}
	}
	if n := number(v); n != nil && *n > 0 && *n < 1e12 {
		return int64(*n)
	}
	return 0
}
func resetTime(o object) int64 {
	if t := timestamp(field(o, "reset_at", "resetAt", "resets_at", "reset_time", "resetTime")); t > 0 {
		return t
	}
	if n := number(field(o, "reset_after_seconds", "resetAfterSeconds", "reset_in", "resetIn", "ttl")); n != nil && *n >= 0 && *n < 1e9 {
		return time.Now().Unix() + int64(*n)
	}
	return 0
}
func duration(v any, fallback string) string {
	n := number(v)
	if n == nil || *n <= 0 {
		return fallback
	}
	if *n == 604800 {
		return "weekly"
	}
	if *n == 86400 {
		return "daily"
	}
	if *n >= 2419200 && *n <= 2678400 {
		return "monthly"
	}
	if math.Mod(*n, 3600) == 0 {
		return fmt.Sprintf("%gh", *n/3600)
	}
	return fmt.Sprintf("%gs", *n)
}
func (d *quotaData) add(group, window string, remaining *float64, reset int64) {
	if remaining != nil || reset > 0 {
		d.Quotas = append(d.Quotas, quota{group, window, remaining, reset})
	}
}
func (d *quotaData) value(name, unit string, v any) {
	if n := number(v); n != nil {
		d.Values = append(d.Values, summary{name, unit, *n})
	}
}

func parseQuota(provider string, o object) (quotaData, error) {
	var d quotaData
	switch provider {
	case "codex":
		limits := func(group string, limit object) {
			for i, key := range []string{"primary_window", "secondary_window"} {
				camel := []string{"primaryWindow", "secondaryWindow"}[i]
				w := obj(field(limit, key, camel))
				d.add(group, duration(field(w, "limit_window_seconds", "limitWindowSeconds"), []string{"primary", "secondary"}[i]), fraction(field(w, "used_percent", "usedPercent"), 100, true), resetTime(w))
			}
		}
		limits("standard", obj(field(o, "rate_limit", "rateLimit")))
		limits("code_review", obj(field(o, "code_review_rate_limit", "codeReviewRateLimit")))
		for i, raw := range array(field(o, "additional_rate_limits", "additionalRateLimits")) {
			a := obj(raw)
			name := str(field(a, "limit_name", "limitName", "metered_feature", "meteredFeature"))
			if name == "" {
				name = fmt.Sprintf("additional_%d", i+1)
			}
			limits(name, obj(field(a, "rate_limit", "rateLimit")))
		}
		d.value("credits_balance", "credits", obj(o["credits"])["balance"])
	case "claude":
		for _, key := range []string{"five_hour", "seven_day", "seven_day_oauth_apps", "seven_day_opus", "seven_day_sonnet", "seven_day_cowork", "iguana_necktie"} {
			w := obj(o[key])
			d.add(key, key, fraction(w["utilization"], 100, true), resetTime(w))
		}
		// The API also supplies named dollar-denominated windows.
		for key, raw := range o {
			w := obj(raw)
			if w["limit_dollars"] != nil && w["remaining_dollars"] != nil {
				d.value(key+"_remaining", "USD", w["remaining_dollars"])
			}
		}
		extra := obj(o["extra_usage"])
		if extra["is_enabled"] == true {
			d.add("extra_usage", "monthly", fraction(extra["utilization"], 100, true), resetTime(extra))
		}
	case "devin":
		plan := obj(obj(o["userStatus"])["planStatus"])
		for _, window := range []string{"daily", "weekly"} {
			d.add("standard", window, fraction(plan[window+"QuotaRemainingPercent"], 100, false), timestamp(plan[window+"QuotaResetAtUnix"]))
		}
	case "meta":
		usage := obj(o["subs_usage"])
		for _, key := range []string{"window", "weekly"} {
			w := obj(usage[key])
			window := key
			if n := number(w["window_duration_mins"]); n != nil {
				window = duration(*n*60, key)
			}
			d.add("standard", window, fraction(w["used_percent"], 100, true), timestamp(w["resets_at"]))
		}
	case "kimi":
		counts := func(group, window string, w object) {
			var remaining *float64
			if limit := number(w["limit"]); limit != nil && *limit > 0 {
				remaining = fraction(w["remaining"], *limit, false)
				if remaining == nil {
					remaining = fraction(w["used"], *limit, true)
				}
			}
			d.add(group, window, remaining, resetTime(w))
		}
		for i, raw := range array(o["limits"]) {
			w := obj(raw)
			detail := obj(w["detail"])
			if detail == nil {
				detail = w
			}
			period := obj(w["window"])
			window := fmt.Sprintf("limit_%d", i+1)
			if n := number(period["duration"]); n != nil {
				unit := strings.ToUpper(str(period["timeUnit"]))
				multipliers := map[string]float64{"SECOND": 1, "MINUTE": 60, "HOUR": 3600, "DAY": 86400, "WEEK": 604800}
				unit = strings.TrimSuffix(strings.TrimPrefix(unit, "TIME_UNIT_"), "S")
				if unit == "" {
					unit = "MINUTE"
				}
				if m := multipliers[unit]; m > 0 {
					window = duration(*n*m, window)
				}
			}
			counts(fmt.Sprintf("limit_%d", i+1), window, detail)
		}
		counts("summary", "weekly", obj(o["usage"]))
		monthly := obj(obj(o["usages"])["limit_month_total"])
		d.add("standard", "monthly", fraction(monthly["used_ratio"], 1, true), resetTime(monthly))
	case "xai":
		c := obj(o["config"])
		period := obj(field(c, "currentPeriod", "current_period"))
		window := "weekly"
		if strings.Contains(strings.ToLower(str(period["type"])), "monthly") {
			window = "monthly"
		}
		reset := timestamp(period["end"])
		if reset == 0 {
			reset = timestamp(field(c, "billingPeriodEnd", "billing_period_end"))
		}
		d.add("credits", window, fraction(field(c, "creditUsagePercent", "credit_usage_percent"), 100, true), reset)
		if limit := number(field(c, "monthlyLimit", "monthly_limit")); limit != nil && *limit > 0 {
			d.add("monthly", "monthly", fraction(c["used"], *limit, true), timestamp(field(c, "billingPeriodEnd", "billing_period_end")))
		}
		if limit := number(field(c, "onDemandCap", "on_demand_cap")); limit != nil && *limit > 0 {
			d.add("on_demand", "monthly", fraction(field(c, "onDemandUsed", "on_demand_used"), *limit, true), timestamp(field(c, "billingPeriodEnd", "billing_period_end")))
		}
		for _, raw := range array(field(c, "productUsage", "product_usage")) {
			p := obj(raw)
			name := str(p["product"])
			if name != "" {
				d.add(name, window, fraction(field(p, "usagePercent", "usage_percent"), 100, true), reset)
			}
		}
		d.value("prepaid_balance", "USD_cents", field(c, "prepaidBalance", "prepaid_balance"))
	default: // Antigravity and CPA's normalized plugin quota contract.
		for i, raw := range array(o["groups"]) {
			group := obj(raw)
			name := str(field(group, "displayName", "display_name"))
			if name == "" {
				name = fmt.Sprintf("group_%d", i+1)
			}
			for j, raw := range array(group["buckets"]) {
				b := obj(raw)
				window := str(b["window"])
				if window == "" {
					window = fmt.Sprintf("bucket_%d", j+1)
				}
				q := name
				if id := str(field(b, "bucketId", "bucket_id")); id != "" {
					q += "/" + id
				}
				d.add(q, window, fraction(field(b, "remainingFraction", "remaining_fraction"), 1, false), resetTime(b))
			}
		}
		for _, raw := range array(o["summary"]) {
			s := obj(raw)
			key := str(s["key"])
			unit := str(s["unit"])
			if str(s["format"]) == "currency" {
				unit = str(s["currency"])
			}
			if key != "" {
				d.value(key, unit, s["value"])
			}
		}
	}
	if len(d.Quotas) == 0 && len(d.Values) == 0 {
		return quotaData{}, errQuota
	}
	return d, nil
}
