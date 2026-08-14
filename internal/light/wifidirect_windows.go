//go:build windows

// Windows Wi-Fi Direct backend.
//
// This file implements WifiDirectManager using the WinRT
// Windows.Devices.WiFiDirect.WiFiDirectDevice and
// Windows.Devices.Enumeration.DeviceInformation APIs, driven from Go through
// github.com/go-ole/go-ole (a pre-existing dependency). We call the WinRT COM
// vtable directly because go-ole does not ship generated bindings for these
// types; the portable transfer stack (HTTP/TCP, optional QUIC) then rides on top
// of the P2P link unchanged.
//
// IMPORTANT: a packaged/desktop Windows app that uses Wi-Fi Direct must declare
// the "Proximity" device capability in its manifest (Package.appxmanifest),
// otherwise DeviceInformation.FindAllAsync and WiFiDirectDevice.FromIdAsync are
// denied and every call below degrades to ErrWifiDirectUnsupported.
//
// The WinRT contract IIDs below are the published interface identifiers for the
// target Windows SDK. They are treated as data so the binding stays easy to
// verify/adjust; any failure anywhere returns ErrWifiDirectUnsupported so callers
// fall back to ordinary LAN discovery.
package light

import (
	"context"
	"net"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
)

// wifidirectTransferPort is the port the existing transfer stack listens on once
// the P2P link is up.
const wifidirectTransferPort = "9120"

// roInitMultiThreaded mirrors RO_INIT_MULTITHREADED (1) for ole.RoInitialize.
// Wi-Fi Direct operations are asynchronous, so we run in the MTA.
const roInitMultiThreaded = 1

// WinRT contract interface IIDs (see header note).
var (
	iidIDeviceInformationStatics = ole.NewGUID("{EEE7E345-1A27-42FB-B79E-8C9CEBCE6DE9}")
	iidIWiFiDirectDeviceStatics  = ole.NewGUID("{72DEB82E-8FA4-4B04-A2BA-AB8E2538A24B}")
	// IAsyncInfo is implemented by every IAsyncOperation<T>; we use it only to
	// poll completion status without building a completion-handler COM object.
	iidIAsyncInfo = ole.NewGUID("{0000002D-0000-0000-C000-000000000046}")
)

// windowsWifiDirectManager is the Windows implementation of WifiDirectManager.
type windowsWifiDirectManager struct {
	mu     sync.Mutex
	device *ole.IInspectable // WiFiDirectDevice held until Close() releases it.
}

// newPlatformWifiDirectManager returns the Windows Wi-Fi Direct manager. The
// WinRT apartment must be initialized first; failure (e.g. wrong apartment
// mode) means the platform cannot host a link, so we report unsupported.
func newPlatformWifiDirectManager() (WifiDirectManager, error) {
	if err := ole.RoInitialize(roInitMultiThreaded); err != nil {
		return nil, ErrWifiDirectUnsupported
	}
	return &windowsWifiDirectManager{}, nil
}

// Discover enumerates nearby Wi-Fi Direct devices. We obtain the Wi-Fi Direct
// AQS selector from WiFiDirectDevice, then ask DeviceInformation to find all
// devices matching it.
func (m *windowsWifiDirectManager) Discover(ctx context.Context) ([]WifiDirectPeer, error) {
	selector, err := wfdDeviceSelector()
	if err != nil {
		return nil, ErrWifiDirectUnsupported
	}

	di, err := ole.RoGetActivationFactory("Windows.Devices.Enumeration.DeviceInformation", iidIDeviceInformationStatics)
	if err != nil {
		return nil, ErrWifiDirectUnsupported
	}
	diVT := (*iDeviceInformationStaticsVtbl)(unsafe.Pointer(di.RawVTable))

	// IDeviceInformationStatics::FindAllAsyncAqsFilter(HSTRING, IAsyncOperation<DeviceInformationCollection>**)
	var async *ole.IInspectable
	r, _, _ := syscall.SyscallN(diVT.FindAllAsyncAqsFilter, 3,
		uintptr(unsafe.Pointer(di)),
		uintptr(selector),
		uintptr(unsafe.Pointer(&async)))
	if r != 0 || async == nil {
		return nil, ErrWifiDirectUnsupported
	}

	coll, err := awaitAsyncOp(ctx, async)
	if err != nil {
		return nil, ErrWifiDirectUnsupported
	}
	return enumerateDevices(coll)
}

