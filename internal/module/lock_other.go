//go:build !unix

package module

// Lock is a no-op where flock is not available.
func Lock(dir string) (unlock func(), err error) {
	if _, err := Find(dir); err != nil {
		return nil, err
	}
	return func() {}, nil
}
