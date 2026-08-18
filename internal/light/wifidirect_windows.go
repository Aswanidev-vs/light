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
	"errors"
	"fmt"
	"log"
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

// winrtErr wraps a WinRT failure as a WifiDirect error while staying compatible
// with errors.Is(..., ErrWifiDirectUnsupported), so callers still fall back to
// ordinary LAN discovery. The text carries the failing operation and HRESULT
// for on-device debugging, and the same detail is logged cheaply.
func winrtErr(op string, hr uintptr) error {
	log.Printf("[wifidirect] %s failed (hr=0x%x)", op, hr)
	return fmt.Errorf("wifi-direct: %s failed (hr=0x%x): %w", op, hr, ErrWifiDirectUnsupported)
}

// winrtErrCause is like winrtErr but wraps an existing error cause.
func winrtErrCause(op string, cause error) error {
	if cause == nil {
		cause = errors.New("unknown failure")
	}
	log.Printf("[wifidirect] %s failed: %v", op, cause)
	return fmt.Errorf("wifi-direct: %s failed: %w: %w", op, ErrWifiDirectUnsupported, cause)
}

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
		return nil, winrtErrCause("GetDeviceSelector", err)
	}
	// We own selector (a fresh HSTRING); the callee copies it, so free it once
	// FindAllAsyncAqsFilter returns.
	defer ole.DeleteHString(selector)

	di, err := ole.RoGetActivationFactory("Windows.Devices.Enumeration.DeviceInformation", iidIDeviceInformationStatics)
	if err != nil {
		return nil, winrtErrCause("RoGetActivationFactory(DeviceInformation)", err)
	}
	defer releaseInspectable(di)
	if di.RawVTable == nil {
		return nil, winrtErr("DeviceInformation.RawVTable", 0)
	}
	diVT := (*iDeviceInformationStaticsVtbl)(unsafe.Pointer(di.RawVTable))

	// IDeviceInformationStatics::FindAllAsyncAqsFilter(HSTRING, IAsyncOperation<DeviceInformationCollection>**)
	var async *ole.IInspectable
	r, _, _ := syscall.SyscallN(diVT.FindAllAsyncAqsFilter, 3,
		uintptr(unsafe.Pointer(di)),
		uintptr(selector),
		uintptr(unsafe.Pointer(&async)))
	if r != 0 || async == nil {
		return nil, winrtErr("FindAllAsyncAqsFilter", r)
	}
	defer releaseInspectable(async)

	coll, err := awaitAsyncOp(ctx, async)
	if err != nil {
		return nil, err
	}
	defer releaseInspectable(coll)

	peers, err := enumerateDevices(coll)
	if err != nil {
		return nil, err
	}
	log.Printf("[wifidirect] Discover found %d peer(s)", len(peers))
	return peers, nil
}

