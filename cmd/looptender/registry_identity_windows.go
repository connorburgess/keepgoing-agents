package main

import (
	"fmt"
	"os"
	"syscall"
)

func registryNativeIdentity(path string, info os.FileInfo) (string, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", errRegistryBinding
	}
	h, err := syscall.CreateFile(name, 0x0080 /* FILE_READ_ATTRIBUTES */, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return "", errRegistryBinding
	}
	f := os.NewFile(uintptr(h), path)
	defer f.Close()
	current, err := f.Stat()
	if err != nil || !os.SameFile(info, current) {
		return "", errRegistryBinding
	}
	var id syscall.ByHandleFileInformation
	if syscall.GetFileInformationByHandle(h, &id) != nil {
		return "", errRegistryBinding
	}
	return fmt.Sprintf("%d:%d:%d", id.VolumeSerialNumber, id.FileIndexHigh, id.FileIndexLow), nil
}
