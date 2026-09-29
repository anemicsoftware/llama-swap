//go:build !linux

package hw

import "context"

func setAMDPowerLimit(context.Context, string, int) (float64, error) {
	return 0, ErrPowerLimitUnsupported
}
