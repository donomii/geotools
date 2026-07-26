# wikipedia2doc2vec

`wikipedia2doc2vec` converts local Wikipedia XML dumps into line-oriented text suitable for document-vector training and other text-processing pipelines. It makes no network requests.

Each output record has the page ID, a tab, and normalized tokens:

```text
12345	example article text
```

## Build

From the repository root:

```sh
./build_all.sh
```

The binary is written to `bin/wikipedia2doc2vec`. To build only this command:

```sh
mkdir -p bin
go -C wikipedia2doc2vec build -o ../bin/wikipedia2doc2vec .
```

## Usage

Plain XML, gzip, and bzip2 inputs are detected from the filename:

```sh
./bin/wikipedia2doc2vec enwiki-pages.xml.bz2 > articles.txt
./bin/wikipedia2doc2vec -limit=10000 enwiki-pages.xml.gz > sample.txt
```

Use `-` to read a plain or explicitly compressed stream from standard input:

```sh
./bin/wikipedia2doc2vec -compression=bz2 - < enwiki-pages.xml.bz2 > articles.txt
```

Wikipedia multistream dumps can be processed from their local index and data files:

```sh
./bin/wikipedia2doc2vec enwiki-index.txt.bz2 enwiki-pages-multistream.xml.bz2 > articles.txt
```

## Text normalization

The converter examines only the first revision present for each page. It lowercases text, decodes HTML entities, removes comments, references, templates, tables, URLs, and HTML tags, and removes wiki-link and external-link syntax and destinations while retaining visible link labels. It then retains Unicode letter and number tokens. Output remains in source-page order even when multiple parsing workers are used.

## Options

| Option | Meaning |
| --- | --- |
| `-compression=''` | Detect plain, gzip, or bzip2 input from the filename. This is the default. Use `gz` or `bz2` for compressed standard input. |
| `-cpus=N` | Set the number of Go execution threads. The default is the runtime CPU count. |
| `-workers=8` | Set concurrent page parsing workers. Accepted values are 1 through 1000. |
| `-limit=-1` | Stop after this many pages. `-1` processes the complete input and `0` emits no pages. |
| `-help` | Print input examples and exit. |
| `-test` | Run the command's built-in checks and exit. |

Pages without revisions are reported on standard error and recorded in `errors.gob`; that file is created when the first page-level error is encountered. XML parser failures abort conversion and are not added to that file.

Processing is streaming and ordered. The parser and workers use bounded queues, so memory use does not grow with the number of pages in a dump.

## Tests

From the repository root:

```sh
go -C wikipedia2doc2vec test ./...
./bin/wikipedia2doc2vec -test
```
