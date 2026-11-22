//go:build !linux && !windows

package sysmon

import (
	"net/netip"
	"runtime"
)

type stubCollector struct{}

func newCollector() (collector, error) {
	return stubCollector{}, nil
}

func (stubCollector) read(r *reading) {
	r.cores = float64(runtime.NumCPU())
	r.missing = missCPUMachine | missCPUProcess | missMemUsed | missMemTotal |
		missMemProcess | missNICs | missNICRates | missTCP | missTCPConns | missPorts
}

func (stubCollector) close() {}

func osVersion() string {
	return runtime.GOOS
}

func routeInterface(ip, local netip.Addr) (idx int, name string, speed uint64, loopback bool, err error) {
	idx, name, loopback, err = routeBySource(local)
	return idx, name, 0, loopback, err
}
