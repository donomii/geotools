package wikidump

import (
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

const declaredMultistreamData = "QlpoOTFBWSZTWcvUm7oAAE1fiQAQUAPgV4EABoAv793AAAiAAKAAaEUeUGmmmmIGjTQ0aANNU0MQCG00Ag0/UmiAJ2lSToEOVpoiYlHWFbmMtMRQW328ggeLet0iIqdqyiFIILUdwLiMwNli4KalHCbD5YGQmIFyWJkIacpfQjU3UKxsYse958XwrJkaA7nHDsC7Uy1ddev4u5IpwoSGXqTd0EJaaDkxQVkmU1mBZyD5AAAM34AAEAAA+sUCAQgALqXdTiAAcigTEyYCYAAlTT1UxmqaA9RtTamnlHeBlK5Nsnmn0OeZikdWhGleElETmViuNzgX1miSHsbVt+LmgXNYXj2lN+QlmYEmZFA8ecb7azrHOi7kinChIQLOQfJCWmg5MUFZJlNZ7H+oygAAEF+AABBAAf/lAgEEAC+l3c4gAGoiZCA09QANAaG1Bqnqek0aaAAAHqHqK3yV2KB0TYRjSZKI5WUiIJ2Rip7+fFrWQLY4L4AkA9MSJFYHg2oM9efQokFhBQ4LCrpY2GbR8DMiMGg0Quy24SO6NTvH8XckU4UJDsf6jKBCWmg5MUFZJlNZgxeMIwAAAJmAAACABSYqAIAgACIAPSEAMInW0BYLxdyRThQkIMXjCMA="
const declaredMultistreamIndex = "QlpoOTFBWSZTWaupCegAAAHfgAAQQAA58AIADAAjpdCAIAAxQaNGgyA0IpmRqZlMjyGpzLiaKIpdEmspnWpUIFbxolrbO5RvhQH4u5IpwoSFXUhPQA=="

func TestIndexedParserPreservesDeclaredHeaderAndPageOffsets(t *testing.T) {
	directory := t.TempDir()
	dataPath := writeIndexedFixture(t, directory, "pages.xml.bz2", declaredMultistreamData)
	indexPath := writeIndexedFixture(t, directory, "index.txt.bz2", declaredMultistreamIndex)
	parsed, err := NewIndexedParser(indexPath, dataPath, 2)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.SiteInfo().SiteName != "Wikipedia" {
		t.Fatalf("indexed parser site name is %q, expected Wikipedia", parsed.SiteInfo().SiteName)
	}
	expected := map[uint64]string{27318: "Singapore", 9232: "Eiffel Tower"}
	for {
		page, err := parsed.Next()
		if errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if page.Title != expected[page.ID] || len(page.Revisions) != 1 {
			t.Fatalf("unexpected page from indexed stream: %#v", page)
		}
		delete(expected, page.ID)
	}
	if len(expected) != 0 {
		t.Fatalf("indexed parser omitted pages: %v", expected)
	}
}

func TestIndexedSiteInfoDoesNotRequirePagePayload(t *testing.T) {
	data, err := base64.StdEncoding.DecodeString(declaredMultistreamData)
	if err != nil {
		t.Fatal(err)
	}
	headerPath := filepath.Join(t.TempDir(), "header.xml.bz2")
	// The index's first page starts at byte 163; this file contains only the preceding XML header stream.
	if err := os.WriteFile(headerPath, data[:163], 0600); err != nil {
		t.Fatal(err)
	}
	siteInfo, err := readIndexedSiteInfo(headerPath)
	if err != nil {
		t.Fatal(err)
	}
	if siteInfo.SiteName != "Wikipedia" {
		t.Fatalf("header-only stream site name is %q, expected Wikipedia", siteInfo.SiteName)
	}
}

func writeIndexedFixture(t *testing.T, directory, name, encoded string) string {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
