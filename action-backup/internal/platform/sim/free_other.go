//go:build !darwin && !linux

package sim

func freeBytes(string) (uint64, error) { return 0, nil }
