// Package bundlepayload implements the on-disk format for plugins
// appended to a stado binary by `stado plugin bundle`.
//
// Payload layout (appended to a vanilla stado binary):
//
//	<STADO_BUNDLE_v1 magic, 16 bytes>
//	<payload-body>
//	<bundler-pubkey, 32 bytes>      ← Ed25519 pubkey of the bundling identity
//	<bundler-sig, 64 bytes>         ← Ed25519 sig over sha256(payload-body)
//	<trailer-size uint64 LE, 8B>    ← size of (payload-body+pubkey+sig)
//	<STADO_BUNDLE_END magic, 16 bytes>
//
// Per-entry inside payload-body:
//
//	<count uint32 LE> { <pubkey-len uint16> <pubkey>
//	                    <manifest-len uint32> <manifest-json>
//	                    <sig-len uint16> <sig>
//	                    <wasm-len uint32> <wasm> } * count
//
// Sorting + deterministic encoding: entries are sorted by manifest
// Name before encoding so byte-for-byte reproducibility holds for
// equivalent inputs.
package bundlepayload

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/foobarto/stado/internal/plugins"
)

const (
	magicStart = "STADO_BUNDLE_v1\n"
	magicEnd   = "STADO_BUNDLE_END"
)

var (
	ErrCorrupt           = errors.New("bundlepayload: payload structurally corrupt")
	ErrBundlerSigInvalid = errors.New("bundlepayload: bundler signature invalid")
	ErrEntrySigInvalid   = errors.New("bundlepayload: per-entry signature invalid")
)

// Entry is one user-bundled plugin: its verifying pubkey, manifest,
// signature, and wasm bytes.
type Entry struct {
	Pubkey   ed25519.PublicKey
	Manifest plugins.Manifest
	Sig      []byte
	Wasm     []byte
}

// Bundle is the parsed payload: bundler identity + verified entries.
type Bundle struct {
	BundlerPubkey ed25519.PublicKey
	Entries       []Entry
	SkipVerified  bool // true when DecodeBytes was called with skipVerify=true
}

// Encode writes the full appended payload (magic + body + bundler
// signature + trailer + end magic) to w. Entries are sorted by
// manifest Name for deterministic output.
func Encode(w io.Writer, entries []Entry, bundlerPriv ed25519.PrivateKey, bundlerPub ed25519.PublicKey) error {
	sorted := make([]Entry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Manifest.Name < sorted[j].Manifest.Name
	})

	var body bytes.Buffer
	if err := binary.Write(&body, binary.LittleEndian, uint32(len(sorted))); err != nil {
		return err
	}
	for _, e := range sorted {
		if err := writeEntry(&body, e); err != nil {
			return err
		}
	}

	digest := sha256.Sum256(body.Bytes())
	bundlerSig := ed25519.Sign(bundlerPriv, digest[:])

	if _, err := io.WriteString(w, magicStart); err != nil {
		return err
	}
	if _, err := w.Write(body.Bytes()); err != nil {
		return err
	}
	if _, err := w.Write(bundlerPub); err != nil {
		return err
	}
	if _, err := w.Write(bundlerSig); err != nil {
		return err
	}
	trailerSize := uint64(body.Len() + ed25519.PublicKeySize + ed25519.SignatureSize)
	if err := binary.Write(w, binary.LittleEndian, trailerSize); err != nil {
		return err
	}
	if _, err := io.WriteString(w, magicEnd); err != nil {
		return err
	}
	return nil
}

// writeEntry writes one entry's length-prefixed fields.
func writeEntry(w io.Writer, e Entry) error {
	manifestJSON, err := json.Marshal(e.Manifest)
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if err := writeLenPrefixedU16(w, e.Pubkey); err != nil {
		return err
	}
	if err := writeLenPrefixedU32(w, manifestJSON); err != nil {
		return err
	}
	if err := writeLenPrefixedU16(w, e.Sig); err != nil {
		return err
	}
	return writeLenPrefixedU32(w, e.Wasm)
}

