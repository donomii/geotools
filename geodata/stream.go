package geodata

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type InputMode string

const (
	InputAuto     InputMode = "auto"
	InputSequence InputMode = "seq"
)

type OutputMode string

const (
	OutputJSONL      OutputMode = "jsonl"
	OutputCollection OutputMode = "collection"
	OutputSequence   OutputMode = "seq"
)

type FeatureVisitor func(Feature) error

func ParseInputMode(value string) (InputMode, error) {
	switch InputMode(value) {
	case InputAuto, InputSequence:
		return InputMode(value), nil
	default:
		return "", fmt.Errorf("invalid input mode %q; expected auto or seq", value)
	}
}

func ParseOutputMode(value string) (OutputMode, error) {
	switch OutputMode(value) {
	case OutputJSONL, OutputCollection, OutputSequence:
		return OutputMode(value), nil
	default:
		return "", fmt.Errorf("invalid output mode %q; expected jsonl, collection, or seq", value)
	}
}

func ReadFeatures(input io.Reader, mode InputMode, visit FeatureVisitor) error {
	reader := bufio.NewReader(input)
	if mode == InputSequence {
		return readSequence(reader, visit)
	}
	first, err := discardLeadingWhitespace(reader)
	if err == io.EOF {
		return fmt.Errorf("GeoJSON input is empty")
	}
	if err != nil {
		return err
	}
	if first == 0x1e {
		return readSequence(reader, visit)
	}
	return readJSONValues(reader, visit)
}

func discardLeadingWhitespace(reader *bufio.Reader) (byte, error) {
	for {
		value, err := reader.Peek(1)
		if err != nil {
			return 0, err
		}
		switch value[0] {
		case ' ', '\t', '\r', '\n':
			if _, err := reader.Discard(1); err != nil {
				return 0, err
			}
		default:
			return value[0], nil
		}
	}
}

func readSequence(reader *bufio.Reader, visit FeatureVisitor) error {
	first, err := reader.ReadByte()
	if err != nil {
		return fmt.Errorf("failed to read GeoJSON sequence: %w", err)
	}
	if first != 0x1e {
		return fmt.Errorf("GeoJSON sequence starts with byte 0x%02x; expected record separator 0x1e", first)
	}
	recordNumber := 0
	var record bytes.Buffer
	for {
		part, readErr := reader.ReadBytes(0x1e)
		if len(part) > 0 {
			if part[len(part)-1] == 0x1e {
				part = part[:len(part)-1]
			}
			record.Write(part)
		}
		trimmed := bytes.TrimSpace(record.Bytes())
		if len(trimmed) > 0 {
			recordNumber++
			if err := readJSONValuesLimited(bytes.NewReader(trimmed), 1, visit); err != nil {
				return fmt.Errorf("GeoJSON sequence record %d is invalid: %w", recordNumber, err)
			}
		}
		record.Reset()
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("failed to read GeoJSON sequence record %d: %w", recordNumber+1, readErr)
		}
	}
}

func readJSONValues(input io.Reader, visit FeatureVisitor) error {
	return readJSONValuesLimited(input, 0, visit)
}

func readJSONValuesLimited(input io.Reader, maximumValues int, visit FeatureVisitor) error {
	decoder := json.NewDecoder(input)
	valueNumber := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			if valueNumber == 0 {
				return fmt.Errorf("GeoJSON input is empty")
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to decode top-level GeoJSON value %d: %w", valueNumber+1, err)
		}
		if maximumValues > 0 && valueNumber >= maximumValues {
			return fmt.Errorf("contains more than %d top-level JSON value", maximumValues)
		}
		valueNumber++
		if err := visitJSONToken(decoder, token, visit); err != nil {
			return fmt.Errorf("top-level GeoJSON value %d is invalid: %w", valueNumber, err)
		}
	}
}