// Connect forms a P2P group with peerID and returns its transfer address as
// "host:port". It calls WiFiDirectDevice.FromIdAsync, awaits the device, and
// reads the first connection endpoint pair's remote host name.
func (m *windowsWifiDirectManager) Connect(ctx context.Context, peerID string) (string, error) {
	wfd, err := ole.RoGetActivationFactory("Windows.Devices.WiFiDirect.WiFiDirectDevice", iidIWiFiDirectDeviceStatics)
	if err != nil {
		return "", winrtErrCause("RoGetActivationFactory(WiFiDirectDevice)", err)
	}
	defer releaseInspectable(wfd)
	if wfd.RawVTable == nil {
		return "", winrtErr("WiFiDirectDevice.RawVTable", 0)
	}
	wfdVT := (*iWiFiDirectDeviceStaticsVtbl)(unsafe.Pointer(wfd.RawVTable))

	hid, err := ole.NewHString(peerID)
	if err != nil {
		return "", winrtErrCause("NewHString(peerID)", err)
	}
	defer ole.DeleteHString(hid)

	// IWiFiDirectDeviceStatics::FromIdAsync(HSTRING, IAsyncOperation<WiFiDirectDevice>**)
	var async *ole.IInspectable
	r, _, _ := syscall.SyscallN(wfdVT.FromIdAsync, 3,
		uintptr(unsafe.Pointer(wfd)),
		uintptr(hid),
		uintptr(unsafe.Pointer(&async)))
	if r != 0 || async == nil {
		return "", winrtErr("FromIdAsync", r)
	}
	defer releaseInspectable(async)

	device, err := awaitAsyncOp(ctx, async)
	if err != nil {
		return "", err
	}

	// Hold the device so Close() can dispose it; drop any previous one first.
	m.mu.Lock()
	if m.device != nil {
		releaseInspectable(m.device)
	}
	m.device = device
	m.mu.Unlock()

	if device.RawVTable == nil {
		return "", winrtErr("WiFiDirectDevice result RawVTable", 0)
	}
	devVT := (*iWiFiDirectDeviceVtbl)(unsafe.Pointer(device.RawVTable))

	// IWiFiDirectDevice::get_ConnectionEndpointPairs(IVectorView<EndpointPair>**)
	var pairs *ole.IInspectable
	r, _, _ = syscall.SyscallN(devVT.GetConnectionEndpointPairs, 2,
		uintptr(unsafe.Pointer(device)),
		uintptr(unsafe.Pointer(&pairs)))
	if r != 0 || pairs == nil {
		return "", winrtErr("GetConnectionEndpointPairs", r)
	}
	defer releaseInspectable(pairs)

	pairsVT := (*iVectorViewVtbl)(unsafe.Pointer(pairs.RawVTable))
	var cnt uint32
	syscall.SyscallN(pairsVT.GetSize, 2, uintptr(unsafe.Pointer(pairs)), uintptr(unsafe.Pointer(&cnt)), 0)
	if cnt == 0 {
		return "", winrtErr("GetConnectionEndpointPairs/empty", 0)
	}

	// IVectorView<EndpointPair>::GetAt(uint32, EndpointPair**)
	var ep *ole.IInspectable
	r, _, _ = syscall.SyscallN(pairsVT.GetAt, 3,
		uintptr(unsafe.Pointer(pairs)),
		0,
		uintptr(unsafe.Pointer(&ep)))
	if r != 0 || ep == nil {
		return "", winrtErr("EndpointPair.GetAt", r)
	}
	defer releaseInspectable(ep)

	epVT := (*iEndpointPairVtbl)(unsafe.Pointer(ep.RawVTable))
	// IEndpointPair::get_RemoteHostName(HostName**)
	var hostName *ole.IInspectable
	r, _, _ = syscall.SyscallN(epVT.GetRemoteHostName, 2,
		uintptr(unsafe.Pointer(ep)),
		uintptr(unsafe.Pointer(&hostName)))
	if r != 0 || hostName == nil {
		return "", winrtErr("GetRemoteHostName", r)
	}
	defer releaseInspectable(hostName)

	hostVT := (*iHostNameVtbl)(unsafe.Pointer(hostName.RawVTable))
	// IHostName::get_DisplayName(HSTRING*)
	var nameH ole.HString
	r, _, _ = syscall.SyscallN(hostVT.GetDisplayName, 2,
		uintptr(unsafe.Pointer(hostName)),
		uintptr(unsafe.Pointer(&nameH)))
	if r != 0 {
		return "", winrtErr("GetDisplayName", r)
	}
	defer ole.DeleteHString(nameH)
	ip := nameH.String()
	if ip == "" {
		return "", winrtErr("GetDisplayName/empty", 0)
	}
	addr := net.JoinHostPort(ip, wifidirectTransferPort)
	log.Printf("[wifidirect] Connect established link, peer transfer address=%s", addr)
	return addr, nil
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
		return 0, winrtErrCause("RoGetActivationFactory(WiFiDirectDevice)", err)
	}
	defer releaseInspectable(wfd)
	if wfd.RawVTable == nil {
		return 0, winrtErr("WiFiDirectDevice.RawVTable", 0)
	}
	wfdVT := (*iWiFiDirectDeviceStaticsVtbl)(unsafe.Pointer(wfd.RawVTable))

	// IWiFiDirectDeviceStatics::GetDeviceSelector(HSTRING*)
	var selector ole.HString
	r, _, _ := syscall.SyscallN(wfdVT.GetDeviceSelector, 2,
		uintptr(unsafe.Pointer(wfd)),
		uintptr(unsafe.Pointer(&selector)))
	if r != 0 {
		return 0, winrtErr("GetDeviceSelector", r)
	}
	return selector, nil
}

