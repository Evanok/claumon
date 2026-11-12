//go:build windows

package main

// lockAgent is a no-op on Windows, where the agent starts once per logon
// from the Startup folder.
func lockAgent(string) (unlock func(), err error) {
	return func() {}, nil
}
