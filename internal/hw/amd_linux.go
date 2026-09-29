//go:build linux

package hw

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func detectAMD(ctx context.Context) []detectedAccelerator {
	var result []detectedAccelerator
	if rocm, err := detectROCm(ctx); err == nil {
		result = append(result, rocm...)
	}
	if kfd, err := detectAMDKFD(
		"/sys/class/kfd/kfd/topology/nodes",
		"/sys/class/drm",
		"/dev/dri",
	); err == nil {
		result = append(result, kfd...)
	}
	return result
}

// rocmSMIFallbackPaths are where ROCm installs rocm-smi. Services often run
// with a PATH that omits /opt/rocm/bin, so PATH alone is not enough.
var rocmSMIFallbackPaths = []string{"/opt/rocm/bin/rocm-smi", "/usr/bin/rocm-smi", "/usr/local/bin/rocm-smi"}

// lookPath resolves executables; a variable so tests can stub it.
var lookPath = exec.LookPath

// findROCmSMI returns the path of rocm-smi, searching PATH first and then the
// usual ROCm install locations.
func findROCmSMI() (string, error) {
	path, err := lookPath("rocm-smi")
	if err == nil {
		return path, nil
	}
	for _, candidate := range rocmSMIFallbackPaths {
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", err
}

func detectROCm(ctx context.Context) ([]detectedAccelerator, error) {
	rocmSmi, err := findROCmSMI()
	if err != nil {
		return nil, err
	}
	// Query each fact separately. Some rocm-smi builds exit successfully but
	// print only a driver row when several --show flags are combined, and one
	// unsupported flag must not discard the device listing.
	output, err := exec.CommandContext(ctx, rocmSmi, "-i", "--showmeminfo", "vram", "--showproductname", "--showbus", "--csv").Output()
	if err != nil {
		return nil, fmt.Errorf("querying rocm-smi: %w", err)
	}
	outputs := []string{string(output)}
	if power, err := exec.CommandContext(ctx, rocmSmi, "--showmaxpower", "--csv").Output(); err == nil {
		outputs = append(outputs, string(power))
	}
	result, err := parseROCmCSV(outputs...)
	if err != nil {
		return nil, err
	}
	if driver, err := exec.CommandContext(ctx, rocmSmi, "--showdriverversion", "--csv").Output(); err == nil {
		if version := parseROCmDriverVersion(string(driver)); version != "" {
			for i := range result {
				result[i].value.Driver = &Driver{Name: stringPtr("amdgpu"), Version: stringPtr(version)}
			}
		}
	}
	return result, nil
}

// parseROCmDriverVersion reads the `name, value` (or `device,Driver version`)
// table printed by `rocm-smi --showdriverversion --csv`.
func parseROCmDriverVersion(output string) string {
	for _, line := range strings.Split(output, "\n") {
		reader := csv.NewReader(strings.NewReader(strings.TrimSpace(line)))
		reader.TrimLeadingSpace = true
		row, err := reader.Read()
		if err != nil || len(row) != 2 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(row[0]), "driver version") {
			return firstROCmField(map[string]string{"v": row[1]}, "v")
		}
		if strings.EqualFold(strings.TrimSpace(row[0]), "system") {
			return firstROCmField(map[string]string{"v": row[1]}, "v")
		}
	}
	return ""
}

// parseROCmTables joins one or more rocm-smi CSV tables on the "device"
// column, returning the device names in first-seen order and their fields.
func parseROCmTables(outputs ...string) ([]string, map[string]map[string]string, error) {
	var order []string
	byDevice := make(map[string]map[string]string)
	for _, output := range outputs {
		scanner := bufio.NewScanner(strings.NewReader(output))
		var header []string
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			row, err := csv.NewReader(strings.NewReader(line)).Read()
			if err != nil {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(row[0]), "device") {
				header = row
				continue
			}
			if len(header) == 0 || len(row) != len(header) {
				continue
			}
			device := strings.TrimSpace(row[0])
			fields := byDevice[device]
			if fields == nil {
				fields = make(map[string]string, len(row))
				byDevice[device] = fields
				order = append(order, device)
			}
			for i := range row {
				fields[strings.ToLower(strings.TrimSpace(header[i]))] = strings.TrimSpace(row[i])
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, nil, err
		}
	}
	return order, byDevice, nil
}

// parseROCmCSV builds accelerators from one or more rocm-smi CSV tables.
// Facts queried by separate invocations end up on the same accelerator.
func parseROCmCSV(outputs ...string) ([]detectedAccelerator, error) {
	order, byDevice, err := parseROCmTables(outputs...)
	if err != nil {
		return nil, err
	}
	var result []detectedAccelerator
	for _, device := range order {
		if a, ok := rocmAccelerator(byDevice[device]); ok {
			result = append(result, a)
		}
	}
	return result, nil
}

