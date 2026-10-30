//go:build !(linux || darwin)

package stacks

func defaultWriteTrust() writeTrust { return writeTrust{} }

func untrustedWriters(string, writeTrust) []string { return nil }
