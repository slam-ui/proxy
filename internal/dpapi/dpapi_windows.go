//go:build windows

package dpapi

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	crypt32           = windows.NewLazySystemDLL("crypt32.dll")
	procProtectData   = crypt32.NewProc("CryptProtectData")
	procUnprotectData = crypt32.NewProc("CryptUnprotectData")
)

type dataBlob struct {
	cbData uint32
	pbData *byte
}

func newBlob(d []byte) (*dataBlob, error) {
	if len(d) == 0 {
		return &dataBlob{}, nil
	}
	if uint64(len(d)) > uint64(^uint32(0)) {
		return nil, fmt.Errorf("DPAPI input too large: %d bytes", len(d))
	}
	return &dataBlob{cbData: uint32(len(d)), pbData: &d[0]}, nil // #nosec G115 -- bounded by the uint64 check above.
}

func freeBlob(blob *dataBlob, zero bool) {
	if blob == nil || blob.pbData == nil {
		return
	}
	if zero && blob.cbData > 0 {
		plain := unsafe.Slice(blob.pbData, blob.cbData) // #nosec G103 -- DPAPI returned this native buffer; clear it before LocalFree.
		clear(plain)
	}
	_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(blob.pbData))) // #nosec G103 -- pointer is the DPAPI LocalAlloc buffer.
}

func Encrypt(data []byte) ([]byte, error) {
	in, err := newBlob(data)
	if err != nil {
		return nil, err
	}
	var out dataBlob
	r, _, err := procProtectData.Call(
		uintptr(unsafe.Pointer(in)),
		0,
		0,
		0,
		0,
		0,
		uintptr(unsafe.Pointer(&out)),
	)
	runtime.KeepAlive(data)
	runtime.KeepAlive(in)
	if r == 0 {
		return nil, fmt.Errorf("CryptProtectData: %w", err)
	}
	defer freeBlob(&out, false)
	result := make([]byte, out.cbData)
	copy(result, unsafe.Slice(out.pbData, out.cbData))
	return result, nil
}

func Decrypt(data []byte) ([]byte, error) {
	in, err := newBlob(data)
	if err != nil {
		return nil, err
	}
	var out dataBlob
	r, _, err := procUnprotectData.Call(
		uintptr(unsafe.Pointer(in)),
		0,
		0,
		0,
		0,
		0,
		uintptr(unsafe.Pointer(&out)),
	)
	runtime.KeepAlive(data)
	runtime.KeepAlive(in)
	if r == 0 {
		return nil, fmt.Errorf("CryptUnprotectData: %w", err)
	}
	defer freeBlob(&out, true)
	result := make([]byte, out.cbData)
	copy(result, unsafe.Slice(out.pbData, out.cbData))
	return result, nil
}
