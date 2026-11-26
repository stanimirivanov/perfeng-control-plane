package jsondocument

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// SafeScalars checks Unicode escapes and bounds numeric expansion before JSON
// Schema arithmetic. Call Valid first to establish well-formed, bounded JSON.
func SafeScalars(raw []byte) bool {
	return scalarUnicode(raw) && scalarNumbers(raw)
}

// Valid JSON is already established. Reject escaped unpaired surrogates that
// Go would replace, preserving exact identity claims across implementations.
func scalarUnicode(raw []byte) bool {
	for index := 0; index < len(raw); index++ {
		if raw[index] != '\\' {
			continue
		}
		index++
		if raw[index] != 'u' {
			continue
		}
		code, err := strconv.ParseUint(string(raw[index+1:index+5]), 16, 16)
		if err != nil {
			return false
		}
		if utf16.IsSurrogate(rune(code)) {
			if index+11 > len(raw) || string(raw[index+5:index+7]) != `\u` {
				return false
			}
			low, err := strconv.ParseUint(string(raw[index+7:index+11]), 16, 16)
			if err != nil || utf16.DecodeRune(rune(code), rune(low)) == utf8.RuneError {
				return false
			}
			index += 6
		}
		index += 4
	}

	return true
}

// Bound rational expansion before the schema validator performs arithmetic.
func scalarNumbers(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	for {
		token, err := decoder.Token()
		if err != nil {
			return errors.Is(err, io.EOF)
		}
		if number, ok := token.(json.Number); ok {
			if len(number) > 128 || !boundedExponent(number) {
				return false
			}
		}
	}
}

func boundedExponent(number json.Number) bool {
	index := strings.IndexAny(string(number), "eE")
	if index < 0 {
		return true
	}
	exponent, err := strconv.Atoi(string(number[index+1:]))
	return err == nil && exponent >= -999 && exponent <= 999
}
