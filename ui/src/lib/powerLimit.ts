import type { HardwareAccelerator } from "./types";

/**
 * Power limits (watts, highest first) offered in the Hardware page dropdown:
 * the maximum stepped down in ~5% increments to the minimum, or to half the
 * maximum when the device reports no usable minimum. The current limit is
 * always included so the dropdown never shows a value that isn't selectable.
 * Empty when the accelerator has no adjustable range.
 */
export function powerLimitOptions(accelerator: HardwareAccelerator): number[] {
  const max = accelerator.power_limit_max_watts;
  if (!max || max <= 0) return [];
  const min = accelerator.power_limit_min_watts && accelerator.power_limit_min_watts > 0
    ? accelerator.power_limit_min_watts
    : Math.ceil(max / 2);
  const step = Math.max(5, Math.ceil((max * 0.05) / 5) * 5);

  const values = new Set<number>();
  for (let watts = Math.floor(max); watts >= min; watts -= step) values.add(watts);
  if (accelerator.power_limit_watts) values.add(Math.round(accelerator.power_limit_watts));
  return [...values].sort((a, b) => b - a);
}