func rocmAccelerator(fields map[string]string) (detectedAccelerator, bool) {
	model := firstROCmField(fields, "card series", "device name", "card model")
	architecture := firstROCmField(fields, "gfx version")
	identity := normalizePCIIdentity(firstROCmField(fields, "pci bus", "pci bus id", "bus"))
	if identity == "" {
		identity = firstROCmField(fields, "guid", "unique id")
	}
	memoryBytes, _ := strconv.ParseUint(firstROCmField(fields, "vram total memory (b)"), 10, 64)
	memory := AcceleratorMemory{Kind: "dedicated"}
	if memoryBytes > 0 {
		memory.CapacityBytes = uint64Ptr(memoryBytes)
	}
	var driver *Driver
	if version := nonEmptyStringPtr(firstROCmField(fields, "driver version")); version != nil {
		driver = &Driver{Name: stringPtr("amdgpu"), Version: version}
	}
	power, _ := parseOptionalFloat(firstROCmField(fields, "max graphics package power (w)", "maximum graphics package power (w)"))
	accelerator := Accelerator{
		Kind:         "gpu",
		Vendor:       stringPtr("AMD"),
		Model:        nonEmptyStringPtr(model),
		Architecture: nonEmptyStringPtr(architecture),
		Memory:       memory,
		Driver:       driver,
	}
	if power > 0 {
		accelerator.PowerLimitWatts = float64Ptr(power)
	}
	return detectedAccelerator{identity: identity, value: accelerator}, identity != "" || model != ""
}

func firstROCmField(fields map[string]string, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(fields[name]); value != "" && !strings.EqualFold(value, "n/a") {
			return value
		}
	}
	return ""
}

func detectAMDKFD(nodesRoot, drmRoot, deviceRoot string) ([]detectedAccelerator, error) {
	propertyPaths, err := filepath.Glob(filepath.Join(nodesRoot, "*", "properties"))
	if err != nil {
		return nil, err
	}

	var result []detectedAccelerator
	for _, propertyPath := range propertyPaths {
		properties, err := readKFDProperties(propertyPath)
		if err != nil {
			continue
		}
		architecture := formatGFXTarget(properties["gfx_target_version"])
		renderMinor := properties["drm_render_minor"]
		if architecture == "" || renderMinor == 0 {
			continue
		}

		renderNode := fmt.Sprintf("renderD%d", renderMinor)
		if _, err := os.Stat(filepath.Join(deviceRoot, renderNode)); err != nil {
			continue
		}
		devicePath, err := filepath.EvalSymlinks(filepath.Join(drmRoot, renderNode, "device"))
		if err != nil || pciVendorName(readTrimmed(filepath.Join(devicePath, "vendor"))) != "AMD" {
			continue
		}
		identity := normalizePCIIdentity(filepath.Base(devicePath))
		if identity == "" {
			continue
		}

		accelerator := Accelerator{
			Kind:         "gpu",
			Vendor:       stringPtr("AMD"),
			Architecture: stringPtr(architecture),
			Memory:       AcceleratorMemory{Kind: "unknown"},
		}
		enrichAMDFromSysfs(&accelerator, devicePath)
		result = append(result, detectedAccelerator{identity: identity, value: accelerator})
	}
	return result, nil
}

func readKFDProperties(path string) (map[string]uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	properties := make(map[string]uint64)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err == nil {
			properties[fields[0]] = value
		}
	}
	return properties, scanner.Err()
}

func formatGFXTarget(version uint64) string {
	if version < 10000 {
		return ""
	}
	major := version / 10000
	minor := (version / 100) % 100
	stepping := version % 100
	if major == 0 || minor > 9 || stepping > 15 {
		return ""
	}
	return fmt.Sprintf("gfx%d%d%x", major, minor, stepping)
}

// enrichAMDFromSysfs fills model, dedicated VRAM and the enforced power cap
// from the amdgpu driver's sysfs files, so AMD GPUs are described fully even
// when rocm-smi is missing or unusable. Absent files leave fields unchanged.
func enrichAMDFromSysfs(accelerator *Accelerator, devicePath string) {
	if name := readTrimmed(filepath.Join(devicePath, "product_name")); name != "" {
		accelerator.Model = stringPtr(name)
	}
	if total, err := strconv.ParseUint(readTrimmed(filepath.Join(devicePath, "mem_info_vram_total")), 10, 64); err == nil && total > 0 {
		accelerator.Memory = AcceleratorMemory{Kind: "dedicated", CapacityBytes: uint64Ptr(total)}
	}
	if watts, ok := readAMDPowerFile(devicePath, "power1_cap"); ok {
		accelerator.PowerLimitWatts = float64Ptr(watts)
	}
	// The hardware maximum bounds what the driver accepts. Some cards report
	// a minimum of 0, which is not a usable lower bound, so leave it unset.
	if watts, ok := readAMDPowerFile(devicePath, "power1_cap_max"); ok {
		accelerator.PowerLimitMaxWatts = float64Ptr(watts)
		if min, ok := readAMDPowerFile(devicePath, "power1_cap_min"); ok && min < watts {
			accelerator.PowerLimitMinWatts = float64Ptr(min)
		}
	}
}

// readAMDPowerFile reads a hwmon power file (microwatts) of an amdgpu device
// and returns it in watts. ok is false when absent, unparsable, or zero.
func readAMDPowerFile(devicePath, name string) (float64, bool) {
	paths, _ := filepath.Glob(filepath.Join(devicePath, "hwmon", "hwmon*", name))
	for _, path := range paths {
		if micro, err := strconv.ParseUint(readTrimmed(path), 10, 64); err == nil && micro > 0 {
			return float64(micro) / 1e6, true
		}
	}
	return 0, false
}
