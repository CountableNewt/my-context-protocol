@testable import App
import Foundation
import Testing

struct DashboardTimeseriesTests {
    @Test func normalizeRangeDefaultIs24h() throws {
        let k = try DashboardTimeseriesService.normalizeRangeKey(nil)
        #expect(k == "24h")
    }

    @Test func allDashboardRangesAreAvailable() throws {
        for range in DashboardTimeseriesService.allowedRangeKeys {
            #expect(try DashboardTimeseriesService.normalizeRangeKey(range) == range)
        }
    }

    @Test func build1hHasTwelveBuckets() throws {
        let end = Date(timeIntervalSince1970: 1_700_000_000)
        let buckets = try DashboardTimeseriesService.buildBucketDefs(
            rangeKey: "1h",
            now: end,
            earliestLog: nil
        )
        #expect(buckets.count == 12)
    }
}