func visitJSONToken(decoder *json.Decoder, token json.Token, visit FeatureVisitor) error {
	delimiter, ok := token.(json.Delim)
	if !ok {
		return fmt.Errorf("expected a GeoJSON object or array, received %v", token)
	}
	switch delimiter {
	case '[':
		for decoder.More() {
			elementToken, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := visitJSONToken(decoder, elementToken, visit); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return fmt.Errorf("GeoJSON array ended with %v instead of ]", end)
		}
		return nil
	case '{':
		return visitJSONObject(decoder, visit)
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
}

func visitJSONObject(decoder *json.Decoder, visit FeatureVisitor) error {
	members := make(map[string]json.RawMessage)
	featuresSeen := false
	featuresStreamed := false
	var deferredFeatures json.RawMessage
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("GeoJSON object key has type %T; expected string", keyToken)
		}
		if key == "features" && rawObjectType(members["type"]) == "FeatureCollection" {
			featuresSeen = true
			featuresStreamed = true
			if err := visitFeatureArray(decoder, visit); err != nil {
				return err
			}
		} else {
			var value json.RawMessage
			if err := decoder.Decode(&value); err != nil {
				return fmt.Errorf("failed to decode GeoJSON member %q: %w", key, err)
			}
			if key == "features" {
				featuresSeen = true
				deferredFeatures = value
			} else {
				members[key] = value
			}
		}
	}
	end, err := decoder.Token()
	if err != nil {
		return err
	}
	if end != json.Delim('}') {
		return fmt.Errorf("GeoJSON object ended with %v instead of }", end)
	}
	typeValue, exists := members["type"]
	if !exists {
		return fmt.Errorf("GeoJSON object is missing type")
	}
	var objectType string
	if err := json.Unmarshal(typeValue, &objectType); err != nil {
		return fmt.Errorf("GeoJSON type must be a string: %w", err)
	}
	if objectType == "FeatureCollection" {
		if !featuresSeen {
			return fmt.Errorf("FeatureCollection is missing features")
		}
		if bbox := members["bbox"]; bbox != nil {
			if _, err := validateBBox(bbox); err != nil {
				return fmt.Errorf("FeatureCollection bbox is invalid: %w", err)
			}
		}
		if !featuresStreamed {
			if err := visitDeferredFeatureArray(deferredFeatures, visit); err != nil {
				return err
			}
		}
		return nil
	}
	if featuresSeen {
		members["features"] = deferredFeatures
	}
	encoded, err := json.Marshal(members)
	if err != nil {
		return err
	}
	var feature Feature
	if err := json.Unmarshal(encoded, &feature); err != nil {
		return err
	}
	if feature.Type != "Feature" {
		return fmt.Errorf("unsupported GeoJSON object type %q; expected Feature or FeatureCollection", feature.Type)
	}
	return visit(feature)
}

func rawObjectType(raw json.RawMessage) string {
	var objectType string
	_ = json.Unmarshal(raw, &objectType)
	return objectType
}

func visitDeferredFeatureArray(raw json.RawMessage, visit FeatureVisitor) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := visitFeatureArray(decoder, visit); err != nil {
		return err
	}
	if decoder.More() {
		return fmt.Errorf("FeatureCollection features contains trailing JSON data")
	}
	return nil
}

func visitFeatureArray(decoder *json.Decoder, visit FeatureVisitor) error {
	start, err := decoder.Token()
	if err != nil {
		return err
	}
	if start != json.Delim('[') {
		return fmt.Errorf("FeatureCollection features is %v; expected an array", start)
	}
	for decoder.More() {
		var feature Feature
		if err := decoder.Decode(&feature); err != nil {
			return fmt.Errorf("failed to decode FeatureCollection Feature: %w", err)
		}
		if feature.Type != "Feature" {
			return fmt.Errorf("FeatureCollection contains object type %q", feature.Type)
		}
		if err := visit(feature); err != nil {
			return err
		}
	}
	end, err := decoder.Token()
	if err != nil {
		return err
	}
	if end != json.Delim(']') {
		return fmt.Errorf("FeatureCollection features ended with %v instead of ]", end)
	}
	return nil
}

type FeatureWriter struct {
	writer       *bufio.Writer
	mode         OutputMode
	wrote        bool
	closed       bool
	featureCount int64
}

func NewFeatureWriter(output io.Writer, mode OutputMode) *FeatureWriter {
	return &FeatureWriter{writer: bufio.NewWriter(output), mode: mode}
}

func (writer *FeatureWriter) Start() error {
	if writer.mode == OutputCollection {
		_, err := writer.writer.WriteString(`{"type":"FeatureCollection","features":[`)
		return err
	}
	return nil
}

func (writer *FeatureWriter) Write(feature Feature) error {
	if writer.closed {
		return fmt.Errorf("cannot write Feature %s after output was closed", feature.EncodedID())
	}
	encoded, err := json.Marshal(feature)
	if err != nil {
		return fmt.Errorf("failed to encode Feature %s: %w", feature.EncodedID(), err)
	}
	switch writer.mode {
	case OutputCollection:
		if writer.wrote {
			if err := writer.writer.WriteByte(','); err != nil {
				return err
			}
		}
	case OutputSequence:
		if err := writer.writer.WriteByte(0x1e); err != nil {
			return err
		}
	}
	if _, err := writer.writer.Write(encoded); err != nil {
		return err
	}
	if writer.mode != OutputCollection {
		if err := writer.writer.WriteByte('\n'); err != nil {
			return err
		}
	}
	writer.wrote = true
	writer.featureCount++
	return nil
}

func (writer *FeatureWriter) Close() error {
	if writer.closed {
		return fmt.Errorf("GeoJSON writer was closed more than once")
	}
	writer.closed = true
	var closeErr error
	if writer.mode == OutputCollection {
		_, closeErr = writer.writer.WriteString("]}\n")
	}
	return errors.Join(closeErr, writer.writer.Flush())
}

func (writer *FeatureWriter) FeatureCount() int64 {
	return writer.featureCount
}
