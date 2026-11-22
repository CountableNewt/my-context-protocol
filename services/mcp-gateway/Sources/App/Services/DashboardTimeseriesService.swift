import Fluent
import Foundation
import Vapor

/// Builds time-bucket windows and aggregates `RequestLog` rows for dashboard charts.
enum DashboardTimeseriesService {
    static let allowedRangeKeys: Set<String> = [
        "1h", "24h", "7d", "1mo", "3mo", "6mo", "1y", "ytd", "all",
    ]

    static func normalizeRangeKey(_ raw: String?) throws -> String {
        let k = (raw ?? "24h").lowercased().trimmingCharacters(in: .whitespacesAndNewlines)
        guard allowedRangeKeys.contains(k) else {
            let sorted = allowedRangeKeys.sorted().joined(separator: ", ")
            throw Abort(.badRequest, reason: "Invalid range; use one of: \(sorted)")
        }
        return k
    }

    struct BucketDef: Sendable {
        let start: Date
        let end: Date
        let label: String
    }

    struct BinAgg: Sendable {
        var requests: Int
        var successes: Int
        var latencySum: Int
        var latencyCount: Int
    }

    static func buildBucketDefs(
        rangeKey: String,
        now: Date = Date(),
        earliestLog: Date?
    ) throws -> [BucketDef] {
        let cal = Self.utcCalendar
        switch rangeKey {
        case "1h":
            return Self.uniformBuckets(
                ending: now,
                total: 3600,
                width: 300,
                label: Self.formatterHourMinute
            )
        case "24h":
            return Self.uniformBuckets(
                ending: now,
                total: 86400,
                width: 3600,
                label: Self.formatterHourMinute
            )
        case "7d":
            return Self.uniformBuckets(
                ending: now,
                total: 7 * 86400,
                width: 86400,
                label: Self.formatterMonthDay
            )
        case "1mo":
            return Self.uniformBuckets(
                ending: now,
                total: 30 * 86400,
                width: 86400,
                label: Self.formatterMonthDay
            )
        case "3mo":
            return Self.uniformBuckets(
                ending: now,
                total: 90 * 86400,
                width: 86400,
                label: Self.formatterMonthDay
            )
        case "6mo":
            return Self.uniformBuckets(
                ending: now,
                total: 180 * 86400,
                width: 86400,
                label: Self.formatterMonthDay
            )
        case "1y":
            return Self.uniformBuckets(
                ending: now,
                total: 365 * 86400,
                width: 86400,
                label: Self.formatterMonthDay
            )
        case "ytd":
            let y = cal.component(.year, from: now)
            guard let start = cal.date(from: DateComponents(year: y, month: 1, day: 1)) else {
                throw Abort(.internalServerError, reason: "Could not compute YTD start")
            }
            return Self.uniformBuckets(from: start, through: now, width: 86400, label: Self.formatterMonthDay)
        case "all":
            let start = earliestLog ?? now.addingTimeInterval(-86400)
            let span = now.timeIntervalSince(start)
            let width: TimeInterval = span > 180 * 86400 ? 7 * 86400 : 86400
            return Self.uniformBuckets(from: start, through: now, width: width, label: Self.formatterMonthDay)
        default:
            throw Abort(.badRequest, reason: "Unsupported range")
        }
    }

