//go:build darwin && cgo

package gss

/*
#cgo CFLAGS: -fmodules
#cgo LDFLAGS: -framework GSS -framework CoreFoundation

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <GSS/GSS.h>

static char *gss_copy_cstring(CFStringRef string) {
	CFIndex size = CFStringGetMaximumSizeForEncoding(CFStringGetLength(string), kCFStringEncodingUTF8) + 1;
	char *buffer = malloc(size);
	if (buffer != NULL && !CFStringGetCString(string, buffer, size, kCFStringEncodingUTF8)) {
		buffer[0] = 0;
	}
	return buffer;
}

static char *gss_status_message(OM_uint32 major, OM_uint32 minor) {
	OM_uint32 ignored, context = 0;
	gss_buffer_desc text = GSS_C_EMPTY_BUFFER;
	if (GSS_ERROR(gss_display_status(&ignored, minor != 0 ? minor : major, minor != 0 ? GSS_C_MECH_CODE : GSS_C_GSS_CODE, GSS_KRB5_MECHANISM, &context, &text))) {
		return NULL;
	}
	char *message = strndup(text.value, text.length);
	gss_release_buffer(&ignored, &text);
	return message;
}

static OM_uint32 gss_import(const char *value, gss_OID type, gss_name_t *name, char **message) {
	OM_uint32 minor;
	gss_buffer_desc buffer = {strlen(value), (void *)value};
	OM_uint32 major = gss_import_name(&minor, &buffer, type, name);
	if (GSS_ERROR(major)) {
		*message = gss_status_message(major, minor);
	}
	return major;
}

static OM_uint32 gss_lkdc_acquire(const char *user, const char *password, const char *host, const char *cache, const char *service, gss_cred_id_t *credential, gss_name_t *target, char **message) {
	gss_name_t name = GSS_C_NO_NAME;
	OM_uint32 minor, major = gss_import(user, GSS_C_NT_USER_NAME, &name, message);
	if (GSS_ERROR(major)) {
		return major;
	}
	CFStringRef passwordString = CFStringCreateWithCString(kCFAllocatorDefault, password, kCFStringEncodingUTF8);
	CFStringRef hostString = CFStringCreateWithCString(kCFAllocatorDefault, host, kCFStringEncodingUTF8);
	CFStringRef cacheString = CFStringCreateWithCString(kCFAllocatorDefault, cache, kCFStringEncodingUTF8);
	const void *keys[] = {kGSSICPassword, kGSSICLKDCHostname, kGSSICKerberosCacheName};
	const void *values[] = {passwordString, hostString, cacheString};
	CFDictionaryRef attributes = CFDictionaryCreate(kCFAllocatorDefault, keys, values, 3, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	CFRelease(passwordString);
	CFRelease(hostString);
	CFRelease(cacheString);
	CFErrorRef error = NULL;
	major = gss_aapl_initial_cred(name, GSS_KRB5_MECHANISM, attributes, credential, &error);
	CFRelease(attributes);
	gss_release_name(&minor, &name);
	if (error != NULL) {
		if (GSS_ERROR(major)) {
			CFStringRef description = CFErrorCopyDescription(error);
			*message = gss_copy_cstring(description);
			CFRelease(description);
		}
		CFRelease(error);
	}
	if (GSS_ERROR(major)) {
		*credential = GSS_C_NO_CREDENTIAL;
		return major;
	}
	gss_name_t credentialName = GSSCredentialCopyName(*credential);
	if (credentialName == GSS_C_NO_NAME) {
		return GSS_S_NO_CRED;
	}
	gss_buffer_desc display = GSS_C_EMPTY_BUFFER;
	major = gss_display_name(&minor, credentialName, &display, NULL);
	gss_release_name(&minor, &credentialName);
	if (GSS_ERROR(major)) {
		*message = gss_status_message(major, minor);
		return major;
	}
	const char *separator = memchr(display.value, '@', display.length);
	if (separator == NULL) {
		gss_release_buffer(&minor, &display);
		return GSS_S_BAD_NAME;
	}
	int realmLength = (int)((const char *)display.value + display.length - separator - 1);
	char *principal = NULL;
	asprintf(&principal, "%s/%.*s@%.*s", service, realmLength, separator + 1, realmLength, separator + 1);
	gss_release_buffer(&minor, &display);
	if (principal == NULL) {
		return GSS_S_FAILURE;
	}
	major = gss_import(principal, GSS_KRB5_NT_PRINCIPAL_NAME, target, message);
	free(principal);
	return major;
}

static OM_uint32 gss_step(gss_cred_id_t credential, gss_ctx_id_t *context, gss_name_t target, void *input, size_t inputLength, gss_buffer_desc *output, char **message) {
	OM_uint32 minor;
	gss_buffer_desc token = {inputLength, input};
	OM_uint32 major = gss_init_sec_context(&minor, credential, context, target, GSS_KRB5_MECHANISM,
		GSS_C_MUTUAL_FLAG | GSS_C_CONF_FLAG | GSS_C_INTEG_FLAG, GSS_C_INDEFINITE, GSS_C_NO_CHANNEL_BINDINGS,
		&token, NULL, output, NULL, NULL);
	if (GSS_ERROR(major)) {
		*message = gss_status_message(major, minor);
	}
	return major;
}

static OM_uint32 gss_unwrap_token(gss_ctx_id_t context, void *input, size_t inputLength, gss_buffer_desc *output, int *confidential, char **message) {
	OM_uint32 minor;
	gss_buffer_desc token = {inputLength, input};
	OM_uint32 major = gss_unwrap(&minor, context, &token, output, confidential, NULL);
	if (GSS_ERROR(major)) {
		*message = gss_status_message(major, minor);
	}
	return major;
}

static void gss_release(gss_cred_id_t *credential, gss_ctx_id_t *context, gss_name_t *target) {
	OM_uint32 minor;
	if (*context != GSS_C_NO_CONTEXT) {
		gss_delete_sec_context(&minor, context, GSS_C_NO_BUFFER);
	}
	if (*target != GSS_C_NO_NAME) {
		gss_release_name(&minor, target);
	}
	if (*credential != GSS_C_NO_CREDENTIAL) {
		gss_destroy_cred(&minor, credential);
	}
}

static void gss_free_buffer(gss_buffer_desc *buffer) {
	OM_uint32 minor;
	gss_release_buffer(&minor, buffer);
}
*/
import "C"

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"unsafe"

	E "github.com/sagernet/sing/common/exceptions"
)

