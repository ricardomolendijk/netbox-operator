package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
)

// gzipMagic is the two bytes every gzip member starts with.
var gzipMagic = []byte{0x1f, 0x8b}

// maybeGunzip decompresses the IR when it is gzipped and passes it through when it is not.
// Both forms exist on purpose: hack/testdata holds the committed IR gzipped, and a regeneration
// run against a fresh NetBox checkout produces plain JSON on a pipe.
func maybeGunzip(raw []byte) ([]byte, error) {
	if !bytes.HasPrefix(raw, gzipMagic) {
		return raw, nil
	}

	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("opening the gzip stream: %w", err)
	}

	defer func() { _ = reader.Close() }()

	out, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("reading the gzip stream: %w", err)
	}

	return out, nil
}
