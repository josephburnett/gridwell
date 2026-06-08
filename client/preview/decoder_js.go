//go:build js && wasm

package preview

import "syscall/js"

// JSDecoder is the production Decoder. It feeds bytes through the
// browser's Blob + URL.createObjectURL + new Image() chain so the
// decode happens off the main thread; onReady fires on the JS event
// loop once the image's load event resolves.
type JSDecoder struct{}

// NewJSDecoder returns a Decoder suitable for the wasm build.
func NewJSDecoder() JSDecoder { return JSDecoder{} }

// Decode implements Decoder. The decoded handle is a *JSImage whose
// Val() gives the renderer the HTMLImageElement to pass to
// canvas.drawImage. Each Decode allocates two js.Func handlers
// (onload, onerror); both are Released once one fires.
func (JSDecoder) Decode(bytes []byte, onReady func(Image), onError func()) {
	if len(bytes) == 0 {
		if onError != nil {
			onError()
		}
		return
	}
	u8 := js.Global().Get("Uint8Array").New(len(bytes))
	js.CopyBytesToJS(u8, bytes)
	blobOpts := js.Global().Get("Object").New()
	blobOpts.Set("type", "image/jpeg")
	parts := js.Global().Get("Array").New()
	parts.Call("push", u8)
	blob := js.Global().Get("Blob").New(parts, blobOpts)
	objectURL := js.Global().Get("URL").Call("createObjectURL", blob).String()

	img := js.Global().Get("Image").New()
	var onload, onerr js.Func
	onload = js.FuncOf(func(js.Value, []js.Value) any {
		onload.Release()
		onerr.Release()
		if onReady != nil {
			onReady(&JSImage{val: img, objectURL: objectURL})
		} else {
			// No consumer — leak-free fallback.
			js.Global().Get("URL").Call("revokeObjectURL", objectURL)
		}
		return nil
	})
	onerr = js.FuncOf(func(js.Value, []js.Value) any {
		onload.Release()
		onerr.Release()
		js.Global().Get("URL").Call("revokeObjectURL", objectURL)
		if onError != nil {
			onError()
		}
		return nil
	})
	img.Set("onload", onload)
	img.Set("onerror", onerr)
	img.Set("src", objectURL)
}

// JSImage wraps an HTMLImageElement and the createObjectURL it was
// loaded from. The renderer reaches the raw js.Value via Val(); the
// cache calls Revoke() when the entry is replaced or dropped.
type JSImage struct {
	val       js.Value
	objectURL string
	revoked   bool
}

// Val returns the underlying HTMLImageElement so the renderer can
// pass it straight to canvas.drawImage.
func (i *JSImage) Val() js.Value { return i.val }

// Truthy implements Image: reports whether the element is still
// usable. After Revoke the object URL is gone and the image will
// fail to paint, so we report false.
func (i *JSImage) Truthy() bool {
	if i == nil || i.revoked {
		return false
	}
	return i.val.Truthy()
}

// Revoke implements Image: releases the createObjectURL. Idempotent.
// The cache calls this when a newer Put supersedes the entry or when
// Drop removes it.
func (i *JSImage) Revoke() {
	if i == nil || i.revoked {
		return
	}
	i.revoked = true
	js.Global().Get("URL").Call("revokeObjectURL", i.objectURL)
}
