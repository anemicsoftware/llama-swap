//go:build linux

package hw

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakePowerCapEnv points the package at a temporary sysfs tree and a stub
// command runner. Setting a limit through rocm-smi writes cap to power1_cap
// unless applyWatts is negative (simulating a silent failure).
func fakePowerCapEnv(t *testing.T, applyWatts int) (calls *[][]string) {
	t.Helper()
	root := t.TempDir()
	hwmon := filepath.Join(root, "0000:83:00.0", "hwmon", "hwmon1")
	if err := os.MkdirAll(hwmon, 0o755); err != nil {
		t.Fatal(err)
	}
	capPath := filepath.Join(hwmon, "power1_cap")
	if err := os.WriteFile(capPath, []byte("225000000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldRoot, oldRunner, oldLookPath := pciDevicesRoot, commandRunner, lookPath
	t.Cleanup(func() { pciDevicesRoot, commandRunner, lookPath = oldRoot, oldRunner, oldLookPath })
	pciDevicesRoot = root
	lookPath = func(name string) (string, error) { return "/opt/rocm/bin/" + name, nil }

	var recorded [][]string
	commandRunner = func(_ context.Context, name string, args ...string) ([]byte, error) {
		recorded = append(recorded, append([]string{name}, args...))
		if name == "rocm-smi" {
			return []byte("device,PCI Bus\ncard0,0000:c3:00.0\ncard2,0000:83:00.0\n"), nil
		}
		if applyWatts >= 0 {
			return nil, os.WriteFile(capPath, []byte(strconv.Itoa(applyWatts*1_000_000)+"\n"), 0o600)
		}
		return []byte("ok"), nil
	}
	return &recorded
}

func amdSnapshot() *HardwareSnapshot {
	watts, min, max := 225.0, 100.0, 225.0
	return &HardwareSnapshot{Accelerators: []Accelerator{{
		Kind:               "gpu",
		Vendor:             stringPtr("AMD"),
		PowerLimitWatts:    &watts,
		PowerLimitMinWatts: &min,
		PowerLimitMaxWatts: &max,
		pciAddress:         "0000:83:00.0",
	}}}
}

func TestHardware_SetPowerLimit(t *testing.T) {
	calls := fakePowerCapEnv(t, 150)
	snapshot := amdSnapshot()

	applied, err := snapshot.SetPowerLimit(context.Background(), 0, 150)
	if err != nil || applied != 150 {
		t.Fatalf("SetPowerLimit() = %v, %v", applied, err)
	}
	if got := *snapshot.Accelerators[0].PowerLimitWatts; got != 150 {
		t.Errorf("snapshot power limit = %v, want 150", got)
	}
	want := "sudo -n /opt/rocm/bin/rocm-smi -d 2 --setpoweroverdrive 150 --autorespond yes"
	if got := strings.Join((*calls)[len(*calls)-1], " "); got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
}

func TestHardware_SetPowerLimitRejectsOutOfRange(t *testing.T) {
	calls := fakePowerCapEnv(t, 150)
	snapshot := amdSnapshot()
	for _, watts := range []float64{0, -5, 99, 226} {
		if _, err := snapshot.SetPowerLimit(context.Background(), 0, watts); !errors.Is(err, ErrInvalidPowerLimit) {
			t.Errorf("SetPowerLimit(%v) error = %v, want ErrInvalidPowerLimit", watts, err)
		}
	}
	if _, err := snapshot.SetPowerLimit(context.Background(), 3, 150); !errors.Is(err, ErrInvalidPowerLimit) {
		t.Errorf("unknown index error = %v, want ErrInvalidPowerLimit", err)
	}
	if len(*calls) != 0 {
		t.Errorf("commands ran for rejected requests: %v", *calls)
	}
}

func TestHardware_SetPowerLimitDetectsUnappliedValue(t *testing.T) {
	fakePowerCapEnv(t, -1)
	snapshot := amdSnapshot()
	if _, err := snapshot.SetPowerLimit(context.Background(), 0, 150); err == nil || !strings.Contains(err.Error(), "did not apply") {
		t.Fatalf("SetPowerLimit() error = %v, want unapplied error", err)
	}
	if got := *snapshot.Accelerators[0].PowerLimitWatts; got != 225 {
		t.Errorf("snapshot power limit = %v, want unchanged 225", got)
	}
}

func TestHardware_SetPowerLimitUnsupported(t *testing.T) {
	snapshot := amdSnapshot()
	snapshot.Accelerators[0].Vendor = stringPtr("NVIDIA")
	if _, err := snapshot.SetPowerLimit(context.Background(), 0, 150); !errors.Is(err, ErrPowerLimitUnsupported) {
		t.Fatalf("SetPowerLimit() error = %v, want ErrPowerLimitUnsupported", err)
	}
}