// Connect forms a P2P group with peerID and returns its transfer address as
// "host:port". It calls WiFiDirectDevice.FromIdAsync, awaits the device, and
// reads the first connection endpoint pair's remote host name.
func (m *windowsWifiDirectManager) Connect(ctx context.Context, peerID string) (string, error) {
	wfd, err := ole.RoGetActivationFactory("Windows.Devices.WiFiDirect.WiFiDirectDevice", iidIWiFiDirectDeviceStatics)
	if err != nil {
		return "", ErrWifiDirectUnsupported
	}
	wfdVT := (*iWiFiDirectDeviceStaticsVtbl)(unsafe.Pointer(wfd.RawVTable))

	hid, err := ole.NewHString(peerID)
	if err != nil {
		return "", ErrWifiDirectUnsupported
	}
	defer ole.DeleteHString(hid)

	// IWiFiDirectDeviceStatics::FromIdAsync(HSTRING, IAsyncOperation<WiFiDirectDevice>**)
	var async *ole.IInspectable
	r, _, _ := syscall.SyscallN(wfdVT.FromIdAsync, 3,
		uintptr(unsafe.Pointer(wfd)),
		uintptr(hid),
		uintptr(unsafe.Pointer(&async)))
	if r != 0 || async == nil {
		return "", ErrWifiDirectUnsupported
	}

	device, err := awaitAsyncOp(ctx, async)
	if err != nil {
		return "", ErrWifiDirectUnsupported
	}

	// Hold the device so Close() can dispose it; drop any previous one first.
	m.mu.Lock()
	if m.device != nil {
		releaseInspectable(m.device)
	}
	m.device = device
	m.mu.Unlock()

	devVT := (*iWiFiDirectDeviceVtbl)(unsafe.Pointer(device.RawVTable))

	// IWiFiDirectDevice::get_ConnectionEndpointPairs(IVectorView<EndpointPair>**)
	var pairs *ole.IInspectable
	r, _, _ = syscall.SyscallN(devVT.GetConnectionEndpointPairs, 2,
		uintptr(unsafe.Pointer(device)),
		uintptr(unsafe.Pointer(&pairs)))
	if r != 0 || pairs == nil {
		return "", ErrWifiDirectUnsupported
	}

	pairsVT := (*iVectorViewVtbl)(unsafe.Pointer(pairs.RawVTable))
	var cnt uint32
	syscall.SyscallN(pairsVT.GetSize, 2, uintptr(unsafe.Pointer(pairs)), uintptr(unsafe.Pointer(&cnt)), 0)
	if cnt == 0 {
		return "", ErrWifiDirectUnsupported
	}

	// IVectorView<EndpointPair>::GetAt(uint32, EndpointPair**)
	var ep *ole.IInspectable
	r, _, _ = syscall.SyscallN(pairsVT.GetAt, 3,
		uintptr(unsafe.Pointer(pairs)),
		0,
		uintptr(unsafe.Pointer(&ep)))
	if r != 0 || ep == nil {
		return "", ErrWifiDirectUnsupported
	}

	epVT := (*iEndpointPairVtbl)(unsafe.Pointer(ep.RawVTable))
	// IEndpointPair::get_RemoteHostName(HostName**)
	var hostName *ole.IInspectable
	r, _, _ = syscall.SyscallN(epVT.GetRemoteHostName, 2,
		uintptr(unsafe.Pointer(ep)),
		uintptr(unsafe.Pointer(&hostName)))
	if r != 0 || hostName == nil {
		return "", ErrWifiDirectUnsupported
	}

	hostVT := (*iHostNameVtbl)(unsafe.Pointer(hostName.RawVTable))
	// IHostName::get_DisplayName(HSTRING*)
	var nameH ole.HString
	r, _, _ = syscall.SyscallN(hostVT.GetDisplayName, 2,
		uintptr(unsafe.Pointer(hostName)),
		uintptr(unsafe.Pointer(&nameH)))
	if r != 0 {
		return "", ErrWifiDirectUnsupported
	}
	ip := nameH.String()
	if ip == "" {
		return "", ErrWifiDirectUnsupported
	}
	return net.JoinHostPort(ip, wifidirectTransferPort), nil
}

