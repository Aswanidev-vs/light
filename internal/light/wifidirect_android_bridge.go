//go:build android

package light

// cgo JNI bridge for the Android Wi-Fi Direct backend.
//
// This file declares the native side of the Java<->Go contract that the host
// (WailsBridge.java) drives for Wi-Fi Direct:
//
//   Java -> Go  (host calls Go, pushes state down):
//     nativeWifiDirectInit()                // host stores the bridge ref in Go
//     nativeWifiDirectReportPeers(String)  // host streams the peer list (JSON)
//     nativeWifiDirectConnected(String)    // host delivers the group owner IP
//     nativeWifiDirectError(String)        // host reports a handshake failure
//
//   Go -> Java  (Go commands the host, public methods on WailsBridge):
//     wifiDirectStartDiscovery()
//     wifiDirectConnect(String deviceAddress)
//     wifiDirectCloseGroup()
//
// The bridge object reference and JavaVM are captured when the host calls
// nativeWifiDirectInit (from WailsBridge.initialize) so later Go->Java calls
// from Discover/Connect/Close can reach the WifiP2pManager the host owns. This
// mirrors the JNI plumbing in the wails module's application_android.go, kept
// separate here so the wifidirect code is self-contained.

/*
#include <jni.h>
#include <stdlib.h>
#include <string.h>

// Global JavaVM + bridge object, captured in nativeWifiDirectInit.
static JavaVM* wd_jvm = NULL;
static jobject wd_bridge = NULL;

// Attach the current thread and return a JNIEnv, detaching afterwards if we
// attached it. Returns NULL when the bridge was never initialised.
static JNIEnv* wdGetEnv(int* detach) {
    *detach = 0;
    if (wd_jvm == NULL) return NULL;
    JNIEnv* env = NULL;
    jint r = (*wd_jvm)->GetEnv(wd_jvm, (void**)&env, JNI_VERSION_1_6);
    if (r == JNI_EDETACHED) {
        if ((*wd_jvm)->AttachCurrentThread(wd_jvm, &env, NULL) != 0) return NULL;
        *detach = 1;
    } else if (r != JNI_OK) {
        return NULL;
    }
    return env;
}

static void wdReleaseEnv(int detach) {
    if (detach && wd_jvm != NULL) (*wd_jvm)->DetachCurrentThread(wd_jvm);
}

static void wdStoreBridge(JNIEnv* env, jobject bridge) {
    if ((*env)->GetJavaVM(env, &wd_jvm) != 0) return;
    if (wd_bridge != NULL) (*env)->DeleteGlobalRef(env, wd_bridge);
    wd_bridge = (*env)->NewGlobalRef(env, bridge);
}

static const char* wdJStringToC(JNIEnv* env, jstring s) {
    return (s == NULL) ? NULL : (*env)->GetStringUTFChars(env, s, NULL);
}

static void wdReleaseJString(JNIEnv* env, jstring s, const char* c) {
    if (s != NULL && c != NULL) (*env)->ReleaseStringUTFChars(env, s, c);
}

static void wdClearException(JNIEnv* env) {
    if ((*env)->ExceptionCheck(env)) {
        (*env)->ExceptionDescribe(env);
        (*env)->ExceptionClear(env);
    }
}

// Call `void name()` on the bridge.
static void wdCallVoid(const char* name) {
    if (wd_bridge == NULL) return;
    int detach = 0;
    JNIEnv* env = wdGetEnv(&detach);
    if (env == NULL) return;
    jclass cls = (*env)->GetObjectClass(env, wd_bridge);
    if (cls != NULL) {
        jmethodID mid = (*env)->GetMethodID(env, cls, name, "()V");
        if (mid != NULL) (*env)->CallVoidMethod(env, wd_bridge, mid);
        wdClearException(env);
        (*env)->DeleteLocalRef(env, cls);
    }
    wdReleaseEnv(detach);
}

// Call `void name(String)` on the bridge.
static void wdCallVoidString(const char* name, const char* arg) {
    if (wd_bridge == NULL) return;
    int detach = 0;
    JNIEnv* env = wdGetEnv(&detach);
    if (env == NULL) return;
    jclass cls = (*env)->GetObjectClass(env, wd_bridge);
    if (cls != NULL) {
        jmethodID mid = (*env)->GetMethodID(env, cls, name, "(Ljava/lang/String;)V");
        if (mid != NULL) {
            jstring jarg = (*env)->NewStringUTF(env, arg);
            (*env)->CallVoidMethod(env, wd_bridge, mid, jarg);
            wdClearException(env);
            if (jarg != NULL) (*env)->DeleteLocalRef(env, jarg);
        } else {
            wdClearException(env);
        }
        (*env)->DeleteLocalRef(env, cls);
    }
    wdReleaseEnv(detach);
}

static int wdBridgePresent() { return wd_bridge != NULL; }
*/
import "C"

import (
	"unsafe"
)

// wdBridgeReady reports whether the Java host has initialised the bridge (so
// Go->Java calls can succeed).
func wdBridgeReady() bool {
	return C.wdBridgePresent() != 0
}

// --- Go -> Java command helpers (called from Discover/Connect/Close) ---------

func wdStartDiscovery() {
	name := C.CString("wifiDirectStartDiscovery")
	defer C.free(unsafe.Pointer(name))
	C.wdCallVoid(name)
}

func wdConnect(peerID string) {
	name := C.CString("wifiDirectConnect")
	defer C.free(unsafe.Pointer(name))
	arg := C.CString(peerID)
	defer C.free(unsafe.Pointer(arg))
	C.wdCallVoidString(name, arg)
}

func wdCloseGroup() {
	name := C.CString("wifiDirectCloseGroup")
	defer C.free(unsafe.Pointer(name))
	C.wdCallVoid(name)
}

// --- Java -> Go JNI exports --------------------------------------------------

//export Java_com_wails_app_WailsBridge_nativeWifiDirectInit
func Java_com_wails_app_WailsBridge_nativeWifiDirectInit(env *C.JNIEnv, thiz C.jobject) {
	C.wdStoreBridge(env, thiz)
	C.wdClearException(env)
}

//export Java_com_wails_app_WailsBridge_nativeWifiDirectReportPeers
func Java_com_wails_app_WailsBridge_nativeWifiDirectReportPeers(env *C.JNIEnv, thiz C.jobject, jjson C.jstring) {
	c := C.wdJStringToC(env, jjson)
	if c == nil {
		return
	}
	s := C.GoString(c)
	C.wdReleaseJString(env, jjson, c)
	wdNotifyPeers(s)
	C.wdClearException(env)
}

//export Java_com_wails_app_WailsBridge_nativeWifiDirectConnected
func Java_com_wails_app_WailsBridge_nativeWifiDirectConnected(env *C.JNIEnv, thiz C.jobject, jip C.jstring) {
	c := C.wdJStringToC(env, jip)
	if c == nil {
		return
	}
	s := C.GoString(c)
	C.wdReleaseJString(env, jip, c)
	wdNotifyConnected(s)
	C.wdClearException(env)
}

//export Java_com_wails_app_WailsBridge_nativeWifiDirectError
func Java_com_wails_app_WailsBridge_nativeWifiDirectError(env *C.JNIEnv, thiz C.jobject, jmsg C.jstring) {
	c := C.wdJStringToC(env, jmsg)
	if c == nil {
		return
	}
	s := C.GoString(c)
	C.wdReleaseJString(env, jmsg, c)
	// The message is logged on the Java side; here we only flag the failure so a
	// waiting Connect returns ErrWifiDirectUnsupported promptly.
	wdNotifyError(s)
	C.wdClearException(env)
}
