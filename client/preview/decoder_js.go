//go:build js && wasm

package preview

import "syscall/js"

// JSDecoder feeds bytes through the browser's Blob, createObjectURL and new
// Image() chain, so onReady fires on the JS event loop.
type JSDecoder struct{}

func NewJSDecoder() JSDecoder { return JSDecoder{} }

// Decode loads the bytes as a JPEG through LoadImage.
func (JSDecoder) Decode(bytes []byte, onReady func(Image), onError func()) {
	if len(bytes) == 0 {
		if onError != nil {
			onError()
		}
		return
	}
	u8 := js.Global().Get("Uint8Array").New(len(bytes))
	js.CopyBytesToJS(u8, bytes)
	blob := js.Global().Get("Blob").New(js.ValueOf([]any{u8}), js.ValueOf(map[string]any{"type": "image/jpeg"}))
	LoadImage(blob, func(img *JSImage) {
		if onReady == nil {
			// With no consumer, revoke here so the object URL does not leak.
			img.Revoke()
			return
		}
		onReady(img)
	}, onError)
}

// LoadImage is the one Blob, createObjectURL and new Image() chain, for a
// JPEG preview and a rendered SVG alike: onReady fires on the JS event loop
// with the loaded image, and a failed load revokes its object URL before
// onError. It allocates an onload and an onerror js.Func, both released once
// either fires.
func LoadImage(blob js.Value, onReady func(*JSImage), onError func()) {
	objectURL := js.Global().Get("URL").Call("createObjectURL", blob).String()
	img := js.Global().Get("Image").New()
	var onload, onerr js.Func
	release := func() { onload.Release(); onerr.Release() }
	onload = js.FuncOf(func(js.Value, []js.Value) any {
		release()
		onReady(&JSImage{val: img, objectURL: objectURL})
		return nil
	})
	onerr = js.FuncOf(func(js.Value, []js.Value) any {
		release()
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

// JSImage wraps an HTMLImageElement and the createObjectURL it was loaded from.
type JSImage struct {
	val       js.Value
	objectURL string
	revoked   bool
}

// Val is the HTMLImageElement for canvas.drawImage.
func (i *JSImage) Val() js.Value { return i.val }

// Truthy is false after Revoke, the object URL being gone.
func (i *JSImage) Truthy() bool {
	if i == nil || i.revoked {
		return false
	}
	return i.val.Truthy()
}

// Revoke releases the createObjectURL. It is idempotent.
func (i *JSImage) Revoke() {
	if i == nil || i.revoked {
		return
	}
	i.revoked = true
	js.Global().Get("URL").Call("revokeObjectURL", i.objectURL)
}
