package wikidump

import (
	"bufio"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/dustin/go-wikiparse"
)

type parser struct {
	decoder  *xml.Decoder
	siteInfo wikiparse.SiteInfo
}

func NewParser(input io.Reader) (wikiparse.Parser, error) {
	buffered := bufio.NewReader(input)
	prefix, err := buffered.Peek(3)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("failed to read Wikipedia XML prefix: %w", err)
	}
	if string(prefix) == "\uFEFF" {
		if _, err := buffered.Discard(3); err != nil {
			return nil, fmt.Errorf("failed to consume Wikipedia XML byte-order mark: %w", err)
		}
	}
	decoder := xml.NewDecoder(buffered)
	root, err := nextMarkup(decoder)
	if err != nil {
		return nil, fmt.Errorf("failed to read Wikipedia XML root: %w", err)
	}
	start, ok := root.(xml.StartElement)
	if !ok || start.Name.Local != "mediawiki" {
		return nil, fmt.Errorf("expected mediawiki root element, received %v", root)
	}
	header, err := nextMarkup(decoder)
	if err != nil {
		return nil, fmt.Errorf("failed to read Wikipedia siteinfo element: %w", err)
	}
	start, ok = header.(xml.StartElement)
	if !ok || start.Name.Local != "siteinfo" {
		return nil, fmt.Errorf("expected siteinfo before Wikipedia pages, received %v", header)
	}
	parsed := &parser{decoder: decoder}
	if err := decoder.DecodeElement(&parsed.siteInfo, &start); err != nil {
		return nil, fmt.Errorf("failed to decode Wikipedia siteinfo: %w", err)
	}
	return parsed, nil
}

func (parsed *parser) SiteInfo() wikiparse.SiteInfo {
	return parsed.siteInfo
}

func (parsed *parser) Next() (*wikiparse.Page, error) {
	token, err := nextMarkup(parsed.decoder)
	if err != nil {
		return nil, err
	}
	if end, ok := token.(xml.EndElement); ok && end.Name.Local == "mediawiki" {
		trailing, err := nextMarkup(parsed.decoder)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("unexpected XML after mediawiki root: %v", trailing)
	}
	start, ok := token.(xml.StartElement)
	if !ok || start.Name.Local != "page" {
		return nil, fmt.Errorf("expected Wikipedia page element, received %v", token)
	}
	page := &wikiparse.Page{}
	if err := parsed.decoder.DecodeElement(page, &start); err != nil {
		return nil, fmt.Errorf("failed to decode Wikipedia page: %w", err)
	}
	return page, nil
}

func nextMarkup(decoder *xml.Decoder) (xml.Token, error) {
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		switch value := token.(type) {
		case xml.StartElement, xml.EndElement:
			return token, nil
		case xml.CharData:
			if strings.TrimSpace(string(value)) != "" {
				return nil, fmt.Errorf("expected Wikipedia XML element, received text %q", string(value))
			}
		}
	}
}
