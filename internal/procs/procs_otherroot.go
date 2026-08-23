//go:build !linux

package procs

// swapProcRoot is a no-op off Linux: other samplers shell out to OS tools.
func swapProcRoot(string) func() { return func() {} }
