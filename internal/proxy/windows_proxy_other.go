//go:build !windows

package proxy

func setSystemProxy(string, string) error { return nil }
func disableSystemProxy() error           { return nil }
func getSystemProxyState() (bool, string, string) {
	return false, "", ""
}
