package fetcher

// newTestNative builds a Native for tests, whose origins are httptest
// servers on loopback. Production blocks loopback and private addresses;
// tests opt in explicitly here, and the guard has tests of its own.
func newTestNative(opts NativeOptions) *Native {
	opts.AllowPrivateHosts = true
	return NewNative(opts)
}
