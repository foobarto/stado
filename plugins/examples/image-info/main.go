// image-info — read just enough of an image file to report format,
// dimensions, color depth, and byte size. Pixel data is never decoded;
// the plugin only walks header bytes.
//
// Why this is useful: an LLM looking at a directory of screenshots
// usually wants "what is each one" before deciding whether to actually
// view the image. This plugin gives the cheap answer without a full
// decoder + 200 MB of memory.
//
// Supported formats: PNG, JPEG, GIF87a/89a, WebP (VP8 / VP8L / VP8X), BMP.
// Anything else returns format="unknown" with raw size only.
//
// Tool:
//
//   image_info {path}
//     → {path, format, width, height, color_depth?, animated?, size_bytes}
//
// Capabilities:
//   - fs:read:. — read inside the workdir
package main

import (
	"encoding/binary"
	"encoding/json"
	"strings"
	"sync"
	"unsafe"
)

func main() {}

//go:wasmimport stado stado_log
func stadoLog(levelPtr, levelLen, msgPtr, msgLen uint32)

//go:wasmimport stado stado_fs_read
func stadoFsRead(pathPtr, pathLen, bufPtr, bufCap uint32) int32

func logInfo(msg string) {
	level := []byte("info")
	m := []byte(msg)
	stadoLog(
		uint32(uintptr(unsafe.Pointer(&level[0]))), uint32(len(level)),
		uint32(uintptr(unsafe.Pointer(&m[0]))), uint32(len(m)),
	)
}

var pinned sync.Map

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 {
	if size <= 0 {
		return 0
	}
	buf := make([]byte, size)
	ptr := uintptr(unsafe.Pointer(&buf[0]))
	pinned.Store(ptr, buf)
	return int32(ptr)
}

//go:wasmexport stado_free
func stadoFree(ptr int32, _ int32) {
	pinned.Delete(uintptr(ptr))
}

type infoArgs struct {
	Path string `json:"path"`
}

