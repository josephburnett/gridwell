//go:build !unix

package files

import "errors"

// mountTop has no answer here: the trash a path on another volume would take
// is not one this OS keeps.
func mountTop(string) (string, error) {
	return "", errors.New("this OS keeps no trash on another volume")
}
