//go:build darwin || linux

package main

import (
	"io"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// Polling makes a pending terminal read interruptible without closing the
// process's stdin or leaving a reader that could steal the next prompt.
type terminalReader struct {
	fd   int
	stop chan struct{}
	once sync.Once
}

func newTerminalReader(f *os.File) (io.ReadCloser, error) {
	return &terminalReader{fd: int(f.Fd()), stop: make(chan struct{})}, nil
}
func (r *terminalReader) Close() error { r.once.Do(func() { close(r.stop) }); return nil }
func (r *terminalReader) Read(p []byte) (int, error) {
	for {
		select {
		case <-r.stop:
			return 0, io.EOF
		default:
		}
		fds := []unix.PollFd{{Fd: int32(r.fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 50)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n == 0 {
			continue
		}
		select {
		case <-r.stop:
			return 0, io.EOF
		default:
		}
		n, err = unix.Read(r.fd, p)
		if err == unix.EINTR {
			continue
		}
		if n == 0 && err == nil {
			return 0, io.EOF
		}
		return n, err
	}
}
