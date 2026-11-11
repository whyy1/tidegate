package main

import "runtime"

func numCPU() int { return runtime.NumCPU() }

func goVersion() string { return runtime.Version() }
