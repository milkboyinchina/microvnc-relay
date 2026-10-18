//go:build !dev

package proxy

// Fault injection is compiled out of production builds.
func SetFaultRST(bool)  {}
func SetFaultDrop(bool) {}

func faultActive() (rst, drop bool) {
	return false, false
}
