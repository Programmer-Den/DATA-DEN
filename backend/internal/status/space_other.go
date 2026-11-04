//go:build !unix

package status

import "errors"

func space(string) (total, free int64, err error) {
	return 0, 0, errors.New("free space is only read on Linux")
}
