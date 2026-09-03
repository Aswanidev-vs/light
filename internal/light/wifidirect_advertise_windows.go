//go:build windows

// Windows Wi-Fi Direct advertising backend.
//
// Makes this machine discoverable to Wi-Fi Direct peers and accepts incoming
// connection requests via WiFiDirectConnectionListener. Like
// wifidirect_windows.go it drives WinRT through raw COM vtables with go-ole,
// except the event handlers: WinRT events require a real COM delegate object,
// so this file implements TypedEventHandler delegates in pure Go
// (syscall.NewCallback-based vtable + a registry that keeps the Go objects
// alive while COM holds the pointers).
//
// Every IID and vtable slot below is taken from Windows SDK 10.0.22621.0
// winrt/windows.devices.wifidirect.h (MIDL_INTERFACE GUIDs and ABI struct
// method order); any failure degrades to ErrWifiDirectUnsupported like the
// rest of the Windows backend.
package light

import (
	"context"
	"log"
	"net"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
)

// wfdGroupOwnerIntent is 15 = "must become group owner". Only group owners get
// the P2P group's DHCP server address, and the peer must reach OUR transfer
// server, so an accepted connection forces this machine into that role.
const wfdGroupOwnerIntent = 15

var (
	iidRoActivateInstanceDummy int // keeps gofmt grouping stable

	// Contract IIDs (windows.devices.wifidirect.h).
	iidIWFDAdvertisementPublisher = ole.NewGUID("{b35a2d1a-9b1f-45d9-925a-694d66df68ef}")
	iidIWFDAdvertiseStatusArgs    = ole.NewGUID("{aafde53c-5481-46e6-90dd-32116518f192}")
	iidIWFDConnectionListener     = ole.NewGUID("{699c1b0d-8d13-4ee9-b9ec-9c72f8251f7d}")
	iidIWFDConnectionParameters   = ole.NewGUID("{b2e55405-5702-4b16-a02c-bbcd21ef6098}")
	iidIWFDConnectionRequest      = ole.NewGUID("{8eb99605-914f-49c3-a614-d18dc5b19b43}")
	iidIWFDConnectionRequestArgs  = ole.NewGUID("{f99d20be-d38d-484f-8215-e7b65abf244c}")
	iidIWFDDeviceStatics2         = ole.NewGUID("{1a953e49-b103-437e-9226-ab67971342f9}")

	// TypedEventHandler instantiations (__declspec(uuid) in the same header).
	iidTypedHandlerAdvertiseStatus = ole.NewGUID("{de73cba7-370d-550c-b23a-53dd0b4e480d}")
	iidTypedHandlerConnectionReq   = ole.NewGUID("{d04b0403-1fe2-532f-8e47-4823a14e624f}")

	// Standard COM IIDs so QueryInterface below doesn't rely on go-ole exports.
	iidIUnknown     = ole.NewGUID("{00000000-0000-0000-C000-000000000046}")
	iidIInspectable = ole.NewGUID("{AF86E2E0-B12D-4C6A-9C5A-D7AA65101E90}")
	iidIAgileObject = ole.NewGUID("{94EA2B94-E9CC-49E0-C0FF-EE64CA8F5B90}")
)

var procRoActivateInstance = syscall.NewLazyDLL("combase.dll").NewProc("RoActivateInstance")

// winrtActivateInstance constructs a default-constructible WinRT runtime class
// and returns it as its default interface.
func winrtActivateInstance(className string) (*ole.IInspectable, error) {
	h, err := ole.NewHString(className)
	if err != nil {
		return nil, err
	}
	defer ole.DeleteHString(h)
	var ins *ole.IInspectable
	r, _, _ := procRoActivateInstance.Call(uintptr(h), uintptr(unsafe.Pointer(&ins)))
	if r != 0 || ins == nil {
		return nil, ole.NewError(r)
	}
	return ins, nil
}

// ---- TypedEventHandler COM delegate ------------------------------------
//
// WinRT event registration consumes TypedEventHandler<Sender,Args> objects:
// IUnknown (QueryInterface/AddRef/Release) + Invoke. wfdEventSink implements
// that vtable with functions created by syscall.NewCallback; the registry map
// both keeps the Go allocation alive (COM holds a raw pointer) and maps the
// `this` pointer back to state when WinRT calls in on an MTA thread.

type wfdTypedHandlerVtbl struct {
	queryInterface uintptr
	addRef         uintptr
	release        uintptr
	invoke         uintptr
}

