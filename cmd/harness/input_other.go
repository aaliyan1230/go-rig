//go:build !darwin && !linux

package main

import (
	"io"
	"os"
)

func newTerminalReader(f *os.File) (io.ReadCloser, error) {
	return struct {
		io.Reader
		io.Closer
	}{Reader: f, Closer: noClose{}}, nil
}

type noClose struct{}

func (noClose) Close() error { return nil }
