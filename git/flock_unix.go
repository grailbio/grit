// Copyright 2018 GRAIL, Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package git

import (
	"syscall"

	"github.com/grailbio/base/log"
)

// Flock implements a simple POSIX file-based advisory lock.
type flock struct {
	name string
	fd   int
}

func newFlock(name string) *flock {
	return &flock{name: name}
}

func (f *flock) Lock() error {
	var err error
	f.fd, err = syscall.Open(f.name, syscall.O_CREAT|syscall.O_RDWR, 0777)
	if err != nil {
		return err
	}
	err = syscall.Flock(f.fd, syscall.LOCK_EX|syscall.LOCK_NB)
	for err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
		log.Printf("waiting for lock %s", f.name)
		err = syscall.Flock(f.fd, syscall.LOCK_EX)
	}
	return err
}

func (f *flock) Unlock() error {
	err := syscall.Flock(f.fd, syscall.LOCK_UN)
	if err := syscall.Close(f.fd); err != nil {
		log.Error.Printf("close %s: %v", f.name, err)
	}
	return err
}
