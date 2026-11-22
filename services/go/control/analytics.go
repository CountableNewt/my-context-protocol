package control

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

const successful = `(l.status<>'' AND l.status NOT GLOB '*[^0-9]*' AND CAST(l.status AS INTEGER)>=200 AND CAST(l.status AS INTEGER)<400 AND TRIM(COALESCE(l.error_code,''),char(9)||char(10)||char(11)||char(12)||char(13)||' ')='')`

func scopeWhere(account, pid string) (string, []any) {
	sql := "p.account_id=?"
	args := []any{account}
	if pid != "" {
		sql += " AND p.id=?"
		args = append(args, pid)
	}
	return sql, args
}
func (s *Service) dashboard(r *http.Request, account, pid, kind string) (any, int, error) {
	if kind == "timeseries" {
		return s.timeseries(r, account, pid, false)
	}
	if kind != "summary" {
		return nil, 0, missing("Not found")
	}
	ctx := r.Context()
	where, args := scopeWhere(account, pid)
	join := " FROM request_logs l JOIN projects p ON p.id=l.project_id WHERE " + where
	since := s.now().Unix() - 7*86400
	counts, e := s.one(ctx, "SELECT COUNT(*) AS total_requests,SUM(CASE WHEN l.timestamp>=? THEN 1 ELSE 0 END) AS requests_last_24h,SUM(CASE WHEN l.timestamp>=? THEN 1 ELSE 0 END) AS requests_last_7d"+join, append([]any{s.now().Unix() - 86400, since}, args...)...)
	if e != nil {
		return nil, 0, e
	}
	for _, key := range []string{"total_requests", "requests_last_24h", "requests_last_7d"} {
		if counts[key] == nil {
			counts[key] = int64(0)
		}
	}
	logs, e := s.query(ctx, "SELECT l.method,l.latency_ms,l.project_id,"+successful+" AS success"+join+" AND l.timestamp>=? ORDER BY l.timestamp DESC,l.id DESC LIMIT 10000", append(args, since)...)
	if e != nil {
		return nil, 0, e
	}
	counts["metrics_sample_size_last_7d"] = len(logs)
	counts["success_rate_last_7d"] = nil
	counts["avg_latency_ms_last_7d"] = nil
	counts["p95_latency_ms_last_7d"] = nil
	methods, traffic := map[string]int{}, map[string]int{}
	latencies := []int{}
	success := 0
	for _, log := range logs {
		methods[str(log["method"])]++
		traffic[str(log["project_id"])]++
		if boolean(log["success"]) {
			success++
		}
		if log["latency_ms"] != nil {
			latencies = append(latencies, int(num(log["latency_ms"])))
		}
	}
	if len(logs) > 0 {
		counts["success_rate_last_7d"] = float64(success) / float64(len(logs))
	}
	if len(latencies) > 0 {
		sort.Ints(latencies)
		sum := 0
		for _, n := range latencies {
			sum += n
		}
		counts["avg_latency_ms_last_7d"] = float64(sum) / float64(len(latencies))
		counts["p95_latency_ms_last_7d"] = latencies[int(math.Floor(float64(len(latencies)-1)*0.95))]
	}
	breakdown := []row{}
	for method, count := range methods {
		breakdown = append(breakdown, row{"method": method, "count": count})
	}
	sort.Slice(breakdown, func(i, j int) bool {
		a, b := breakdown[i], breakdown[j]
		if a["count"] != b["count"] {
			return a["count"].(int) > b["count"].(int)
		}
		return str(a["method"]) < str(b["method"])
	})
	counts["method_breakdown_last_7d"] = breakdown
	projects, e := s.query(ctx, "SELECT p.id,p.name,p.active_release_id,r.commit_sha,r.status FROM projects p LEFT JOIN releases r ON r.id=p.active_release_id WHERE "+where, args...)
	if e != nil {
		return nil, 0, e
	}
	caps, e := s.query(ctx, "SELECT d.type,COUNT(*) AS count FROM projects p JOIN compiled_skills c ON c.release_id=p.active_release_id AND c.status='ready' JOIN capability_defs d ON d.compiled_skill_id=c.id WHERE "+where+" GROUP BY d.type", args...)
	if e != nil {
		return nil, 0, e
	}
	capCounts := map[string]int64{}
	for _, cap := range caps {
		capCounts[str(cap["type"])] = int64(num(cap["count"]))
	}
	if pid == "" {
		counts["projects_total"] = len(projects)
		active := 0
		top := []row{}
		for _, p := range projects {
			if p["active_release_id"] != nil {
				active++
			}
			if traffic[str(p["id"])] > 0 {
				top = append(top, row{"project_id": p["id"], "project_name": p["name"], "request_count": traffic[str(p["id"])]})
			}
		}
		sort.Slice(top, func(i, j int) bool {
			if top[i]["request_count"] != top[j]["request_count"] {
				return top[i]["request_count"].(int) > top[j]["request_count"].(int)
			}
			return str(top[i]["project_id"]) < str(top[j]["project_id"])
		})
		if len(top) > 8 {
			top = top[:8]
		}
		counts["top_projects_last_7d"] = top
		counts["projects_with_active_release"] = active
		counts["active_tools_total"] = capCounts["tool"]
		counts["active_resources_total"] = capCounts["resource"]
		counts["active_prompts_total"] = capCounts["prompt"]
	} else {
		counts["project_id"] = pid
		counts["active_release_id"] = nil
		counts["active_commit_sha"] = nil
		counts["active_release_status"] = nil
		if len(projects) > 0 {
			counts["active_release_id"] = projects[0]["active_release_id"]
			counts["active_commit_sha"] = projects[0]["commit_sha"]
			counts["active_release_status"] = projects[0]["status"]
		}
		counts["active_tools"] = capCounts["tool"]
		counts["active_resources"] = capCounts["resource"]
		counts["active_prompts"] = capCounts["prompt"]
		usage, e := s.query(ctx, "SELECT TRIM(mcp_capability_kind) AS kind,TRIM(mcp_capability_key) AS key,COUNT(*) AS invocations_last_7d,SUM(success) AS successful_last_7d FROM (SELECT l.mcp_capability_kind,l.mcp_capability_key,"+successful+" AS success"+join+" AND l.timestamp>=? AND l.mcp_capability_kind IN ('tool','resource','prompt') ORDER BY l.timestamp DESC,l.id DESC LIMIT 50000) WHERE mcp_capability_key IS NOT NULL AND TRIM(mcp_capability_key)<>'' GROUP BY TRIM(mcp_capability_kind),TRIM(mcp_capability_key) ORDER BY invocations_last_7d DESC,kind,key", append(args, since)...)
		if e != nil {
			return nil, 0, e
		}
		counts["capability_usage_last_7d"] = usage
	}
	return counts, 200, nil
}

