package catalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

const maxSafeInteger = 1<<53 - 1

func manifestDigest(descriptor Descriptor) (string, error) {
	canonical, err := Canonicalize(descriptor)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// Canonicalize encodes a value as RFC 8785 JSON. Descriptors contain only
// integers, so non-integer numbers are rejected rather than formatted.
func Canonicalize(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var tree any
	if err := decoder.Decode(&tree); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if err := writeCanonical(&output, tree); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func writeCanonical(output *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		output.WriteString("null")
	case bool:
		output.WriteString(strconv.FormatBool(typed))
	case string:
		return writeCanonicalString(output, typed)
	case json.Number:
		integer, err := strconv.ParseInt(typed.String(), 10, 64)
		if err != nil || integer > maxSafeInteger || integer < -maxSafeInteger {
			return fmt.Errorf("number %s cannot be canonicalized", typed)
		}
		output.WriteString(strconv.FormatInt(integer, 10))
	case []any:
		output.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := writeCanonical(output, item); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case map[string]any:
		return writeCanonicalObject(output, typed)
	default:
		return fmt.Errorf("unsupported JSON value %T", value)
	}
	return nil
}

func writeCanonicalObject(output *bytes.Buffer, object map[string]any) error {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b string) int {
		return slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b)))
	})
	output.WriteByte('{')
	for index, key := range keys {
		if index > 0 {
			output.WriteByte(',')
		}
		if err := writeCanonicalString(output, key); err != nil {
			return err
		}
		output.WriteByte(':')
		if err := writeCanonical(output, object[key]); err != nil {
			return err
		}
	}
	output.WriteByte('}')
	return nil
}

func writeCanonicalString(output *bytes.Buffer, value string) error {
	if !utf8.ValidString(value) {
		return errors.New("string is not valid UTF-8")
	}
	output.WriteByte('"')
	for _, char := range value {
		switch char {
		case '"':
			output.WriteString(`\"`)
		case '\\':
			output.WriteString(`\\`)
		case '\b':
			output.WriteString(`\b`)
		case '\f':
			output.WriteString(`\f`)
		case '\n':
			output.WriteString(`\n`)
		case '\r':
			output.WriteString(`\r`)
		case '\t':
			output.WriteString(`\t`)
		default:
			if char < 0x20 {
				fmt.Fprintf(output, `\u%04x`, char)
			} else {
				output.WriteRune(char)
			}
		}
	}
	output.WriteByte('"')
	return nil
}
