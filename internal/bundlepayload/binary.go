package bundlepayload

import (
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// AppendToBinary copies srcPath to dstPath (preserving execute
// permission), then appends a signed bundle. Refuses when srcPath
// resolves to dstPath.
func AppendToBinary(srcPath, dstPath string, entries []Entry, bundlerPriv ed25519.PrivateKey, bundlerPub ed25519.PublicKey) error {
	srcAbs, err := filepath.Abs(srcPath)
	if err != nil {
		return err
	}
	dstAbs, err := filepath.Abs(dstPath)
	if err != nil {
		return err
	}
	if srcAbs == dstAbs {
		return errors.New("bundlepayload: source and destination paths are the same; refusing to overwrite running binary")
	}

	if err := copyFile(srcAbs, dstAbs); err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	f, err := os.OpenFile(dstAbs, os.O_WRONLY|os.O_APPEND, 0o755)
	if err != nil {
		return fmt.Errorf("open dst: %w", err)
	}
	defer f.Close()

	return Encode(f, entries, bundlerPriv, bundlerPub)
}

// LoadFromFile opens path, reads only the trailing tail needed to
// resolve the bundle (cheap for very large binaries), and returns
// the parsed Bundle. Returns (zero, nil) for vanilla binaries.
func LoadFromFile(path string, skipVerify bool) (Bundle, error) {
	f, err := os.Open(path)
	if err != nil {
		return Bundle{}, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return Bundle{}, err
	}
	const trailerLen = 8 + 16 // size field + STADO_BUNDLE_END magic
	if st.Size() < int64(trailerLen) {
		return Bundle{}, nil
	}
	tail := make([]byte, trailerLen)
	if _, err := f.ReadAt(tail, st.Size()-int64(trailerLen)); err != nil {
		return Bundle{}, err
	}
	if string(tail[8:]) != magicEnd {
		return Bundle{}, nil // vanilla
	}
	trailerSize := binary.LittleEndian.Uint64(tail[:8])
	totalBundleLen := int64(trailerSize) + int64(len(magicStart)) + int64(trailerLen)
	if totalBundleLen > st.Size() {
		return Bundle{}, fmt.Errorf("%w: bundle larger than file", ErrCorrupt)
	}
	bundleBytes := make([]byte, totalBundleLen)
	if _, err := f.ReadAt(bundleBytes, st.Size()-totalBundleLen); err != nil {
		return Bundle{}, err
	}
	return DecodeBytes(bundleBytes, skipVerify)
}

// StripFromBinary copies srcPath to dstPath, truncated at the
// bundle's start magic. If srcPath has no bundle, dst is a byte-
// identical copy. Refuses when src == dst.
func StripFromBinary(srcPath, dstPath string) error {
	srcAbs, _ := filepath.Abs(srcPath)
	dstAbs, _ := filepath.Abs(dstPath)
	if srcAbs == dstAbs {
		return errors.New("bundlepayload: source and destination paths are the same")
	}
	src, err := os.Open(srcAbs)
	if err != nil {
		return err
	}
	defer src.Close()

	st, err := src.Stat()
	if err != nil {
		return err
	}

	stripAt := st.Size()
	const trailerLen = 8 + 16
	if st.Size() >= int64(trailerLen) {
		tail := make([]byte, trailerLen)
		if _, err := src.ReadAt(tail, st.Size()-int64(trailerLen)); err == nil {
			if string(tail[8:]) == magicEnd {
				trailerSize := binary.LittleEndian.Uint64(tail[:8])
				stripAt = st.Size() - int64(trailerSize) - int64(len(magicStart)) - int64(trailerLen)
				if stripAt < 0 {
					return fmt.Errorf("%w: strip offset negative", ErrCorrupt)
				}
			}
		}
	}

	dst, err := os.OpenFile(dstAbs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer dst.Close()

	if _, err := io.CopyN(dst, src, stripAt); err != nil {
		return err
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