type wfdEventSink struct {
	vtbl       uintptr // must stay first: standard COM object layout
	ref        int32
	handlerIID *ole.GUID
	fn         func(sender, args *ole.IInspectable)
}

var (
	wfdSinkMu sync.Mutex
	wfdSinks  = map[uintptr]*wfdEventSink{}
	// One shared vtable: syscall.NewCallback function pointers must outlive
	// every native call, so they live in package-level variables.
	wfdHandlerVtbl = &wfdTypedHandlerVtbl{
		queryInterface: syscall.NewCallback(wfdSinkQueryInterface),
		addRef:         syscall.NewCallback(wfdSinkAddRef),
		release:        syscall.NewCallback(wfdSinkRelease),
		invoke:         syscall.NewCallback(wfdSinkInvoke),
	}
)

func wfdSinkGet(this uintptr) *wfdEventSink {
	wfdSinkMu.Lock()
	defer wfdSinkMu.Unlock()
	return wfdSinks[this]
}

// wfdCallbackPointer turns a native callback argument back into a pointer.
// unsafe.Add keeps this ABI boundary explicit while avoiding a uintptr-to-
// pointer conversion that go vet cannot distinguish from a dangling pointer.
func wfdCallbackPointer(value uintptr) unsafe.Pointer {
	return unsafe.Add(unsafe.Pointer(nil), value)
}

func wfdSinkQueryInterface(this, riid, out uintptr) uintptr {
	ppv := (*uintptr)(wfdCallbackPointer(out))
	*ppv = 0
	s := wfdSinkGet(this)
	if s == nil {
		return uintptr(0x80004002) // E_NOINTERFACE
	}
	req := (*ole.GUID)(wfdCallbackPointer(riid))
	if ole.IsEqualGUID(req, iidIUnknown) || ole.IsEqualGUID(req, iidIInspectable) ||
		ole.IsEqualGUID(req, iidIAgileObject) || ole.IsEqualGUID(req, s.handlerIID) {
		wfdSinkAddRef(this)
		*ppv = this
		return 0 // S_OK
	}
	return uintptr(0x80004002)
}

func wfdSinkAddRef(this uintptr) uintptr {
	wfdSinkMu.Lock()
	defer wfdSinkMu.Unlock()
	s := wfdSinks[this]
	if s == nil {
		return 0
	}
	s.ref++
	return uintptr(s.ref)
}

// wfdSinkRelease drops one reference and evicts the sink from the registry at
// zero so the Go allocation can be collected once COM is done with it.
func wfdSinkRelease(this uintptr) uintptr {
	wfdSinkMu.Lock()
	defer wfdSinkMu.Unlock()
	s := wfdSinks[this]
	if s == nil {
		return 0
	}
	s.ref--
	if s.ref <= 0 {
		delete(wfdSinks, this)
		return 0
	}
	return uintptr(s.ref)
}

// wfdSinkInvoke dispatches on the WinRT event thread; it must stay short and
// never block (handlers copy what they need and defer real work).
func wfdSinkInvoke(this, sender, args uintptr) uintptr {
	s := wfdSinkGet(this)
	if s != nil && s.fn != nil {
		s.fn((*ole.IInspectable)(wfdCallbackPointer(sender)), (*ole.IInspectable)(wfdCallbackPointer(args)))
	}
	return 0
}

// registerWfdEvent wires fn onto obj's add_* slot (add_StatusChanged /
// add_ConnectionRequested). The sink starts with one owner reference; after
// registration WinRT holds its own reference and the owner reference is
// dropped, so remove_* (event fires its release) frees the sink.
func registerWfdEvent(obj *ole.IInspectable, addSlot uintptr, handlerIID *ole.GUID, fn func(*ole.IInspectable, *ole.IInspectable)) (int64, error) {
	s := &wfdEventSink{
		vtbl:       uintptr(unsafe.Pointer(wfdHandlerVtbl)),
		ref:        1,
		handlerIID: handlerIID,
		fn:         fn,
	}
	wfdSinkMu.Lock()
	wfdSinks[uintptr(unsafe.Pointer(s))] = s
	wfdSinkMu.Unlock()

	var token int64
	r, _, _ := syscall.SyscallN(addSlot, 3,
		uintptr(unsafe.Pointer(obj)),
		uintptr(unsafe.Pointer(s)),
		uintptr(unsafe.Pointer(&token)))
	wfdSinkRelease(uintptr(unsafe.Pointer(s)))
	if r != 0 {
		return 0, ole.NewError(r)
	}
	return token, nil
}