// Close disposes the held WiFiDirectDevice, if any.
func (m *windowsWifiDirectManager) Close() error {
	m.mu.Lock()
	if m.device != nil {
		releaseInspectable(m.device)
		m.device = nil
	}
	m.mu.Unlock()
	return nil
}

// wfdDeviceSelector returns the AQS selector string
// WiFiDirectDevice.GetDeviceSelector() produces. We call the static method and
// read back the HSTRING result.
func wfdDeviceSelector() (ole.HString, error) {
	wfd, err := ole.RoGetActivationFactory("Windows.Devices.WiFiDirect.WiFiDirectDevice", iidIWiFiDirectDeviceStatics)
	if err != nil {
		return 0, err
	}
	wfdVT := (*iWiFiDirectDeviceStaticsVtbl)(unsafe.Pointer(wfd.RawVTable))

	// IWiFiDirectDeviceStatics::GetDeviceSelector(HSTRING*)
	var selector ole.HString
	r, _, _ := syscall.SyscallN(wfdVT.GetDeviceSelector, 2,
		uintptr(unsafe.Pointer(wfd)),
		uintptr(unsafe.Pointer(&selector)))
	if r != 0 {
		return 0, ole.NewError(r)
	}
	return selector, nil
}

// enumerateDevices walks an IVectorView<DeviceInformation> and returns each
// item's Id and Name as a WifiDirectPeer.
func enumerateDevices(coll *ole.IInspectable) ([]WifiDirectPeer, error) {
	collVT := (*iVectorViewVtbl)(unsafe.Pointer(coll.RawVTable))
	var size uint32
	r, _, _ := syscall.SyscallN(collVT.GetSize, 2, uintptr(unsafe.Pointer(coll)), uintptr(unsafe.Pointer(&size)), 0)
	if r != 0 {
		return nil, ErrWifiDirectUnsupported
	}

	peers := make([]WifiDirectPeer, 0, size)
	for i := uint32(0); i < size; i++ {
		// IVectorView<DeviceInformation>::GetAt(uint32, DeviceInformation**)
		var dev *ole.IInspectable
		r, _, _ := syscall.SyscallN(collVT.GetAt, 3,
			uintptr(unsafe.Pointer(coll)),
			uintptr(i),
			uintptr(unsafe.Pointer(&dev)))
		if r != 0 || dev == nil {
			continue
		}
		devVT := (*iDeviceInformationVtbl)(unsafe.Pointer(dev.RawVTable))

		var idH, nameH ole.HString
		syscall.SyscallN(devVT.GetId, 2, uintptr(unsafe.Pointer(dev)), uintptr(unsafe.Pointer(&idH)), 0)
		syscall.SyscallN(devVT.GetName, 2, uintptr(unsafe.Pointer(dev)), uintptr(unsafe.Pointer(&nameH)), 0)
		peers = append(peers, WifiDirectPeer{ID: idH.String(), Name: nameH.String()})
	}
	return peers, nil
}

// awaitAsyncOp polls IAsyncInfo::get_Status on the given IAsyncOperation until
// it completes, then calls IAsyncOperation::GetResults and returns the result
// object. Polling avoids implementing a completion-handler COM object; ctx lets
// discovery/connect abort early.
func awaitAsyncOp(ctx context.Context, async *ole.IInspectable) (*ole.IInspectable, error) {
	info, err := queryInterface(async, iidIAsyncInfo)
	if err != nil {
		return nil, err
	}
	infoVT := (*iAsyncInfoVtbl)(unsafe.Pointer(info.RawVTable))

	const (
		asyncStatusStarted   = 0
		asyncStatusCompleted = 1
	)

	for {
		var status int32
		r, _, _ := syscall.SyscallN(infoVT.GetStatus, 2,
			uintptr(unsafe.Pointer(info)),
			uintptr(unsafe.Pointer(&status)))
		if r != 0 {
			return nil, ole.NewError(r)
		}
		switch status {
		case asyncStatusCompleted:
			goto done
		case asyncStatusStarted:
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(20 * time.Millisecond):
			}
		default:
			// Canceled (2) or Error (3): the link cannot be established.
			return nil, ErrWifiDirectUnsupported
		}
	}

