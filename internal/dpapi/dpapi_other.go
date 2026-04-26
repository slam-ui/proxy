//go:build !windows

package dpapi

func Encrypt(data []byte) ([]byte, error) { return data, nil }
func Decrypt(data []byte) ([]byte, error) { return data, nil }
