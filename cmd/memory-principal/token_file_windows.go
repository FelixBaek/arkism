package main

import (
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows ignores Unix 0600 semantics. Install the current user's protected
// DACL at creation, before writing any secret; never inherit a public folder ACL.
func openTokenFile(path string) (*os.File, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	attributes := windows.SecurityAttributes{SecurityDescriptor: descriptor}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	handle, err := windows.CreateFile(name, windows.GENERIC_WRITE, 0, &attributes, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	runtime.KeepAlive(descriptor)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}
