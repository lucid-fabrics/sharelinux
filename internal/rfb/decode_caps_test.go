package rfb

import (
	"strings"
	"testing"
)

func TestMVSHeldRectangleCount(t *testing.T) {
	payload := mvsFullRefine(false, 3)

	var data []byte
	for i := 0; i < 4; i++ {
		data = append(data, mvsRectangle(24, 0, payload)...)
	}

	client := smallClient(data)
	client.decoders.deferredRectangleLimit = 3

	_, err := client.receiveRectangles(4)
	if err == nil || !strings.Contains(err.Error(), "wait for a resize") {
		t.Fatalf("receiveRectangles() error = %v, want an error containing %q", err, "wait for a resize")
	}

	var resizeData []byte
	for i := 0; i < 3; i++ {
		resizeData = append(resizeData, mvsRectangle(24, 0, payload)...)
	}
	resizeData = append(resizeData, rectangleBytes(0, 0, 32, 32, EncodingDesktopSize)...)

	resizeClient := smallClient(resizeData)
	resizeClient.decoders.deferredRectangleLimit = 3

	if _, err := resizeClient.receiveRectangles(4); err != nil {
		t.Fatalf("receiveRectangles() error = %v, want nil", err)
	}
}

func TestMVSHeldPayloadBytes(t *testing.T) {
	payload := mvsFullRefine(false, 3)
	limit := len(payload) * 2

	var data []byte
	for i := 0; i < 3; i++ {
		data = append(data, mvsRectangle(24, 0, payload)...)
	}

	client := smallClient(data)
	client.decoders.deferredPayloadLimit = limit

	_, err := client.receiveRectangles(3)
	if err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("receiveRectangles() error = %v, want an error containing %q", err, "bytes")
	}

	var resizeData []byte
	for i := 0; i < 2; i++ {
		resizeData = append(resizeData, mvsRectangle(24, 0, payload)...)
	}
	resizeData = append(resizeData, rectangleBytes(0, 0, 32, 32, EncodingDesktopSize)...)

	resizeClient := smallClient(resizeData)
	resizeClient.decoders.deferredPayloadLimit = limit

	if _, err := resizeClient.receiveRectangles(3); err != nil {
		t.Fatalf("receiveRectangles() error = %v, want nil", err)
	}
}

func TestMVSHeldDefaultLimits(t *testing.T) {
	var d decoders
	if err := d.holdMVS(maximumDeferredPayload + 1); err == nil {
		t.Fatal("a payload over the default byte limit was accepted")
	}
	d.deferred = make([]deferredMVS, maximumDeferredRectangles)
	if err := d.holdMVS(0); err == nil {
		t.Fatal("a rectangle over the default count limit was accepted")
	}
}
