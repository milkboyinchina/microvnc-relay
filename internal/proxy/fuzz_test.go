package proxy

import "testing"

// FuzzParseMode2ID exercises the UltraVNC ID handshake parser with arbitrary
// bytes. It must reject invalid payloads without panicking.
func FuzzParseMode2ID(f *testing.F) {
	f.Add([]byte("ID:1234"))
	f.Add([]byte("ID:1234\n"))
	f.Add([]byte("1234\x00\x00"))
	f.Add([]byte(""))
	f.Add([]byte("ID:"))
	f.Add([]byte("ID:999999999999999999999999999"))

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = parseMode2ID(data)
	})
}