// ---- WinRT vtable layouts (windows.devices.wifidirect.h ABI structs) ----

type iWFDAdvertisementPublisherVtbl struct {
	ole.IInspectableVtbl
	GetAdvertisement    uintptr
	GetStatus           uintptr
	AddStatusChanged    uintptr
	RemoveStatusChanged uintptr
	Start               uintptr
	Stop                uintptr
}

type iWFDAdvertiseStatusArgsVtbl struct {
	ole.IInspectableVtbl
	GetStatus uintptr
	GetError  uintptr
}

type iWFDConnectionListenerVtbl struct {
	ole.IInspectableVtbl
	AddConnectionRequested    uintptr
	RemoveConnectionRequested uintptr
}

type iWFDConnectionParametersVtbl struct {
	ole.IInspectableVtbl
	GetGroupOwnerIntent uintptr
	PutGroupOwnerIntent uintptr
}

type iWFDConnectionRequestVtbl struct {
	ole.IInspectableVtbl
	GetDeviceInformation uintptr
}

type iWFDConnectionRequestArgsVtbl struct {
	ole.IInspectableVtbl
	GetConnectionRequest uintptr
}

type iWFDDeviceStatics2Vtbl struct {
	ole.IInspectableVtbl
	GetDeviceSelector uintptr
	FromIdAsync       uintptr
}

// ---- Advertising manager methods ----------------------------------------

