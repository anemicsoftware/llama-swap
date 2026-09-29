---
title: Set GPU power limits from the Hardware page
summary: Let the Hardware page change AMD GPU power limits, and what sudo access it needs.
category: guides
tags: [hardware, gpu, power, amd, rocm, sudo]
config_keys: [hardware, hardware.allowPowerCap]
updated: 2026-09-29
---

# Set GPU power limits from the Hardware page

The Hardware page can show a dropdown of power limits for each AMD GPU. It is
off by default, because anyone who can reach the llama-swap API can change
power limits once it is on. Set `apiKeys` if the server is reachable by others.

```yaml
hardware:
  allowPowerCap: true
```

llama-swap runs `sudo -n /path/to/rocm-smi -d N --setpoweroverdrive W`, since
the amdgpu power cap is writable only by root. Allow that without a password in
sudoers (use `visudo`), using the absolute path from `which rocm-smi`:

```
youruser ALL=(root) NOPASSWD: /opt/rocm/bin/rocm-smi
```

The dropdown offers steps from the card's maximum down to its minimum (or half
the maximum when the card reports no minimum). After each change llama-swap
reads the cap back from sysfs and shows what the device reports.

## What goes wrong

- **"sudo rocm-smi failed"**: sudo asked for a password or does not allow the
  command. Check the sudoers rule and that the path matches exactly. `sudo`
  ignores your `PATH`, which is why llama-swap passes an absolute path.
- **"did not apply the limit"**: rocm-smi exited successfully but the device
  kept its old cap, often because the value is outside what the firmware allows.
- **No dropdown**: the option is off, the GPU is not AMD, or the driver does not
  expose `power1_cap_max`. Only the current limit is shown.
- **Limits reset on reboot or driver reload.** The cap is not persistent; use a
  startup hook or a systemd unit to reapply it.
