//go:build !linux

package vm

import "errors"

func memfdCreate(name string) (int, error) {
	return 0, errors.New("memfd not supported on this platform")
}