// StartAdvertising makes this machine discoverable: it activates the WinRT
// connection listener + advertisement publisher, wires the ConnectionRequested
// and StatusChanged handlers, and starts publishing. onPeerConnected fires for
// every accepted incoming connection with the peer's device id, name, and
// transfer address. Idempotent: advertising twice returns nil.
func (m *windowsWifiDirectManager) StartAdvertising(onPeerConnected func(peerID, peerName, peerAddr string)) error {
	m.mu.Lock()
	if m.publisher != nil {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	listener, err := winrtActivateInstance("Windows.Devices.WiFiDirect.WiFiDirectConnectionListener")
	if err != nil {
		return winrtErrCause("ActivateInstance(WiFiDirectConnectionListener)", err)
	}
	publisher, err := winrtActivateInstance("Windows.Devices.WiFiDirect.WiFiDirectAdvertisementPublisher")
	if err != nil {
		releaseInspectable(listener)
		return winrtErrCause("ActivateInstance(WiFiDirectAdvertisementPublisher)", err)
	}

	listenerVT := (*iWFDConnectionListenerVtbl)(unsafe.Pointer(listener.RawVTable))
	connToken, err := registerWfdEvent(listener, listenerVT.AddConnectionRequested, iidTypedHandlerConnectionReq,
		func(sender, args *ole.IInspectable) { m.handleConnectionRequest(args, onPeerConnected) })
	if err != nil {
		releaseInspectable(listener)
		releaseInspectable(publisher)
		return winrtErrCause("add_ConnectionRequested", err)
	}

	publisherVT := (*iWFDAdvertisementPublisherVtbl)(unsafe.Pointer(publisher.RawVTable))
	statusToken, err := registerWfdEvent(publisher, publisherVT.AddStatusChanged, iidTypedHandlerAdvertiseStatus,
		func(sender, args *ole.IInspectable) { m.handleAdvertiserStatus(args) })
	if err != nil {
		syscall.SyscallN(listenerVT.RemoveConnectionRequested, 2, uintptr(unsafe.Pointer(listener)), uintptr(connToken))
		releaseInspectable(listener)
		releaseInspectable(publisher)
		return winrtErrCause("add_StatusChanged", err)
	}

	if r, _, _ := syscall.SyscallN(publisherVT.Start, 1, uintptr(unsafe.Pointer(publisher))); r != 0 {
		syscall.SyscallN(publisherVT.RemoveStatusChanged, 2, uintptr(unsafe.Pointer(publisher)), uintptr(statusToken))
		syscall.SyscallN(listenerVT.RemoveConnectionRequested, 2, uintptr(unsafe.Pointer(listener)), uintptr(connToken))
		releaseInspectable(listener)
		releaseInspectable(publisher)
		return winrtErr("AdvertisementPublisher.Start", r)
	}

	m.mu.Lock()
	if m.publisher != nil {
		// Lost a race to a concurrent StartAdvertising; publish once only.
		m.mu.Unlock()
		syscall.SyscallN(publisherVT.Stop, 1, uintptr(unsafe.Pointer(publisher)))
		syscall.SyscallN(publisherVT.RemoveStatusChanged, 2, uintptr(unsafe.Pointer(publisher)), uintptr(statusToken))
		syscall.SyscallN(listenerVT.RemoveConnectionRequested, 2, uintptr(unsafe.Pointer(listener)), uintptr(connToken))
		releaseInspectable(listener)
		releaseInspectable(publisher)
		return nil
	}
	m.listener, m.publisher = listener, publisher
	m.connToken, m.statusToken = connToken, statusToken
	m.mu.Unlock()

	log.Printf("[wifidirect] advertising started; this device is now discoverable")
	return nil
}

// StopAdvertising stops publishing and releases the listener/publisher.
// An already-active P2P link survives; DisconnectWifiDirect is what leaves a
// group, matching the non-advertising flow.
func (m *windowsWifiDirectManager) StopAdvertising() error {
	m.mu.Lock()
	listener, publisher := m.listener, m.publisher
	connToken, statusToken := m.connToken, m.statusToken
	m.listener, m.publisher = nil, nil
	m.mu.Unlock()

	if publisher != nil {
		vt := (*iWFDAdvertisementPublisherVtbl)(unsafe.Pointer(publisher.RawVTable))
		syscall.SyscallN(vt.RemoveStatusChanged, 2, uintptr(unsafe.Pointer(publisher)), uintptr(statusToken))
		syscall.SyscallN(vt.Stop, 1, uintptr(unsafe.Pointer(publisher)))
		releaseInspectable(publisher)
	}
	if listener != nil {
		vt := (*iWFDConnectionListenerVtbl)(unsafe.Pointer(listener.RawVTable))
		syscall.SyscallN(vt.RemoveConnectionRequested, 2, uintptr(unsafe.Pointer(listener)), uintptr(connToken))
		releaseInspectable(listener)
	}
	return nil
}

// handleAdvertiserStatus logs publisher state changes: Created=0, Started=1,
// Stopped=2, Aborted=3. Aborted carries a cause (Success=0, RadioNotAvailable=1,
// ResourceInUse=2) — typically the Wi-Fi radio was turned off.
func (m *windowsWifiDirectManager) handleAdvertiserStatus(args *ole.IInspectable) {
	if args == nil || args.RawVTable == nil {
		return
	}
	vt := (*iWFDAdvertiseStatusArgsVtbl)(unsafe.Pointer(args.RawVTable))
	var status int32
	if r, _, _ := syscall.SyscallN(vt.GetStatus, 2, uintptr(unsafe.Pointer(args)), uintptr(unsafe.Pointer(&status))); r != 0 {
		return
	}
	if status != 3 {
		log.Printf("[wifidirect] advertiser status=%d", status)
		return
	}
	var cause int32
	syscall.SyscallN(vt.GetError, 2, uintptr(unsafe.Pointer(args)), uintptr(unsafe.Pointer(&cause)))
	log.Printf("[wifidirect] advertiser aborted (cause=%d: 0=success 1=radio-off 2=resource-in-use)", cause)
}

// handleConnectionRequest runs on the WinRT event thread: it extracts the peer
// identity while the args objects are alive, then dispatches the accept to a
// goroutine — Invoke must not block, and the accept involves pairing round
// trips. A group-owner-intent-15 FromIdAsync makes this machine the group
// owner so the peer can reach the transfer server on this side.
func (m *windowsWifiDirectManager) handleConnectionRequest(args *ole.IInspectable, onPeerConnected func(string, string, string)) {
	if args == nil || args.RawVTable == nil {
		return
	}
	argsVT := (*iWFDConnectionRequestArgsVtbl)(unsafe.Pointer(args.RawVTable))
	var req *ole.IInspectable
	if r, _, _ := syscall.SyscallN(argsVT.GetConnectionRequest, 2, uintptr(unsafe.Pointer(args)), uintptr(unsafe.Pointer(&req))); r != 0 || req == nil {
		log.Printf("[wifidirect] ConnectionRequested without request object")
		return
	}
	reqVT := (*iWFDConnectionRequestVtbl)(unsafe.Pointer(req.RawVTable))
	var dev *ole.IInspectable
	r, _, _ := syscall.SyscallN(reqVT.GetDeviceInformation, 2, uintptr(unsafe.Pointer(req)), uintptr(unsafe.Pointer(&dev)))
	if r != 0 || dev == nil {
		releaseInspectable(req)
		log.Printf("[wifidirect] ConnectionRequested without device info")
		return
	}
	id, name := deviceIDName(dev)
	releaseInspectable(dev)
	releaseInspectable(req)
	if id == "" {
		log.Printf("[wifidirect] ConnectionRequested with empty device id")
		return
	}

	log.Printf("[wifidirect] incoming connection request from %q; accepting", id)
	go m.acceptIncomingConnection(id, name, onPeerConnected)
}

// acceptIncomingConnection forms the group for an incoming request and reports
// the peer's transfer address. The WiFiDirectDevice is retained on the manager
// (like Connect does) so the group stays up until Close/Disconnect.
func (m *windowsWifiDirectManager) acceptIncomingConnection(peerID, peerName string, onPeerConnected func(string, string, string)) {
	wfd, err := ole.RoGetActivationFactory("Windows.Devices.WiFiDirect.WiFiDirectDevice", iidIWiFiDirectDeviceStatics)
	if err != nil {
		log.Printf("[wifidirect] accept: %v", winrtErrCause("RoGetActivationFactory(WiFiDirectDevice)", err))
		return
	}
	statics, err := queryInterface(wfd, iidIWFDDeviceStatics2)
	releaseInspectable(wfd)
	if err != nil {
		log.Printf("[wifidirect] accept: %v", winrtErrCause("QueryInterface(IWiFiDirectDeviceStatics2)", err))
		return
	}
	staticsVT := (*iWFDDeviceStatics2Vtbl)(unsafe.Pointer(statics.RawVTable))

	params, err := winrtActivateInstance("Windows.Devices.WiFiDirect.WiFiDirectConnectionParameters")
	if err != nil {
		releaseInspectable(statics)
		log.Printf("[wifidirect] accept: %v", winrtErrCause("ActivateInstance(WiFiDirectConnectionParameters)", err))
		return
	}
	paramsVT := (*iWFDConnectionParametersVtbl)(unsafe.Pointer(params.RawVTable))
	syscall.SyscallN(paramsVT.PutGroupOwnerIntent, 2, uintptr(unsafe.Pointer(params)), uintptr(wfdGroupOwnerIntent))

	hid, err := ole.NewHString(peerID)
	if err != nil {
		releaseInspectable(params)
		releaseInspectable(statics)
		log.Printf("[wifidirect] accept: %v", winrtErrCause("NewHString(peerID)", err))
		return
	}
	defer ole.DeleteHString(hid)

	var async *ole.IInspectable
	r, _, _ := syscall.SyscallN(staticsVT.FromIdAsync, 4,
		uintptr(unsafe.Pointer(statics)),
		uintptr(hid),
		uintptr(unsafe.Pointer(params)),
		uintptr(unsafe.Pointer(&async)))
	releaseInspectable(params)
	releaseInspectable(statics)
	if r != 0 || async == nil {
		log.Printf("[wifidirect] accept: FromIdAsync failed (hr=0x%x)", r)
		return
	}
	defer releaseInspectable(async)

	ctx, cancel := context.WithTimeout(context.Background(), wifiDirectConnectTimeout)
	defer cancel()
	device, err := awaitAsyncOp(ctx, async)
	if err != nil {
		log.Printf("[wifidirect] accept: %v", err)
		return
	}

	peer, local, err := wfdEndpoints(device)
	if err != nil {
		releaseInspectable(device)
		log.Printf("[wifidirect] accept: %v", err)
		return
	}
	m.mu.Lock()
	if m.device != nil {
		releaseInspectable(m.device)
	}
	m.device = device
	m.mu.Unlock()

	peerAddr := net.JoinHostPort(peer, wifidirectTransferPort)
	log.Printf("[wifidirect] incoming connection established; group-owner this machine (us=%s, peer=%s)", local, peerAddr)
	if onPeerConnected != nil {
		onPeerConnected(peerID, peerName, peerAddr)
	}
}

// deviceIDName reads the two strings needed by the incoming-connection
// callback while the DeviceInformation object is still alive.
func deviceIDName(dev *ole.IInspectable) (string, string) {
	if dev == nil || dev.RawVTable == nil {
		return "", ""
	}
	vt := (*iDeviceInformationVtbl)(unsafe.Pointer(dev.RawVTable))
	var idH, nameH ole.HString
	if r, _, _ := syscall.SyscallN(vt.GetId, 2,
		uintptr(unsafe.Pointer(dev)), uintptr(unsafe.Pointer(&idH))); r != 0 {
		return "", ""
	}
	defer ole.DeleteHString(idH)
	if r, _, _ := syscall.SyscallN(vt.GetName, 2,
		uintptr(unsafe.Pointer(dev)), uintptr(unsafe.Pointer(&nameH))); r != 0 {
		return idH.String(), ""
	}
	defer ole.DeleteHString(nameH)
	return idH.String(), nameH.String()
}

// wfdEndpoints returns the remote and local host names from a connected
// WiFiDirectDevice. EndpointPair is used for both outgoing and incoming
// connections, so keeping this in one helper prevents the two paths from
// drifting apart.
func wfdEndpoints(device *ole.IInspectable) (string, string, error) {
	if device == nil || device.RawVTable == nil {
		return "", "", winrtErr("wfdEndpoints(device nil)", 0)
	}
	deviceVT := (*iWiFiDirectDeviceVtbl)(unsafe.Pointer(device.RawVTable))
	var pairs *ole.IInspectable
	if r, _, _ := syscall.SyscallN(deviceVT.GetConnectionEndpointPairs, 2,
		uintptr(unsafe.Pointer(device)), uintptr(unsafe.Pointer(&pairs))); r != 0 || pairs == nil {
		return "", "", winrtErr("GetConnectionEndpointPairs", r)
	}
	defer releaseInspectable(pairs)
	if pairs.RawVTable == nil {
		return "", "", winrtErr("ConnectionEndpointPairs.RawVTable", 0)
	}

	pairsVT := (*iVectorViewVtbl)(unsafe.Pointer(pairs.RawVTable))
	var count uint32
	if r, _, _ := syscall.SyscallN(pairsVT.GetSize, 2,
		uintptr(unsafe.Pointer(pairs)), uintptr(unsafe.Pointer(&count))); r != 0 {
		return "", "", winrtErr("ConnectionEndpointPairs.GetSize", r)
	}
	if count == 0 {
		return "", "", winrtErr("ConnectionEndpointPairs/empty", 0)
	}

	var pair *ole.IInspectable
	if r, _, _ := syscall.SyscallN(pairsVT.GetAt, 3,
		uintptr(unsafe.Pointer(pairs)), 0, uintptr(unsafe.Pointer(&pair))); r != 0 || pair == nil {
		return "", "", winrtErr("ConnectionEndpointPairs.GetAt", r)
	}
	defer releaseInspectable(pair)
	if pair.RawVTable == nil {
		return "", "", winrtErr("EndpointPair.RawVTable", 0)
	}

	pairVT := (*iEndpointPairVtbl)(unsafe.Pointer(pair.RawVTable))
	var remote, local *ole.IInspectable
	if r, _, _ := syscall.SyscallN(pairVT.GetRemoteHostName, 2,
		uintptr(unsafe.Pointer(pair)), uintptr(unsafe.Pointer(&remote))); r != 0 || remote == nil {
		return "", "", winrtErr("EndpointPair.GetRemoteHostName", r)
	}
	defer releaseInspectable(remote)
	if r, _, _ := syscall.SyscallN(pairVT.GetLocalHostName, 2,
		uintptr(unsafe.Pointer(pair)), uintptr(unsafe.Pointer(&local))); r != 0 || local == nil {
		return "", "", winrtErr("EndpointPair.GetLocalHostName", r)
	}
	defer releaseInspectable(local)

	remoteName, err := hostNameDisplayName(remote)
	if err != nil {
		return "", "", err
	}
	localName, err := hostNameDisplayName(local)
	if err != nil {
		return "", "", err
	}
	return remoteName, localName, nil
}

func hostNameDisplayName(hostName *ole.IInspectable) (string, error) {
	if hostName == nil || hostName.RawVTable == nil {
		return "", winrtErr("HostName.RawVTable", 0)
	}
	vt := (*iHostNameVtbl)(unsafe.Pointer(hostName.RawVTable))
	var display ole.HString
	if r, _, _ := syscall.SyscallN(vt.GetDisplayName, 2,
		uintptr(unsafe.Pointer(hostName)), uintptr(unsafe.Pointer(&display))); r != 0 {
		return "", winrtErr("HostName.GetDisplayName", r)
	}
	defer ole.DeleteHString(display)
	value := display.String()
	if value == "" {
		return "", winrtErr("HostName.GetDisplayName/empty", 0)
	}
	return value, nil
}

// wfdAcceptDeadline bounds how long an incoming connect may take; exported for
// tests via the wifiDirectConnectTimeout constant in discovery.go.
var _ = time.Second