// enumerateDevices walks an IVectorView<DeviceInformation> and returns each
// item's Id and Name as a WifiDirectPeer.
func enumerateDevices(coll *ole.IInspectable) ([]WifiDirectPeer, error) {
	if coll == nil || coll.RawVTable == nil {
		return nil, winrtErr("enumerateDevices(coll nil)", 0)
	}
	collVT := (*iVectorViewVtbl)(unsafe.Pointer(coll.RawVTable))
	var size uint32
	r, _, _ := syscall.SyscallN(collVT.GetSize, 2, uintptr(unsafe.Pointer(coll)), uintptr(unsafe.Pointer(&size)), 0)
	if r != 0 {
		return nil, winrtErr("DeviceCollection.GetSize", r)
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
		// GetId/GetName allocate HSTRINGs we own; free them immediately after
		// reading so each peer's COM object + HSTRINGs don't pile up across a
		// large enumeration (and are released even on a partial/error path).
		syscall.SyscallN(devVT.GetId, 2, uintptr(unsafe.Pointer(dev)), uintptr(unsafe.Pointer(&idH)), 0)
		syscall.SyscallN(devVT.GetName, 2, uintptr(unsafe.Pointer(dev)), uintptr(unsafe.Pointer(&nameH)), 0)
		id := idH.String()
		name := nameH.String()
		if idH != 0 {
			ole.DeleteHString(idH)
		}
		if nameH != 0 {
			ole.DeleteHString(nameH)
		}
		releaseInspectable(dev)
		peers = append(peers, WifiDirectPeer{ID: id, Name: name})
	}
	return peers, nil
}

// awaitAsyncOp polls IAsyncInfo::get_Status on the given IAsyncOperation until
// it completes, then calls IAsyncOperation::GetResults and returns the result
// object. Polling avoids implementing a completion-handler COM object; ctx lets
// discovery/connect abort early.
func awaitAsyncOp(ctx context.Context, async *ole.IInspectable) (*ole.IInspectable, error) {
	if async == nil || async.RawVTable == nil {
		return nil, winrtErr("await(async nil)", 0)
	}
	info, err := queryInterface(async, iidIAsyncInfo)
	if err != nil {
		return nil, winrtErrCause("QueryInterface(IAsyncInfo)", err)
	}
	// Balance the QueryInterface AddRef; info is only used for status polling.
	defer releaseInspectable(info)
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
			return nil, winrtErr("GetStatus", r)
		}
		switch status {
		case asyncStatusCompleted:
			goto done
		case asyncStatusStarted:
			select {
			case <-ctx.Done():
				// Best-effort cancel the in-flight WinRT operation so it does
				// not keep running after the caller has given up.
				syscall.SyscallN(infoVT.Cancel, 1, uintptr(unsafe.Pointer(info)), 0, 0)
				return nil, winrtErrCause("await", ctx.Err())
			case <-time.After(20 * time.Millisecond):
			}
		default:
			// Canceled (2) or Error (3): the link cannot be established.
			return nil, winrtErr("asyncStatus", uintptr(status))
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
		return nil, winrtErr("GetResults", r)
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

// iDeviceInformationStaticsVtbl follows the REAL WinRT ABI slot order for
// ABI.WINDOWS.DEVICES.ENUMERATION.IDEVICEINFORMATIONSTATICS. The overload
// ordering matters: the parameter-less form comes first, then the DeviceClass
// (enum) overload, then the AQS-filter (string) overload, and the same arity
// rule applies to the CreateWatcher overloads. The previous ordering swapped
// AqsFilter/DeviceClass, which made FindAllAsyncAqsFilter (the only method we
// call) land on FindAllAsyncDeviceClass — passing an HSTRING where an int is
// expected — yielding an empty peer list. Slot indices below follow Microsoft's
// published IDL for this interface.
type iDeviceInformationStaticsVtbl struct {
	ole.IInspectableVtbl
	FindAllAsync             uintptr
	FindAllAsyncDeviceClass  uintptr
	FindAllAsyncAqsFilter    uintptr
	CreateFromIdAsync        uintptr
	CreateWatcher            uintptr
	CreateWatcherDeviceClass uintptr
	CreateWatcherAqsFilter   uintptr
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
