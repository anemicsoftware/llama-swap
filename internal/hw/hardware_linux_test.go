//go:build linux

package hw

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHardware_LinuxCgroupMemoryLimit(t *testing.T) {
	dir := t.TempDir()
	limited := filepath.Join(dir, "memory.max")
	unlimited := filepath.Join(dir, "unlimited")
	if err := os.WriteFile(limited, []byte("2147483648\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unlimited, []byte("max\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := limitedMemoryCapacity(8*1024*1024*1024, []string{unlimited, limited}); got != 2*1024*1024*1024 {
		t.Fatalf("limitedMemoryCapacity() = %d", got)
	}
}

func TestHardware_LinuxZoneinfoMemoryCapacity(t *testing.T) {
	dir := t.TempDir()
	zoneinfo := `Node 0, zone DMA
        spanned  524288
        present  393216
        managed  390000
Node 0, zone Normal
        spanned  33554432
        present  33161216
        managed  32000000
Node 0, zone Device
        spanned  524288
        present  524288
        managed  0
`
	path := filepath.Join(dir, "zoneinfo")
	if err := os.WriteFile(path, []byte(zoneinfo), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := zoneinfoMemoryCapacity(path, 4096); got != 128*1024*1024*1024 {
		t.Fatalf("zoneinfoMemoryCapacity() = %d", got)
	}
}

func TestHardware_LinuxKFDArchitecture(t *testing.T) {
	root := t.TempDir()
	nodesRoot := filepath.Join(root, "kfd", "nodes")
	drmRoot := filepath.Join(root, "drm")
	deviceRoot := filepath.Join(root, "dev", "dri")
	pciDevice := filepath.Join(root, "pci", "0000:65:00.0")
	for _, path := range []string{
		filepath.Join(nodesRoot, "1"),
		filepath.Join(drmRoot, "renderD128"),
		deviceRoot,
		pciDevice,
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	properties := "gfx_target_version 110501\ndrm_render_minor 128\nvendor_id 4098\n"
	if err := os.WriteFile(filepath.Join(nodesRoot, "1", "properties"), []byte(properties), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pciDevice, "vendor"), []byte("0x1002\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deviceRoot, "renderD128"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(pciDevice, filepath.Join(drmRoot, "renderD128", "device")); err != nil {
		t.Fatal(err)
	}

	got, err := detectAMDKFD(nodesRoot, drmRoot, deviceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].identity != "0000:65:00.0" || stringValue(got[0].value.Architecture) != "gfx1151" {
		t.Fatalf("detectAMDKFD() = %+v", got)
	}
}

func TestHardware_LinuxGFXTargetFormatting(t *testing.T) {
	tests := map[uint64]string{
		0:      "",
		90010:  "gfx90a",
		110000: "gfx1100",
		110501: "gfx1151",
	}
	for version, want := range tests {
		if got := formatGFXTarget(version); got != want {
			t.Errorf("formatGFXTarget(%d) = %q, want %q", version, got, want)
		}
	}
}

func TestHardware_LinuxPackageSocketCount(t *testing.T) {
	newCPU := func(root, id, packageID string) {
		topology := filepath.Join(root, id, "topology")
		if err := os.MkdirAll(topology, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(topology, "physical_package_id"), []byte(packageID+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("two packages", func(t *testing.T) {
		dir := t.TempDir()
		newCPU(dir, "cpu0", "0")
		newCPU(dir, "cpu1", "0")
		newCPU(dir, "cpu2", "1")
		if got := packageSocketCount(dir); got != 2 {
			t.Fatalf("packageSocketCount() = %d, want 2", got)
		}
	})
	t.Run("single package", func(t *testing.T) {
		dir := t.TempDir()
		newCPU(dir, "cpu0", "36")
		newCPU(dir, "cpu1", "36")
		if got := packageSocketCount(dir); got != 1 {
			t.Fatalf("packageSocketCount() = %d, want 1", got)
		}
	})
	t.Run("no topology", func(t *testing.T) {
		if got := packageSocketCount(t.TempDir()); got != 0 {
			t.Fatalf("packageSocketCount() = %d, want 0", got)
		}
	})
}

func TestHardware_LinuxSystem(t *testing.T) {
	root := t.TempDir()
	for name, value := range map[string]string{
		"sys_vendor":      "LENOVO\n",
		"product_version": "ThinkStation PGX\n",
		"product_name":    "30KL0004FC\n",
		"product_family":  "DGX Spark\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	system := detectSystem(root)
	if stringValue(system.Vendor) != "LENOVO" || stringValue(system.Model) != "ThinkStation PGX" || stringValue(system.Family) != "DGX Spark" {
		t.Fatalf("detectSystem() = %+v", system)
	}
}

func TestHardware_LinuxSystemPlaceholders(t *testing.T) {
	root := t.TempDir()
	for name, value := range map[string]string{
		"sys_vendor":      "To be filled by O.E.M.\n",
		"product_version": "System Product Name\n",
		"product_name":    "System Product Name\n",
		"product_family":  "Not Defined\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	system := detectSystem(root)
	if system.Vendor != nil || system.Model != nil || system.Family != nil {
		t.Fatalf("detectSystem() = %+v, want all fields nil", system)
	}
}

func TestHardware_LinuxSystemRevisionModel(t *testing.T) {
	tests := []struct {
		version string
		name    string
		want    string
	}{
		{version: "1.0\n", name: "Precision 5860 Tower", want: "Precision 5860 Tower"},
		{version: "0001\n", name: "OptiPlex 7010", want: "OptiPlex 7010"},
		{version: "Not Specified\n", name: "System X", want: "System X"},
		{version: "ThinkStation PGX\n", name: "30KL0004FC", want: "ThinkStation PGX"},
	}
	for _, test := range tests {
		t.Run(test.version, func(t *testing.T) {
			root := t.TempDir()
			for name, value := range map[string]string{"sys_vendor": "OEM\n", "product_version": test.version, "product_name": test.name + "\n"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			system := detectSystem(root)
			if stringValue(system.Model) != test.want {
				t.Fatalf("detectSystem().Model = %q, want %q", stringValue(system.Model), test.want)
			}
		})
	}
}

func TestHardware_LinuxSystemModelFallback(t *testing.T) {
	root := t.TempDir()
	for name, value := range map[string]string{
		"sys_vendor":      "NVIDIA\n",
		"product_version": "To be filled by O.E.M.\n",
		"product_name":    "DGX Spark\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	system := detectSystem(root)
	if stringValue(system.Vendor) != "NVIDIA" || stringValue(system.Model) != "DGX Spark" || system.Family != nil {
		t.Fatalf("detectSystem() = %+v, want model to fall back to product_name", system)
	}
}

func TestHardware_ParseROCmCSV(t *testing.T) {
	output := "device,Device Name,GUID,VRAM Total Memory (B),Card Series,GFX Version,Driver version,PCI Bus,Max Graphics Package Power (W)\n" +
		"card0,AMD Radeon,abc,25769803776,Radeon RX 7900 XTX,gfx1100,6.12.12,0000:03:00.0,339\n"
	got, err := parseROCmCSV(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].identity != "0000:03:00.0" || stringValue(got[0].value.Model) != "Radeon RX 7900 XTX" {
		t.Fatalf("parseROCmCSV() = %+v", got)
	}
}

func TestHardware_ParseROCmCSVMergesSeparateQueries(t *testing.T) {
	info := "device,Device Name,GUID,PCI Bus,VRAM Total Memory (B),Card Series,GFX Version\n" +
		"card0,AMD Instinct MI60 / MI50,57756,0000:83:00.0,34342961152,AMD Instinct MI60 / MI50,gfx906\n" +
		"card1,AMD Instinct MI60 / MI50,45663,0000:86:00.0,34342961152,AMD Instinct MI60 / MI50,gfx906\n"
	power := "device,Max Graphics Package Power (W)\ncard0,100.0\ncard1,150.0\n"
	got, err := parseROCmCSV(info, power)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("parseROCmCSV() returned %d accelerators, want 2", len(got))
	}
	if got[1].identity != "0000:86:00.0" || got[1].value.PowerLimitWatts == nil || *got[1].value.PowerLimitWatts != 150 {
		t.Fatalf("second accelerator = %+v", got[1])
	}
	if got[0].value.Memory.CapacityBytes == nil || *got[0].value.Memory.CapacityBytes != 34342961152 {
		t.Fatalf("first accelerator memory = %+v", got[0].value.Memory)
	}
}

func TestHardware_ParseROCmDriverVersion(t *testing.T) {
	for _, output := range []string{
		"name, value\n\"Driver version\", \"6.19.12-1-cachyos\"\n",
		"device,Driver version\nsystem,6.19.12-1-cachyos\n",
	} {
		if got := parseROCmDriverVersion(output); got != "6.19.12-1-cachyos" {
			t.Fatalf("parseROCmDriverVersion(%q) = %q", output, got)
		}
	}
}

func TestHardware_EnrichAMDFromSysfs(t *testing.T) {
	root := t.TempDir()
	hwmon := filepath.Join(root, "hwmon", "hwmon3")
	if err := os.MkdirAll(hwmon, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, value := range map[string]string{
		filepath.Join(root, "product_name"):        "Radeon Instinct MI50 32GB\n",
		filepath.Join(root, "mem_info_vram_total"): "34342961152\n",
		filepath.Join(hwmon, "power1_cap"):         "100000000\n",
	} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	accelerator := Accelerator{Memory: AcceleratorMemory{Kind: "unknown"}}
	enrichAMDFromSysfs(&accelerator, root)
	if stringValue(accelerator.Model) != "Radeon Instinct MI50 32GB" ||
		accelerator.Memory.Kind != "dedicated" || *accelerator.Memory.CapacityBytes != 34342961152 ||
		accelerator.PowerLimitWatts == nil || *accelerator.PowerLimitWatts != 100 {
		t.Fatalf("enrichAMDFromSysfs() = %+v", accelerator)
	}
}