func writeLenPrefixedU16(w io.Writer, b []byte) error {
	if len(b) > 0xFFFF {
		return fmt.Errorf("field too large for uint16 length: %d", len(b))
	}
	if err := binary.Write(w, binary.LittleEndian, uint16(len(b))); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

func writeLenPrefixedU32(w io.Writer, b []byte) error {
	if uint64(len(b)) > 0xFFFFFFFF {
		return fmt.Errorf("field too large for uint32 length: %d", len(b))
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(len(b))); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

// DecodeBytes parses raw bytes (e.g. the tail of a binary file) and
// returns the verified Bundle. Returns (zero, nil) when no bundle
// magic is present (vanilla input). Returns an error when a bundle
// IS present but malformed or signature-invalid (unless skipVerify
// is true, in which case signature checks are bypassed but
// structural validation still applies).
func DecodeBytes(raw []byte, skipVerify bool) (Bundle, error) {
	const trailerLen = 8 + len(magicEnd)
	if len(raw) < trailerLen+len(magicStart)+ed25519.PublicKeySize+ed25519.SignatureSize {
		// Too short to possibly contain a bundle; vanilla.
		return Bundle{}, nil
	}
	if string(raw[len(raw)-len(magicEnd):]) != magicEnd {
		return Bundle{}, nil // vanilla
	}
	sizeOff := len(raw) - len(magicEnd) - 8
	trailerSize := binary.LittleEndian.Uint64(raw[sizeOff : sizeOff+8])
	if trailerSize > uint64(len(raw)) {
		return Bundle{}, fmt.Errorf("%w: trailer size out of range", ErrCorrupt)
	}
	startOff := uint64(sizeOff) - trailerSize - uint64(len(magicStart))
	if int64(startOff) < 0 || startOff > uint64(len(raw)) {
		return Bundle{}, fmt.Errorf("%w: start offset out of range", ErrCorrupt)
	}
	if string(raw[startOff:startOff+uint64(len(magicStart))]) != magicStart {
		return Bundle{}, fmt.Errorf("%w: leading magic missing", ErrCorrupt)
	}
	bodyAndKeys := raw[startOff+uint64(len(magicStart)) : sizeOff]
	if len(bodyAndKeys) < ed25519.PublicKeySize+ed25519.SignatureSize {
		return Bundle{}, fmt.Errorf("%w: trailer too small for pubkey+sig", ErrCorrupt)
	}
	bodyEnd := len(bodyAndKeys) - ed25519.PublicKeySize - ed25519.SignatureSize
	body := bodyAndKeys[:bodyEnd]
	bundlerPub := ed25519.PublicKey(bodyAndKeys[bodyEnd : bodyEnd+ed25519.PublicKeySize])
	bundlerSig := bodyAndKeys[bodyEnd+ed25519.PublicKeySize:]

	if !skipVerify {
		digest := sha256.Sum256(body)
		if !ed25519.Verify(bundlerPub, digest[:], bundlerSig) {
			return Bundle{}, ErrBundlerSigInvalid
		}
	}

	entries, err := decodeEntries(body)
	if err != nil {
		return Bundle{}, err
	}

	if !skipVerify {
		for i, e := range entries {
			canon, err := e.Manifest.Canonical()
			if err != nil {
				return Bundle{}, fmt.Errorf("entry %d: canonicalize: %w", i, err)
			}
			// Plugin signatures are over the canonical manifest bytes
			// only — the manifest's WASMSHA256 field is the binding to
			// the wasm. Verify both: (1) author sig over canonical
			// manifest, (2) wasm sha matches the declared digest.
			if !ed25519.Verify(e.Pubkey, canon, e.Sig) {
				return Bundle{}, fmt.Errorf("entry %d (%s): %w", i, e.Manifest.Name, ErrEntrySigInvalid)
			}
			wasmHash := sha256.Sum256(e.Wasm)
			wasmHashHex := hex.EncodeToString(wasmHash[:])
			if e.Manifest.WASMSHA256 != "" && e.Manifest.WASMSHA256 != wasmHashHex {
				return Bundle{}, fmt.Errorf("entry %d (%s): %w: wasm sha256 mismatch (manifest %s, actual %s)",
					i, e.Manifest.Name, ErrEntrySigInvalid, e.Manifest.WASMSHA256, wasmHashHex)
			}
		}
	}

	return Bundle{
		BundlerPubkey: bundlerPub,
		Entries:       entries,
		SkipVerified:  skipVerify,
	}, nil
}

func decodeEntries(body []byte) ([]Entry, error) {
	r := bytes.NewReader(body)
	var count uint32
	if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
		return nil, fmt.Errorf("%w: entry count", ErrCorrupt)
	}
	entries := make([]Entry, 0, count)
	for i := uint32(0); i < count; i++ {
		e, err := readEntry(r)
		if err != nil {
			return nil, fmt.Errorf("entry %d: %w", i, err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func readEntry(r *bytes.Reader) (Entry, error) {
	pub, err := readLenPrefixedU16(r)
	if err != nil {
		return Entry{}, err
	}
	if len(pub) != ed25519.PublicKeySize {
		return Entry{}, fmt.Errorf("%w: pubkey length %d", ErrCorrupt, len(pub))
	}
	manifestBytes, err := readLenPrefixedU32(r)
	if err != nil {
		return Entry{}, err
	}
	var mf plugins.Manifest
	if err := json.Unmarshal(manifestBytes, &mf); err != nil {
		return Entry{}, fmt.Errorf("%w: manifest json: %v", ErrCorrupt, err)
	}
	sig, err := readLenPrefixedU16(r)
	if err != nil {
		return Entry{}, err
	}
	if len(sig) != ed25519.SignatureSize {
		return Entry{}, fmt.Errorf("%w: sig length %d", ErrCorrupt, len(sig))
	}
	wasm, err := readLenPrefixedU32(r)
	if err != nil {
		return Entry{}, err
	}
	return Entry{Pubkey: ed25519.PublicKey(pub), Manifest: mf, Sig: sig, Wasm: wasm}, nil
}

func readLenPrefixedU16(r *bytes.Reader) ([]byte, error) {
	var n uint16
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return nil, fmt.Errorf("%w: u16 length", ErrCorrupt)
	}
	out := make([]byte, n)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, fmt.Errorf("%w: u16 body", ErrCorrupt)
	}
	return out, nil
}

func readLenPrefixedU32(r *bytes.Reader) ([]byte, error) {
	var n uint32
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return nil, fmt.Errorf("%w: u32 length", ErrCorrupt)
	}
	if uint64(n) > uint64(r.Len()+1) {
		return nil, fmt.Errorf("%w: u32 length too large", ErrCorrupt)
	}
	out := make([]byte, n)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, fmt.Errorf("%w: u32 body", ErrCorrupt)
	}
	return out, nil
}
