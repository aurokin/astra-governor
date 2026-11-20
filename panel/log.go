package main

import (
	"os"
	"syscall"
)

// The Magisk boot hook has no reliable stderr consumer. Keep the current
// and previous service logs under the same root-only directory as its lock.
type boundedLog struct{ path string }

func (l boundedLog) Write(data []byte) (int, error) {
	info, err := os.Stat(l.path)
	if err == nil && info.Size()+int64(len(data)) > 64*1024 {
		if err = os.Rename(l.path, l.path+".1"); err != nil {
			return 0, err
		}
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return 0, err
	}
	n, err := f.Write(data)
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return n, err
}
