//go:build linux

package sysmon

import (
	"net/netip"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func newCollector() (collector, error) {
	c := newProcCollector("/")
	c.selfCPU = rusageCPU
	var u unix.Utsname
	if unix.Uname(&u) == nil {
		c.bits = kernelCounterBits(unix.ByteSliceToString(u.Machine[:]))
	}
	return c, nil
}

// rusageCPU returns this process's user plus system CPU time with
// microsecond resolution.
func rusageCPU() (uint64, bool) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, false
	}
	ns := ru.Utime.Nano() + ru.Stime.Nano()
	if ns < 0 {
		return 0, false
	}
	return uint64(ns), true
}

// osVersion returns the distribution name from os-release plus the kernel
// release, for example "Ubuntu 24.04.1 LTS (kernel 6.8.0-45-generic)".
func osVersion() string {
	name := ""
	for _, p := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		if b, err := os.ReadFile(p); err == nil {
			name = osReleaseName(b)
			break
		}
	}
	var u unix.Utsname
	kernel := ""
	if unix.Uname(&u) == nil {
		kernel = unix.ByteSliceToString(u.Release[:])
	}
	switch {
	case name != "" && kernel != "":
		return name + " (kernel " + kernel + ")"
	case name != "":
		return name
	case kernel != "":
		return "Linux " + kernel
	}
	return "Linux"
}

// routeInterface finds the interface owning the source address the kernel
// chose for the destination.
func routeInterface(ip, local netip.Addr) (idx int, name string, speed uint64, loopback bool, err error) {
	idx, name, loopback, err = routeBySource(local)
	if err != nil {
		return 0, "", 0, false, err
	}
	if !loopback {
		speed = sysfsLinkSpeed("/", name)
	}
	return idx, name, speed, loopback, nil
}
