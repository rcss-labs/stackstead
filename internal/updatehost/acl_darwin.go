package updatehost

import (
	"encoding/binary"
	"os"
	"syscall"
	"unsafe"

	"github.com/rcss-labs/stackstead/internal/update"
	"golang.org/x/sys/unix"
)

// fgetattrlist and kauth_filesec layouts come from the Darwin sys/attr.h and
// sys/kauth.h ABI. Both supported Darwin architectures are little-endian.
// Reject all nonempty ACLs rather than infer the permissions of arbitrary GUIDs.
func darwinACLProof(f *os.File) error { return platformDescriptorTrustFD(int(f.Fd())) }

type darwinAttributes struct {
	Count                                 uint16
	Reserved                              uint16
	Common, Volume, Directory, File, Fork uint32
}

func descriptorAttributes(fd int, attributes darwinAttributes) ([]byte, error) {
	var buffer [4096]byte
	_, _, errno := syscall.Syscall6(unix.SYS_FGETATTRLIST, uintptr(fd), uintptr(unsafe.Pointer(&attributes)), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0, 0)
	if errno != 0 {
		return nil, update.ErrSetupRequired
	}
	return buffer[:], nil
}
func platformDescriptorTrustFD(fd int) error {
	// Prove ACL support on the same descriptor's volume before accepting absence.
	volume, e := descriptorAttributes(fd, darwinAttributes{Count: 5, Common: unix.ATTR_CMN_RETURNED_ATTRS, Volume: unix.ATTR_VOL_INFO | unix.ATTR_VOL_CAPABILITIES})
	if e != nil {
		return e
	}
	if e = validateDarwinVolumeACL(volume); e != nil {
		return e
	}
	// Do not ask for RETURNED_ATTRS here: XNU rejects unsupported requested vnode
	// attributes instead of silently omitting them. A null ACL packs a zero-length
	// attrreference (offset 8), distinct from a populated kauth_filesec.
	acl, e := descriptorAttributes(fd, darwinAttributes{Count: 5, Common: unix.ATTR_CMN_EXTENDED_SECURITY})
	if e != nil {
		return e
	}
	return validateDarwinACL(acl)
}
func validateDarwinVolumeACL(buffer []byte) error {
	if len(buffer) < 56 || binary.LittleEndian.Uint32(buffer[:4]) != 56 || binary.LittleEndian.Uint32(buffer[4:8]) != unix.ATTR_CMN_RETURNED_ATTRS || binary.LittleEndian.Uint32(buffer[8:12]) != unix.ATTR_VOL_CAPABILITIES {
		return update.ErrSetupRequired
	}
	for _, b := range buffer[12:24] {
		if b != 0 {
			return update.ErrSetupRequired
		}
	}
	// vol_capabilities_attr: capabilities[4], valid[4], interface index 1.
	const extendedSecurity uint32 = 0x400
	if binary.LittleEndian.Uint32(buffer[28:32])&extendedSecurity == 0 || binary.LittleEndian.Uint32(buffer[44:48])&extendedSecurity == 0 {
		return update.ErrSetupRequired
	}
	return nil
}
func validateDarwinACL(buffer []byte) error {
	// A single attrreference follows the four-byte returned length.
	if len(buffer) < 12 {
		return update.ErrSetupRequired
	}
	size := uint64(binary.LittleEndian.Uint32(buffer[:4]))
	if size < 12 || size > uint64(len(buffer)) {
		return update.ErrSetupRequired
	}
	buffer = buffer[:size]
	offset := int64(int32(binary.LittleEndian.Uint32(buffer[4:8])))
	length := uint64(binary.LittleEndian.Uint32(buffer[8:12]))
	if length == 0 {
		if offset != 8 || size != 12 {
			return update.ErrSetupRequired
		}
		return nil
	}
	start := int64(4) + offset
	if offset < 8 || offset%4 != 0 || start < 12 || uint64(start) > size || length > size-uint64(start) || uint64(start)+length != size || length < 44 {
		return update.ErrSetupRequired
	}
	security := buffer[start : uint64(start)+length]
	if binary.LittleEndian.Uint32(security[:4]) != 0x012cc16d {
		return update.ErrSetupRequired
	}
	entries := binary.LittleEndian.Uint32(security[36:40])
	// Empty ACLs and the explicit kernel NOACL marker contain no grants. Any
	// extension, ACE, or unknown flags is unsupported and fails closed.
	if entries != 0 && entries != 0xffffffff || length != 44 || binary.LittleEndian.Uint32(security[40:44]) != 0 {
		return update.ErrSetupRequired
	}
	return nil
}
