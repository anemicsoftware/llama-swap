import { describe, expect, it } from "vitest";
import { powerLimitOptions } from "./powerLimit";
import type { HardwareAccelerator } from "./types";

function accelerator(overrides: Partial<HardwareAccelerator>): HardwareAccelerator {
  return {
    index: 0,
    kind: "gpu",
    vendor: "AMD",
    model: null,
    architecture: null,
    memory: { kind: "dedicated", capacity_bytes: null },
    driver: null,
    power_limit_watts: 225,
    nominal_power_watts: null,
    ...overrides,
  };
}

describe("powerLimitOptions", () => {
  it("is empty without a maximum", () => {
    expect(powerLimitOptions(accelerator({ power_limit_max_watts: null }))).toEqual([]);
  });

  it("steps from the maximum down to the reported minimum", () => {
    const options = powerLimitOptions(
      accelerator({ power_limit_min_watts: 150, power_limit_max_watts: 225 }),
    );
    expect(options[0]).toBe(225);
    expect(options.at(-1)).toBeGreaterThanOrEqual(150);
    expect(options).toEqual([...options].sort((a, b) => b - a));
  });

  it("falls back to half the maximum when the minimum is 0", () => {
    const options = powerLimitOptions(
      accelerator({ power_limit_min_watts: 0, power_limit_max_watts: 200, power_limit_watts: 200 }),
    );
    expect(options.at(-1)).toBeGreaterThanOrEqual(100);
    expect(options).toContain(200);
  });

  it("always includes the current limit", () => {
    const options = powerLimitOptions(
      accelerator({ power_limit_max_watts: 225, power_limit_watts: 100 }),
    );
    expect(options).toContain(100);
  });
});
