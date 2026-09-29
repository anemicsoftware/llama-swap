//go:build linux

package hw

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// commandRunner runs an external command and returns its combined output.
// It is a variable so tests can stub privileged calls.
var commandRunner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// pciDevicesRoot is the sysfs directory of PCI devices.
var pciDevicesRoot = "/sys/bus/pci/devices"

// setAMDPowerLimit sets the power cap with `sudo -n rocm-smi`, since the
// amdgpu hwmon files are writable only by root. The result is verified by
// reading the cap back from sysfs because rocm-smi can exit successfully
// without applying the value.
func setAMDPowerLimit(ctx context.Context, pciAddress string, watts int) (float64, error) {
	// sudo's secure_path and a service's PATH usually omit /opt/rocm/bin, so
	// use an absolute path everywhere.
	rocmSmi, err := findROCmSMI()
	if err != nil {
		return 0, fmt.Errorf("rocm-smi not found: %w", err)
	}
	index, err := rocmDeviceIndex(ctx, rocmSmi, pciAddress)
	if err != nil {
		return 0, err
	}
	output, err := commandRunner(ctx, "sudo", "-n", rocmSmi,
		"-d", strconv.Itoa(index), "--setpoweroverdrive", strconv.Itoa(watts), "--autorespond", "yes")
	if err != nil {
		return 0, fmt.Errorf("sudo rocm-smi failed (is passwordless sudo allowed for rocm-smi?): %w: %s", err, trimOutput(output))
	}
	applied, ok := readAMDPowerFile(filepath.Join(pciDevicesRoot, pciAddress), "power1_cap")
	if !ok {
		return 0, fmt.Errorf("could not read back the power limit after setting it")
	}
	if math.Abs(applied-float64(watts)) > 1 {
		return 0, fmt.Errorf("rocm-smi did not apply the limit: requested %d W, device reports %g W: %s", watts, applied, trimOutput(output))
	}
	return applied, nil
}

// rocmDeviceIndex maps a PCI address to rocm-smi's device number.
func rocmDeviceIndex(ctx context.Context, rocmSmi, pciAddress string) (int, error) {
	output, err := commandRunner(ctx, rocmSmi, "--showbus", "--csv")
	if err != nil {
		return 0, fmt.Errorf("querying rocm-smi: %w", err)
	}
	order, byDevice, err := parseROCmTables(string(output))
	if err != nil {
		return 0, err
	}
	for _, device := range order {
		bus := normalizePCIIdentity(firstROCmField(byDevice[device], "pci bus", "pci bus id", "bus"))
		if bus != pciAddress {
			continue
		}
		index, err := strconv.Atoi(strings.TrimPrefix(strings.ToLower(device), "card"))
		if err != nil {
			return 0, fmt.Errorf("unexpected rocm-smi device name %q", device)
		}
		return index, nil
	}
	return 0, fmt.Errorf("rocm-smi does not list device %s", pciAddress)
}
