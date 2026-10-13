//go:build !unix && !windows

package doctor

import "errors"

func freeBytes(string) (uint64, error) { return 0, errors.New("unsupported") }