done:
	opVT := (*iAsyncOperationVtbl)(unsafe.Pointer(async.RawVTable))
	// IAsyncOperation<T>::GetResults(T**)
	var result *ole.IInspectable
	r, _, _ := syscall.SyscallN(opVT.GetResults, 2,
		uintptr(unsafe.Pointer(async)),
		uintptr(unsafe.Pointer(&result)))
	if r != 0 || result == nil {
		return nil, ole.NewError(r)
	}
	return result, nil
}

// queryInterface wraps IUnknown::QueryInterface, returning the requested
// interface as an *ole.IInspectable (callers reinterpret its vtable as needed).
func queryInterface(ins *ole.IInspectable, iid *ole.GUID) (*ole.IInspectable, error) {
	var out *ole.IInspectable
	vt := (*ole.IUnknownVtbl)(unsafe.Pointer(ins.RawVTable))
	r, _, _ := syscall.SyscallN(vt.QueryInterface, 3,
		uintptr(unsafe.Pointer(ins)),
		uintptr(unsafe.Pointer(iid)),
		uintptr(unsafe.Pointer(&out)))
	if r != 0 {
		return nil, ole.NewError(r)
	}
	return out, nil
}

// releaseInspectable calls IUnknown::Release to drop a reference.
func releaseInspectable(ins *ole.IInspectable) {
	if ins == nil {
		return
	}
	vt := (*ole.IUnknownVtbl)(unsafe.Pointer(ins.RawVTable))
	syscall.SyscallN(vt.Release, 1, uintptr(unsafe.Pointer(ins)), 0, 0)
}

// WinRT vtable layouts. Each embeds ole.IInspectableVtbl (IUnknown 3 +
// IInspectable 3) so the first slots align, then lists the interface-specific
// method slots we call. Slot indices follow the documented WinRT ABI order.

type iDeviceInformationStaticsVtbl struct {
	ole.IInspectableVtbl
	FindAllAsync             uintptr
	FindAllAsyncAqsFilter    uintptr
	FindAllAsyncDeviceClass  uintptr
	CreateFromIdAsync        uintptr
	CreateWatcher            uintptr
	CreateWatcherAqsFilter   uintptr
	CreateWatcherDeviceClass uintptr
}

type iWiFiDirectDeviceStaticsVtbl struct {
	ole.IInspectableVtbl
	GetDeviceSelector                   uintptr
	GetDeviceSelectorFromConnectionKind uintptr
	GetDeviceSelectorFromDeviceType     uintptr
	FromIdAsync                         uintptr
}

type iAsyncOperationVtbl struct {
	ole.IInspectableVtbl
	GetCompleted uintptr
	PutCompleted uintptr
	GetResults   uintptr
}

type iAsyncInfoVtbl struct {
	ole.IInspectableVtbl
	GetId        uintptr
	GetStatus    uintptr
	GetErrorCode uintptr
	Cancel       uintptr
}

type iVectorViewVtbl struct {
	ole.IInspectableVtbl
	GetSize uintptr
	GetAt   uintptr
	IndexOf uintptr
	GetMany uintptr
}

type iDeviceInformationVtbl struct {
	ole.IInspectableVtbl
	GetId                uintptr
	GetName              uintptr
	GetIsEnabled         uintptr
	GetEnclosureLocation uintptr
}

type iWiFiDirectDeviceVtbl struct {
	ole.IInspectableVtbl
	GetConnectionStatus        uintptr
	GetDeviceAddress           uintptr
	GetConnectionEndpointPairs uintptr
}

type iEndpointPairVtbl struct {
	ole.IInspectableVtbl
	GetRemoteHostName    uintptr
	GetRemoteServiceName uintptr
	GetLocalHostName     uintptr
	GetLocalServiceName  uintptr
}

type iHostNameVtbl struct {
	ole.IInspectableVtbl
	GetIPInformation uintptr
	GetCanonicalName uintptr
	GetDisplayName   uintptr
	GetRawName       uintptr
	GetType          uintptr
}
