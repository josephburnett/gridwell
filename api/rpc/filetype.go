package rpc

import (
	"path"
	"strconv"
	"strings"
)

// ContentStamp names a file's bytes by its mtime to the nanosecond and its
// size, so a write within one mtime tick still moves it unless it keeps the
// size. A write claims it, and a preview reads the size off it (StampSize).
func ContentStamp(mtimeNs, size int64) string {
	return strconv.FormatInt(mtimeNs, 10) + "-" + strconv.FormatInt(size, 10)
}

// StampSize is the size a ContentStamp names, ok false for any other stamp.
func StampSize(stamp string) (int64, bool) {
	_, size, ok := strings.Cut(stamp, "-")
	n, err := strconv.ParseInt(size, 10, 64)
	return n, ok && err == nil && n >= 0
}

// extensionTypes names a file's type by its extension alone, so a preview in a
// directory says it without opening the file; the node sniffs the real one
// when the file is opened (files.sniff).
var extensionTypes = map[string]string{
	".gz": "gzip", ".tgz": "gzip", ".bz2": "bzip2", ".xz": "xz", ".zst": "zstd",
	".zip": "zip", ".tar": "tar", ".7z": "7-zip", ".rar": "rar", ".jar": "java archive",
	".deb": "debian package", ".rpm": "rpm package", ".dmg": "disk image", ".iso": "disk image",
	".img": "disk image", ".bin": "binary", ".dat": "data", ".exe": "windows executable",
	".dll": "windows library", ".so": "shared library", ".dylib": "shared library",
	".a": "static library", ".o": "object file", ".class": "java class", ".pyc": "python bytecode",
	".wasm": "webassembly", ".db": "database", ".sqlite": "sqlite", ".sqlite3": "sqlite",
	".ttf": "font", ".otf": "font", ".woff": "font", ".woff2": "font",
	".tif": "image", ".tiff": "image", ".heic": "image", ".psd": "image", ".raw": "image",
	".mov": "video", ".mkv": "video", ".avi": "video", ".aac": "audio", ".opus": "audio",
	".doc": "word document", ".docx": "word document", ".xls": "spreadsheet",
	".xlsx": "spreadsheet", ".ppt": "presentation", ".pptx": "presentation",
	".odt": "document", ".epub": "ebook", ".pem": "certificate", ".der": "certificate",
	".key": "key", ".gpg": "gpg", ".pdf": "pdf", ".png": "png", ".jpg": "jpeg", ".jpeg": "jpeg",
	".gif": "gif", ".webp": "webp", ".mp4": "video", ".webm": "video", ".mp3": "audio",
	".ogg": "audio", ".wav": "audio", ".flac": "audio",
}

// ExtensionType is the type a file's name says: its extension's row, else
// the extension itself, else "file".
func ExtensionType(name string) string {
	ext := strings.ToLower(path.Ext(name))
	switch {
	case extensionTypes[ext] != "":
		return extensionTypes[ext]
	case len(ext) > 1 && ext != strings.ToLower(name):
		return ext[1:]
	}
	return "file"
}