type bucket struct {
	start, end                                    int64
	label                                         string
	requests, successes, latencySum, latencyCount float64
}

func buildBuckets(key string, now int64, earliest any) ([]bucket, error) {
	seconds, width := int64(0), int64(86400)
	switch key {
	case "1h":
		seconds, width = 3600, 300
	case "24h":
		seconds, width = 86400, 3600
	case "7d":
		seconds = 7 * 86400
	case "1mo":
		seconds = 30 * 86400
	case "3mo":
		seconds = 90 * 86400
	case "6mo":
		seconds = 180 * 86400
	case "1y":
		seconds = 365 * 86400
	case "ytd":
		t := time.Unix(now, 0).UTC()
		seconds = now - time.Date(t.Year(), 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	case "all":
		start := now - 86400
		if earliest != nil {
			start = int64(num(earliest))
		}
		seconds = now - start
		if seconds > 180*86400 {
			width = 7 * 86400
		}
	default:
		return nil, bad("Invalid dashboard range")
	}
	start := now - seconds
	if start > now {
		start = now
	}
	if (now-start)/width > 10000 {
		return nil, bad("Dashboard range is too large")
	}
	out := []bucket{}
	for t := start; t < now; t += width {
		end := t + width
		if end > now {
			end = now
		}
		label := "Jan 2"
		if width < 86400 {
			label = "Jan 2 15:04"
		}
		out = append(out, bucket{start: t, end: end, label: time.Unix(t, 0).UTC().Format(label)})
	}
	if len(out) == 0 {
		out = append(out, bucket{start: start, end: now, label: time.Unix(start, 0).UTC().Format("Jan 2")})
	}
	return out, nil
}
func (s *Service) timeseries(r *http.Request, account, pid string, admin bool) (any, int, error) {
	ctx := r.Context()
	key := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("range")))
	if key == "" {
		key = "24h"
	}
	now := s.now().Unix()
	where, args := scopeWhere(account, pid)
	join := " FROM request_logs l JOIN projects p ON p.id=l.project_id WHERE " + where
	if admin {
		join = " FROM request_logs l WHERE 1=1"
		args = nil
	}
	first, e := s.one(ctx, "SELECT MIN(l.timestamp) AS earliest"+join, args...)
	if e != nil {
		return nil, 0, e
	}
	buckets, e := buildBuckets(key, now, first["earliest"])
	if e != nil {
		return nil, 0, e
	}
	start := buckets[0].start
	var updated any
	if admin {
		rows, e := s.query(ctx, "SELECT hour_start,request_count,success_count,latency_sum_ms,latency_count,updated_at FROM admin_analytics_hourly WHERE hour_start>=? AND hour_start<=? ORDER BY hour_start", start-3600, now)
		if e != nil {
			return nil, 0, e
		}
		for _, v := range rows {
			hour := int64(num(v["hour_start"]))
			if v["updated_at"] != nil && (updated == nil || num(v["updated_at"]) > num(updated)) {
				updated = v["updated_at"]
			}
			for i := range buckets {
				b := &buckets[i]
				lo, hi := hour, hour+3600
				if b.start > lo {
					lo = b.start
				}
				if b.end < hi {
					hi = b.end
				}
				if hi <= lo {
					continue
				}
				fraction := float64(hi-lo) / 3600
				b.requests += num(v["request_count"]) * fraction
				b.successes += num(v["success_count"]) * fraction
				b.latencySum += num(v["latency_sum_ms"]) * fraction
				b.latencyCount += num(v["latency_count"]) * fraction
			}
		}
	} else {
		width := buckets[0].end - start
		if width < 1 {
			width = 1
		}
		query := "SELECT MIN(CAST((l.timestamp-?)/? AS INTEGER),?) AS bucket_index,COUNT(*) AS request_count,SUM(" + successful + ") AS success_count,COALESCE(SUM(l.latency_ms),0) AS latency_sum,COUNT(l.latency_ms) AS latency_count" + join + " AND l.timestamp>=? AND l.timestamp<=? GROUP BY bucket_index"
		binds := append([]any{start, width, len(buckets) - 1}, args...)
		binds = append(binds, start, now)
		rows, e := s.query(ctx, query, binds...)
		if e != nil {
			return nil, 0, e
		}
		for _, v := range rows {
			i := int(num(v["bucket_index"]))
			if i < 0 || i >= len(buckets) {
				continue
			}
			buckets[i].requests = num(v["request_count"])
			buckets[i].successes = num(v["success_count"])
			buckets[i].latencySum = num(v["latency_sum"])
			buckets[i].latencyCount = num(v["latency_count"])
		}
	}
	series := []row{}
	for _, b := range buckets {
		var avg any
		if b.latencyCount > 0 {
			avg = b.latencySum / b.latencyCount
		}
		requests, success := int(math.Round(b.requests)), int(math.Round(b.successes))
		if success > requests {
			success = requests
		}
		series = append(series, row{"label": b.label, "start": date(b.start), "end": date(b.end), "request_count": requests, "success_count": success, "avg_latency_ms": avg})
	}
	out := row{"range_key": key, "range_start": date(start), "range_end": date(now), "buckets": series}
	if pid != "" {
		out["project_id"] = pid
	}
	if admin {
		out["rollup_updated_at"] = date(updated)
		out["data_source_note"] = "Charts use hourly aggregates refreshed by daily maintenance. Values may lag live traffic."
	}
	return out, 200, nil
}
func (s *Service) refreshRollup(ctx context.Context) error {
	cutoff := s.now().Unix() - 500*86400
	_, e := s.db.Batch(ctx, []turso.Statement{{SQL: "DELETE FROM admin_analytics_hourly WHERE hour_start>=?", Args: values(cutoff)}, {SQL: `INSERT INTO admin_analytics_hourly(id,hour_start,request_count,success_count,latency_sum_ms,latency_count,updated_at) SELECT upper(hex(randomblob(4))||'-'||hex(randomblob(2))||'-'||hex(randomblob(2))||'-'||hex(randomblob(2))||'-'||hex(randomblob(6))),CAST(l.timestamp/3600 AS INTEGER)*3600,COUNT(*),SUM(` + successful + `),COALESCE(SUM(l.latency_ms),0),COUNT(l.latency_ms),? FROM request_logs l WHERE l.timestamp>=? GROUP BY CAST(l.timestamp/3600 AS INTEGER)`, Args: values(s.now().Unix(), cutoff)}})
	return e
}
