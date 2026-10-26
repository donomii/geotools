package wikidump

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestParserReadsDeclaredWikipediaXML(t *testing.T) {
	input := "\uFEFF" + `<?xml version="1.0" encoding="UTF-8"?>
<!-- Wikipedia export -->
<mediawiki xmlns="http://www.mediawiki.org/xml/export-0.11/">
<!-- Site information -->
<siteinfo><sitename>Wikipedia</sitename></siteinfo>
<page><title>Singapore</title><id>27318</id><revision><text>{{coord|1|17|N|103|50|E}}</text></revision></page>
<!-- Next page -->
<page><title>Eiffel Tower</title><id>9232</id><revision><text>Paris</text></revision></page>
</mediawiki><!-- Export complete -->`
	parsed, err := NewParser(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.SiteInfo().SiteName != "Wikipedia" {
		t.Fatalf("site name is %q, expected Wikipedia", parsed.SiteInfo().SiteName)
	}
	for _, title := range []string{"Singapore", "Eiffel Tower"} {
		page, err := parsed.Next()
		if err != nil {
			t.Fatal(err)
		}
		if page.Title != title || len(page.Revisions) != 1 {
			t.Fatalf("received page %#v, expected %q with one revision", page, title)
		}
	}
	if page, err := parsed.Next(); page != nil || !errors.Is(err, io.EOF) {
		t.Fatalf("received page %#v and error %v after final page, expected EOF", page, err)
	}
}

func TestParserRejectsInvalidDumpStructure(t *testing.T) {
	for _, input := range []string{
		" \uFEFF<mediawiki><siteinfo/></mediawiki>",
		"<mediawiki><siteinfo/>\uFEFF</mediawiki>",
		`<page><title>Wrong root</title></page>`,
		`<mediawiki><page><title>Missing siteinfo</title></page></mediawiki>`,
		`<mediawiki><siteinfo/>`,
		`<mediawiki><siteinfo/><page><title>Truncated page`,
		`<mediawiki><siteinfo/><unexpected/></mediawiki>`,
		`<mediawiki><siteinfo/>unexpected text</mediawiki>`,
		`<mediawiki><siteinfo/></mediawiki><mediawiki><siteinfo/></mediawiki>`,
	} {
		t.Run(input, func(t *testing.T) {
			parsed, err := NewParser(strings.NewReader(input))
			if err == nil {
				_, err = parsed.Next()
			}
			if err == nil || errors.Is(err, io.EOF) {
				t.Fatalf("invalid dump returned %v, expected an XML structure error", err)
			}
		})
	}
}
