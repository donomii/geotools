package wikidump

import (
	"bytes"
	"compress/bzip2"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/dustin/go-wikiparse"
)

// Upstream's private initializer needs a header without a preamble. This is compressed <mediawiki><siteinfo/></mediawiki>;
// the wrapper supplies the actual SiteInfo separately, and workers always reopen the original data at indexed offsets.
const indexedBootstrap = "QlpoOTFBWSZTWWotmuAAAAGZgAAAgAUnK4yAIAAhKjINGT1CmAAJZ7PAbOnqokRpQclkzI+LuSKcKEg1Fs1wAA=="

type indexedSource struct {
	indexPath  string
	dataPath   string
	header     []byte
	headerOnce sync.Once
}

type indexedHeaderReader struct {
	*bytes.Reader
}

func (reader indexedHeaderReader) Close() error {
	return nil
}

func (source *indexedSource) OpenIndex() (io.ReadCloser, error) {
	return os.Open(source.indexPath)
}

func (source *indexedSource) OpenData() (wikiparse.ReadSeekCloser, error) {
	readHeader := false
	source.headerOnce.Do(func() { readHeader = true })
	if readHeader {
		return indexedHeaderReader{bytes.NewReader(source.header)}, nil
	}
	return os.Open(source.dataPath)
}

type indexedParser struct {
	wikiparse.Parser
	siteInfo wikiparse.SiteInfo
}

func (parsed *indexedParser) SiteInfo() wikiparse.SiteInfo {
	return parsed.siteInfo
}

func NewIndexedParser(indexPath, dataPath string, workers int) (wikiparse.Parser, error) {
	if workers < 1 {
		return nil, fmt.Errorf("Wikipedia indexed parser requires at least one worker, received %d", workers)
	}
	siteInfo, err := readIndexedSiteInfo(dataPath)
	if err != nil {
		return nil, err
	}
	header, err := base64.StdEncoding.DecodeString(indexedBootstrap)
	if err != nil {
		return nil, fmt.Errorf("invalid indexed-parser bootstrap header: %w", err)
	}
	source := &indexedSource{indexPath: indexPath, dataPath: dataPath, header: header}
	parsed, err := wikiparse.NewIndexedParserFromSrc(source, workers)
	if err != nil {
		return nil, err
	}
	return &indexedParser{Parser: parsed, siteInfo: siteInfo}, nil
}

func readIndexedSiteInfo(dataPath string) (wikiparse.SiteInfo, error) {
	input, err := os.Open(dataPath)
	if err != nil {
		return wikiparse.SiteInfo{}, fmt.Errorf("failed to open Wikipedia multistream header %q: %w", dataPath, err)
	}
	parsed, parseErr := NewParser(bzip2.NewReader(input))
	if err := errors.Join(parseErr, input.Close()); err != nil {
		return wikiparse.SiteInfo{}, fmt.Errorf("failed to read Wikipedia multistream header %q: %w", dataPath, err)
	}
	return parsed.SiteInfo(), nil
}