type Initiator struct {
	credential C.gss_cred_id_t
	context    C.gss_ctx_id_t
	target     C.gss_name_t
}

// NewInitiator signs in to the local KDC of the Mac at host. Asked for the
// well-known realm, that KDC refers the client to its own LKDC:SHA1 realm, and
// the Mac's service principals in that realm are service/realm@realm
// (Heimdal kdc and screensharingd 756.36.5.2, macOS 27.0.1 26A434).
func NewInitiator(ctx context.Context, username, password, host, service string) (*Initiator, error) {
	type result struct {
		initiator *Initiator
		err       error
	}
	done := make(chan result, 1)
	go func() {
		initiator, err := acquire(username, password, host, service)
		done <- result{initiator, err}
	}()
	select {
	case r := <-done:
		return r.initiator, r.err
	case <-ctx.Done():
		go func() {
			r := <-done
			if r.initiator != nil {
				r.initiator.Close()
			}
		}()
		return nil, E.Cause(ctx.Err(), "gss: sign in to the Kerberos service")
	}
}

func acquire(username, password, host, service string) (*Initiator, error) {
	var cacheID [16]byte
	rand.Read(cacheID[:])
	user := C.CString(username + "@WELLKNOWN:COM.APPLE.LKDC")
	secret := C.CString(password)
	kdc := C.CString(host)
	cache := C.CString("MEMORY:" + hex.EncodeToString(cacheID[:]))
	serviceName := C.CString(service)
	defer func() {
		C.free(unsafe.Pointer(user))
		C.memset(unsafe.Pointer(secret), 0, C.strlen(secret))
		C.free(unsafe.Pointer(secret))
		C.free(unsafe.Pointer(kdc))
		C.free(unsafe.Pointer(cache))
		C.free(unsafe.Pointer(serviceName))
	}()
	initiator := &Initiator{}
	var message *C.char
	major := C.gss_lkdc_acquire(user, secret, kdc, cache, serviceName, &initiator.credential, &initiator.target, &message)
	if major != C.GSS_S_COMPLETE {
		initiator.Close()
		return nil, statusError("sign in to the Kerberos service", major, message)
	}
	return initiator, nil
}

func (i *Initiator) Step(input []byte) ([]byte, bool, error) {
	var output C.gss_buffer_desc
	var token unsafe.Pointer
	if len(input) > 0 {
		token = C.CBytes(input)
		defer C.free(token)
	}
	var message *C.char
	major := C.gss_step(i.credential, &i.context, i.target, token, C.size_t(len(input)), &output, &message)
	if major != C.GSS_S_COMPLETE && major != C.GSS_S_CONTINUE_NEEDED {
		return nil, false, statusError("establish the Kerberos context", major, message)
	}
	defer C.gss_free_buffer(&output)
	return C.GoBytes(output.value, C.int(output.length)), major == C.GSS_S_COMPLETE, nil
}

func (i *Initiator) Unwrap(input []byte) ([]byte, error) {
	var output C.gss_buffer_desc
	var confidential C.int
	var message *C.char
	token := C.CBytes(input)
	defer C.free(token)
	major := C.gss_unwrap_token(i.context, token, C.size_t(len(input)), &output, &confidential, &message)
	if major != C.GSS_S_COMPLETE {
		return nil, statusError("unwrap the Kerberos message", major, message)
	}
	defer C.gss_free_buffer(&output)
	if confidential == 0 {
		return nil, E.New("gss: the Kerberos message is not encrypted")
	}
	return C.GoBytes(output.value, C.int(output.length)), nil
}

func (i *Initiator) Close() {
	C.gss_release(&i.credential, &i.context, &i.target)
}

func statusError(operation string, major C.OM_uint32, message *C.char) error {
	if message == nil {
		return E.New("gss: ", operation, ": major status ", uint32(major))
	}
	defer C.free(unsafe.Pointer(message))
	return E.New("gss: ", operation, ": ", C.GoString(message))
}