    static func aggregate(
        db: Database,
        projectIds: [UUID],
        buckets: [BucketDef],
        rangeEndInclusive: Date,
        pageSize: Int = 8_000
    ) async throws -> [DashboardTimeseriesBucketDTO] {
        guard !buckets.isEmpty else { return [] }
        guard !projectIds.isEmpty else {
            return buckets.map { Self.emptyDTO(bucket: $0) }
        }

        let rangeStart = buckets[0].start
        var bins: [BinAgg] = Array(
            repeating: BinAgg(requests: 0, successes: 0, latencySum: 0, latencyCount: 0),
            count: buckets.count
        )

        var offset = 0
        while true {
            let chunk = try await RequestLog.query(on: db)
                .filter(\.$project.$id ~~ projectIds)
                .filter(\.$timestamp >= rangeStart)
                .filter(\.$timestamp <= rangeEndInclusive)
                .sort(\.$timestamp, .ascending)
                .limit(pageSize)
                .offset(offset)
                .all()

            if chunk.isEmpty { break }

            for log in chunk {
                guard let ts = log.timestamp else { continue }
                guard let ix = Self.bucketIndex(for: ts, buckets: buckets, rangeEndInclusive: rangeEndInclusive)
                else { continue }
                bins[ix].requests += 1
                if log.countsAsSuccessfulRequestMetric {
                    bins[ix].successes += 1
                }
                if let lat = log.latencyMs {
                    bins[ix].latencySum += lat
                    bins[ix].latencyCount += 1
                }
            }

            offset += chunk.count
            if chunk.count < pageSize { break }
        }

        return zip(buckets, bins).map { b, u in
            let avg: Double? =
                u.latencyCount > 0
                    ? Double(u.latencySum) / Double(u.latencyCount)
                    : nil
            return DashboardTimeseriesBucketDTO(
                label: b.label,
                start: Self.formatISO(b.start),
                end: Self.formatISO(b.end),
                request_count: u.requests,
                success_count: u.successes,
                avg_latency_ms: avg
            )
        }
    }

    // MARK: - Internals

    private static let utcCalendar: Calendar = {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(secondsFromGMT: 0)!
        return c
    }()

    /// Avoid caching `ISO8601DateFormatter` in a `static let` (not `Sendable` under strict concurrency).
    static func formatISO(_ date: Date) -> String {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        return f.string(from: date)
    }

    private static let formatterHourMinute: DateFormatter = {
        let f = DateFormatter()
        f.timeZone = TimeZone(secondsFromGMT: 0)
        f.dateFormat = "MMM d HH:mm"
        f.locale = Locale(identifier: "en_US_POSIX")
        return f
    }()

    private static let formatterMonthDay: DateFormatter = {
        let f = DateFormatter()
        f.timeZone = TimeZone(secondsFromGMT: 0)
        f.dateFormat = "MMM d"
        f.locale = Locale(identifier: "en_US_POSIX")
        return f
    }()

    private static func uniformBuckets(
        ending end: Date,
        total: TimeInterval,
        width: TimeInterval,
        label: DateFormatter
    ) -> [BucketDef] {
        let start = end.addingTimeInterval(-total)
        return uniformBuckets(from: start, through: end, width: width, label: label)
    }

    private static func uniformBuckets(
        from start: Date,
        through end: Date,
        width: TimeInterval,
        label: DateFormatter
    ) -> [BucketDef] {
        var out: [BucketDef] = []
        var t = start
        while t < end {
            let next = min(t.addingTimeInterval(width), end)
            if next <= t { break }
            out.append(BucketDef(start: t, end: next, label: label.string(from: t)))
            t = next
        }
        if out.isEmpty {
            out.append(BucketDef(start: start, end: end, label: label.string(from: start)))
        }
        return out
    }

    private static func bucketIndex(
        for ts: Date,
        buckets: [BucketDef],
        rangeEndInclusive: Date
    ) -> Int? {
        guard let first = buckets.first else { return nil }
        if ts < first.start || ts > rangeEndInclusive { return nil }

        var lo = 0
        var hi = buckets.count - 1
        while lo <= hi {
            let mid = (lo + hi) / 2
            if buckets[mid].start > ts {
                hi = mid - 1
            } else {
                lo = mid + 1
            }
        }
        let i = hi
        guard i >= 0 else { return nil }
        let b = buckets[i]
        let isLast = i == buckets.count - 1
        if isLast {
            guard ts >= b.start, ts <= rangeEndInclusive else { return nil }
        } else {
            guard ts >= b.start, ts < b.end else { return nil }
        }
        return i
    }

    private static func emptyDTO(bucket: BucketDef) -> DashboardTimeseriesBucketDTO {
        DashboardTimeseriesBucketDTO(
            label: bucket.label,
            start: formatISO(bucket.start),
            end: formatISO(bucket.end),
            request_count: 0,
            success_count: 0,
            avg_latency_ms: nil
        )
    }
}