type infoResult struct {
	Path       string `json:"path"`
	Format     string `json:"format"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	ColorDepth int    `json:"color_depth,omitempty"`
	Animated   bool   `json:"animated,omitempty"`
	SizeBytes  int    `json:"size_bytes"`
	Note       string `json:"note,omitempty"`
}

type errResult struct {
	Error string `json:"error"`
}

// 16 MiB buffer matches the host's hard cap (maxPluginRuntimeFSFileBytes).
// stado_fs_read refuses any file larger than the buffer cap rather than
// returning a partial read, so we have to size for the whole file —
// even though this plugin only needs the first ~64 KiB. A future
// `stado_fs_read_partial(path, offset, length)` host import would let
// us drop this to 64 KiB; for now, accept the over-allocation.
const headerCap = 16 << 20

//go:wasmexport stado_tool_image_info
func stadoToolImageInfo(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	logInfo("image-info invoked")

	args := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(argsPtr))), int(argsLen))
	var a infoArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return writeJSON(resultPtr, resultCap, errResult{Error: "args: " + err.Error()})
		}
	}
	if strings.TrimSpace(a.Path) == "" {
		return writeJSON(resultPtr, resultCap, errResult{Error: "path is required"})
	}

	buf := make([]byte, headerCap)
	pathBytes := []byte(a.Path)
	n := stadoFsRead(
		uint32(uintptr(unsafe.Pointer(&pathBytes[0]))), uint32(len(pathBytes)),
		uint32(uintptr(unsafe.Pointer(&buf[0]))), uint32(headerCap),
	)
	if n < 0 {
		return writeJSON(resultPtr, resultCap, errResult{
			Error: "stado_fs_read returned -1 — path outside fs:read scope or file missing",
		})
	}
	header := buf[:n]

	res := infoResult{Path: a.Path, SizeBytes: int(n)}
	parseHeader(header, &res)

	return writeJSON(resultPtr, resultCap, res)
}

// parseHeader fills res based on the first bytes of the file. If
// nothing matches, format stays "unknown".
func parseHeader(b []byte, res *infoResult) {
	switch {
	case isPNG(b):
		res.Format = "png"
		parsePNG(b, res)
	case isJPEG(b):
		res.Format = "jpeg"
		parseJPEG(b, res)
	case isGIF(b):
		res.Format = "gif"
		parseGIF(b, res)
	case isWebP(b):
		res.Format = "webp"
		parseWebP(b, res)
	case isBMP(b):
		res.Format = "bmp"
		parseBMP(b, res)
	default:
		res.Format = "unknown"
		res.Note = "no recognized magic bytes — only the file size is reliable"
	}
}

// ---- PNG ---------------------------------------------------------------
//
// Layout: 8-byte signature, then chunks. First chunk is always IHDR:
//   offset  8: length (4) = 13
//   offset 12: type     = "IHDR"
//   offset 16: width    (uint32 BE)
//   offset 20: height   (uint32 BE)
//   offset 24: bit_depth (uint8)
//   offset 25: color_type (uint8)

var pngMagic = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}

func isPNG(b []byte) bool { return len(b) >= 8 && bytesEqual(b[:8], pngMagic) }

func parsePNG(b []byte, res *infoResult) {
	if len(b) < 26 {
		res.Note = "truncated PNG header"
		return
	}
	res.Width = int(binary.BigEndian.Uint32(b[16:20]))
	res.Height = int(binary.BigEndian.Uint32(b[20:24]))
	bitDepth := int(b[24])
	colorType := b[25]
	channels := pngChannelsFor(colorType)
	if channels > 0 {
		res.ColorDepth = bitDepth * channels
	}
}

func pngChannelsFor(colorType byte) int {
	// PNG color types (RFC 2083 §4.1.1):
	//   0 grayscale       1 channel
	//   2 truecolor       3 channels
	//   3 indexed         1 channel (8 bpp through palette)
	//   4 greyscale+alpha 2 channels
	//   6 truecolor+alpha 4 channels
	switch colorType {
	case 0:
		return 1
	case 2:
		return 3
	case 3:
		return 1
	case 4:
		return 2
	case 6:
		return 4
	}
	return 0
}

// ---- JPEG --------------------------------------------------------------
//
// JFIF/Exif file: starts with FF D8, then a sequence of segments. Each
// segment after the SOI is `FF <marker> <len-be-2bytes> <payload>`. We
// scan forward until we hit a Start-Of-Frame marker (FF C0..C3, C5..C7,
// C9..CB, CD..CF — but NOT CC which is DAC, and not C4 / C8 which are
// DHT / JPG); the SOF payload begins with bit_depth(1), height(2 BE),
// width(2 BE), components(1).

func isJPEG(b []byte) bool {
	return len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF
}

func parseJPEG(b []byte, res *infoResult) {
	// Skip SOI (FF D8). i points to the next byte, expected FF.
	i := 2
	for i < len(b)-9 {
		if b[i] != 0xFF {
			i++
			continue
		}
		// Skip fill bytes.
		for i < len(b) && b[i] == 0xFF {
			i++
		}
		if i >= len(b) {
			break
		}
		marker := b[i]
		i++
		// Markers without payload (RST0..7 and TEM, EOI).
		if marker == 0xD9 || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01 {
			continue
		}
		if i+2 > len(b) {
			break
		}
		segLen := int(binary.BigEndian.Uint16(b[i : i+2]))
		if isSOF(marker) {
			if i+7 <= len(b) {
				bitDepth := int(b[i+2])
				h := int(binary.BigEndian.Uint16(b[i+3 : i+5]))
				w := int(binary.BigEndian.Uint16(b[i+5 : i+7]))
				comps := int(b[i+7])
				res.Width = w
				res.Height = h
				res.ColorDepth = bitDepth * comps
			}
			return
		}
		i += segLen
	}
	res.Note = "no SOF marker found in 64 KiB header window"
}

func isSOF(m byte) bool {
	switch m {
	case 0xC0, 0xC1, 0xC2, 0xC3,
		0xC5, 0xC6, 0xC7,
		0xC9, 0xCA, 0xCB,
		0xCD, 0xCE, 0xCF:
		return true
	}
	return false
}

// ---- GIF ---------------------------------------------------------------

func isGIF(b []byte) bool {
	return len(b) >= 6 &&
		(string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a")
}

func parseGIF(b []byte, res *infoResult) {
	if len(b) < 13 {
		res.Note = "truncated GIF header"
		return
	}
	res.Width = int(binary.LittleEndian.Uint16(b[6:8]))
	res.Height = int(binary.LittleEndian.Uint16(b[8:10]))
	// Color depth is bits-per-pixel of the global color table.
	packed := b[10]
	bitsPerPixel := int((packed&0x07)+1) * 3 // RGB triplets
	res.ColorDepth = bitsPerPixel
	// Animated GIF detection: scan for multiple Image Descriptors (0x2C).
	// Skip global color table if present.
	cursor := 13
	if packed&0x80 != 0 {
		gctSize := 3 * (1 << ((packed & 0x07) + 1))
		cursor += gctSize
	}
	imageCount := 0
	for cursor < len(b) && imageCount < 2 {
		switch b[cursor] {
		case 0x2C: // Image Descriptor
			imageCount++
			if imageCount >= 2 {
				res.Animated = true
				return
			}
			// Skip 9-byte image descriptor header + optional LCT + image data.
			// Cheap path: just bail; we have the answer if a second Image
			// Descriptor exists, otherwise we'd need a real parser.
			cursor++
		case 0x21: // Extension Introducer
			cursor++
			if cursor >= len(b) {
				return
			}
			cursor++ // extension label
			// Sub-blocks: each prefixed by length, terminated by 0x00.
			for cursor < len(b) {
				sz := int(b[cursor])
				cursor++
				if sz == 0 {
					break
				}
				cursor += sz
			}
		case 0x3B: // Trailer
			return
		default:
			cursor++
		}
	}
}

// ---- WebP --------------------------------------------------------------
//
// File layout: "RIFF" (4) + size (4 LE) + "WEBP" (4) + chunks.
// First chunk type tells us the variant:
//   "VP8 " — lossy. Bytes 23-29 hold the 14-bit width/height pair.
//   "VP8L" — lossless. Bytes 21-25 hold packed 14-bit width/height-1.
//   "VP8X" — extended. Width-1 (24-bit LE) at offset 24, height-1 at 27.

func isWebP(b []byte) bool {
	return len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP"
}

func parseWebP(b []byte, res *infoResult) {
	if len(b) < 30 {
		res.Note = "truncated WebP header"
		return
	}
	chunk := string(b[12:16])
	switch chunk {
	case "VP8 ":
		// Frame tag at offset 20, then 0x9d012a sync, then width/height.
		if len(b) < 30 {
			return
		}
		w := int(binary.LittleEndian.Uint16(b[26:28])) & 0x3FFF
		h := int(binary.LittleEndian.Uint16(b[28:30])) & 0x3FFF
		res.Width = w
		res.Height = h
	case "VP8L":
		// Signature byte 0x2F at offset 20, then 4 bytes of packed dims.
		if len(b) < 25 {
			return
		}
		bits := binary.LittleEndian.Uint32(b[21:25])
		res.Width = int(bits&0x3FFF) + 1
		res.Height = int((bits>>14)&0x3FFF) + 1
	case "VP8X":
		// Width-1 / Height-1 stored as 24-bit LE at offsets 24 and 27.
		if len(b) < 30 {
			return
		}
		res.Width = int(uint32(b[24])|uint32(b[25])<<8|uint32(b[26])<<16) + 1
		res.Height = int(uint32(b[27])|uint32(b[28])<<8|uint32(b[29])<<16) + 1
		// VP8X flags byte at offset 20 — bit 1 = animation.
		if b[20]&0x02 != 0 {
			res.Animated = true
		}
	default:
		res.Note = "unrecognized WebP chunk: " + chunk
	}
}

// ---- BMP ---------------------------------------------------------------

func isBMP(b []byte) bool {
	return len(b) >= 2 && b[0] == 'B' && b[1] == 'M'
}

func parseBMP(b []byte, res *infoResult) {
	if len(b) < 30 {
		res.Note = "truncated BMP header"
		return
	}
	res.Width = int(binary.LittleEndian.Uint32(b[18:22]))
	// BMP heights are signed int32 (negative = top-down). We report
	// the absolute value.
	h := int32(binary.LittleEndian.Uint32(b[22:26]))
	if h < 0 {
		h = -h
	}
	res.Height = int(h)
	res.ColorDepth = int(binary.LittleEndian.Uint16(b[28:30]))
}

// ---- helpers -----------------------------------------------------------

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func writeJSON(resultPtr, resultCap int32, v any) int32 {
	payload, err := json.Marshal(v)
	if err != nil {
		return -1
	}
	if int32(len(payload)) > resultCap {
		return -1
	}
	dst := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(resultPtr))), int(resultCap))
	copy(dst, payload)
	return int32(len(payload))
}
