//go:build windows

// wifidirect_probe diagnoses why WinRT class activation fails. Reports OS
// build, CoInitializeEx result, RoInitialize result, RoGetActivationFactory
// results for a plain WinRT class and for WiFiDirectDevice, and whether
// winrt.dll is present.
package main

import (
	"fmt"
	"syscall"
	"unsafe"

	"github.com/go-ole/go-ole"
)

const (
	iidDeviceInformationStatics = "{EEE7E345-1A27-42FB-B79E-8C9CEBCE6DE9}"
	iidWiFiDirectDeviceStatics  = "{72DEB82E-8FA4-4B04-A2BA-AB8E2538A24B}"
	errorInsufficientBuffer     = 122
	appmodelErrorNoPackage      = 15700
)

func packageIdentity() string {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetCurrentPackageFullName")
	var idLen uint32
	r, _, _ := proc.Call(uintptr(unsafe.Pointer(&idLen)), 0)
	if uint32(r) == appmodelErrorNoPackage {
		return "NONE (APPMODEL_ERROR_NO_PACKAGE)"
	}
	if uint32(r) != errorInsufficientBuffer {
		return fmt.Sprintf("unknown (HRESULT %d)", uint32(r))
	}
	buf := make([]uint16, idLen)
	r, _, _ = proc.Call(uintptr(unsafe.Pointer(&idLen)), uintptr(unsafe.Pointer(&buf[0])))
	if uint32(r) != 0 {
		return fmt.Sprintf("unknown (second call HRESULT %d)", uint32(r))
	}
	return "PRESENT: " + syscall.UTF16ToString(buf)
}

func osBuild() {
	mod := syscall.NewLazyDLL("ntdll.dll").NewProc("RtlGetVersion")
	var info [3]uint32 // MajorVersion, MinorVersion, BuildNumber
	r, _, _ := mod.Call(uintptr(unsafe.Pointer(&info[0])))
	fmt.Printf("OS: Major=%d Minor=%d Build=%d (RtlGetVersion=%d)\n",
		info[0], info[1], info[2], r)
}

func tryActivate(class, iidStr string) {
	obj, err := ole.RoGetActivationFactory(class, ole.NewGUID(iidStr))
	if err != nil {
		fmt.Printf("  %-50s FAIL: %v\n", class, err)
		return
	}
	fmt.Printf("  %-50s OK\n", class)
	vt := (*ole.IUnknownVtbl)(unsafe.Pointer(obj.RawVTable))
	syscall.SyscallN(vt.Release, uintptr(unsafe.Pointer(obj)))
}

func main() {
	osBuild()
	// Initialize COM in Multi-Threaded Apartment.
	coErr := ole.CoInitializeEx(0, 0)
	fmt.Printf("CoInitializeEx(MTA): %v\n", coErr)
	// Initialize the WinRT base.
	roErr := ole.RoInitialize(1)
	fmt.Printf("RoInitialize(1): %v\n", roErr)
	fmt.Println("Package identity:", packageIdentity())

	winrt, werr := syscall.LoadLibrary("winrt.dll")
	if werr != nil {
		fmt.Println("winrt.dll:", "NOT FOUND", werr)
	} else {
		fmt.Println("winrt.dll: loaded")
		syscall.FreeLibrary(winrt)
	}

	fmt.Println("WinRT activation:")
	tryActivate("Windows.Devices.Enumeration.DeviceInformation", iidDeviceInformationStatics)
	tryActivate("Windows.Devices.WiFiDirect.WiFiDirectDevice", iidWiFiDirectDeviceStatics)
}
