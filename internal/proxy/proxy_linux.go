//go:build linux

package proxy

import (
	"io"
	"net"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// spliceRaw calls splice(2) through Syscall6 so the implementation is
// identical on 32-bit and 64-bit Linux, where x/sys/unix.Splice differs in its
// return type (int vs int64).
func spliceRaw(rfd int, roff *int64, wfd int, woff *int64, length int, flags int) (int64, error) {
	r0, _, errno := unix.Syscall6(
		unix.SYS_SPLICE,
		uintptr(rfd), uintptr(unsafe.Pointer(roff)),
		uintptr(wfd), uintptr(unsafe.Pointer(woff)),
		uintptr(length), uintptr(flags),
	)
	if errno != 0 {
		return 0, errno
	}
	return int64(r0), nil
}

const (
	spliceChunkSize = 16 * 1024
	// splicePipeSize shrinks the kernel pipe buffer from the default 64KB to
	// 16KB per direction, cutting kernel slab memory per session.
	splicePipeSize = 16 * 1024
)

// spliceMetered moves bytes from src to dst through a kernel pipe using
// splice(2). onBytes is called for each chunk written to dst, so live metering
// remains accurate without a userspace copy.
//
// handled=false means the caller should fall back to a userspace copy. That is
// only returned before any byte has been transferred; a mid-stream error is
// returned with handled=true to avoid duplicating data.
func spliceMetered(
	dst *net.TCPConn,
	src *net.TCPConn,
	onBytes func(uint64),
	idleTimeout time.Duration,
) (handled bool, total int64, err error) {
	var pipeFDs [2]int
	if err := unix.Pipe2(pipeFDs[:], unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		spliceFallbacks.Add(1)
		return false, 0, nil
	}
	pipeR, pipeW := pipeFDs[0], pipeFDs[1]
	defer func() { _ = unix.Close(pipeR) }()
	defer func() { _ = unix.Close(pipeW) }()

	// Best effort: a constrained board may reject the shrink or round it up.
	_, _ = unix.FcntlInt(uintptr(pipeW), unix.F_SETPIPE_SZ, splicePipeSize)

	srcRaw, err := src.SyscallConn()
	if err != nil {
		spliceFallbacks.Add(1)
		return false, 0, nil
	}
	dstRaw, err := dst.SyscallConn()
	if err != nil {
		spliceFallbacks.Add(1)
		return false, 0, nil
	}

	countDirection := false
	defer func() {
		if countDirection {
			spliceDirections.Add(1)
		}
	}()

	countDirection = true
	engaged := false
	for {
		if idleTimeout > 0 {
			_ = src.SetReadDeadline(time.Now().Add(idleTimeout))
		}

		var n int64
		var spliceErr error
		readErr := srcRaw.Read(func(fd uintptr) bool {
			n, spliceErr = spliceRaw(
				int(fd), nil,
				pipeW, nil,
				spliceChunkSize,
				unix.SPLICE_F_MOVE|unix.SPLICE_F_NONBLOCK,
			)
			if spliceErr == unix.EAGAIN {
				return false
			}
			return true
		})
		if readErr != nil {
			_ = src.SetReadDeadline(time.Time{})
			return true, total, readErr
		}
		if spliceErr != nil {
			if !engaged && (spliceErr == unix.EINVAL || spliceErr == unix.ENOSYS) {
				_ = src.SetReadDeadline(time.Time{})
				countDirection = false
				spliceFallbacks.Add(1)
				return false, 0, nil
			}
			return true, total, spliceErr
		}
		if n == 0 {
			// EOF.
			return true, total, nil
		}

		engaged = true

		remaining := n
		for remaining > 0 {
			var m int64
			var writeErr error
			writeCallErr := dstRaw.Write(func(fd uintptr) bool {
				m, writeErr = spliceRaw(
					pipeR, nil,
					int(fd), nil,
					int(remaining),
					unix.SPLICE_F_MOVE|unix.SPLICE_F_NONBLOCK,
				)
				if writeErr == unix.EAGAIN {
					return false
				}
				return true
			})
			if writeCallErr != nil {
				return true, total, writeCallErr
			}
			if writeErr != nil {
				return true, total, writeErr
			}
			if m == 0 {
				return true, total, io.ErrUnexpectedEOF
			}

			remaining -= m
			total += int64(m)
			if onBytes != nil {
				onBytes(uint64(m))
			}
		}
	}
}
