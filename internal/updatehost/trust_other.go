//go:build !darwin

package updatehost

// Other platforms retain their existing ownership and mode trust contract.
func platformDescriptorTrustFD(int) error { return nil }
