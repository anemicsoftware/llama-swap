package hw

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
)

// ErrPowerLimitUnsupported reports that an accelerator's power limit cannot
// be changed by llama-swap.
var ErrPowerLimitUnsupported = errors.New("power limit cannot be changed for this accelerator")

// ErrInvalidPowerLimit reports a requested limit outside the accepted range.
var ErrInvalidPowerLimit = errors.New("invalid power limit")

// snapshotMu guards the power limit fields of the process's snapshot, which
// SetPowerLimit updates after startup.
var snapshotMu sync.RWMutex

var pciAddressPattern = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-9a-f]$`)

func pciAddressOf(identity string) string {
	if pciAddressPattern.MatchString(identity) {
		return identity
	}
	return ""
}

// CanSetPowerLimit reports whether the power limit of the accelerator can be
// changed: an AMD GPU with a known PCI address and a known limit range.
func (a Accelerator) CanSetPowerLimit() bool {
	return a.Kind == "gpu" && stringValue(a.Vendor) == "AMD" && a.pciAddress != "" &&
		a.PowerLimitMaxWatts != nil
}

// Clone returns a copy of the snapshot that is safe to read while
// ApplyPowerLimit runs.
func (s *HardwareSnapshot) Clone() HardwareSnapshot {
	snapshotMu.RLock()
	defer snapshotMu.RUnlock()
	out := *s
	out.Accelerators = make([]Accelerator, len(s.Accelerators))
	copy(out.Accelerators, s.Accelerators)
	return out
}

// Accelerator returns a copy of the accelerator at index.
func (s *HardwareSnapshot) Accelerator(index int) (Accelerator, bool) {
	snapshotMu.RLock()
	defer snapshotMu.RUnlock()
	if index < 0 || index >= len(s.Accelerators) {
		return Accelerator{}, false
	}
	return s.Accelerators[index], true
}

// SetPowerLimit changes the enforced power limit of accelerator index and
// records the value the device reports afterwards in the snapshot. It returns
// the applied limit.
func (s *HardwareSnapshot) SetPowerLimit(ctx context.Context, index int, watts float64) (float64, error) {
	accelerator, ok := s.Accelerator(index)
	if !ok {
		return 0, fmt.Errorf("%w: accelerator %d not found", ErrInvalidPowerLimit, index)
	}
	if !accelerator.CanSetPowerLimit() {
		return 0, ErrPowerLimitUnsupported
	}
	if math.IsNaN(watts) || math.IsInf(watts, 0) || watts <= 0 {
		return 0, fmt.Errorf("%w: watts must be positive", ErrInvalidPowerLimit)
	}
	watts = math.Round(watts)
	if watts > *accelerator.PowerLimitMaxWatts ||
		(accelerator.PowerLimitMinWatts != nil && watts < *accelerator.PowerLimitMinWatts) {
		return 0, fmt.Errorf("%w: watts must be within %s", ErrInvalidPowerLimit, powerRangeLabel(accelerator))
	}

	applied, err := setAMDPowerLimit(ctx, accelerator.pciAddress, int(watts))
	if err != nil {
		return 0, err
	}
	snapshotMu.Lock()
	s.Accelerators[index].PowerLimitWatts = &applied
	snapshotMu.Unlock()
	return applied, nil
}

func powerRangeLabel(a Accelerator) string {
	low := "0"
	if a.PowerLimitMinWatts != nil {
		low = fmt.Sprintf("%g", *a.PowerLimitMinWatts)
	}
	return fmt.Sprintf("%s-%g W", low, *a.PowerLimitMaxWatts)
}

func trimOutput(output []byte) string {
	text := strings.TrimSpace(string(output))
	if len(text) > 300 {
		text = text[:300] + "..."
	}
	return text
}
