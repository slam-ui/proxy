//go:build !windows

package power

func Current() (Status, error) {
	return Status{}, nil
}
