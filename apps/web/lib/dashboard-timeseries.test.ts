import { describe, expect, it } from "vitest";
import {
  DASHBOARD_TIMESERIES_OPTIONS,
  type DashboardTimeseriesRange,
} from "./dashboard-timeseries";

describe("dashboard-timeseries", () => {
  it("options cover every declared range", () => {
    const values = new Set(DASHBOARD_TIMESERIES_OPTIONS.map((o) => o.value));
    const declared: DashboardTimeseriesRange[] = [
      "1h",
      "24h",
      "7d",
      "1mo",
      "3mo",
      "6mo",
      "1y",
      "ytd",
      "all",
    ];
    for (const v of declared) {
      expect(values.has(v)).toBe(true);
    }
  });
});
