// Package cleantext finds what PostgreSQL will not store in text a caller sent:
// a NUL byte, which its text type cannot hold, and bytes that are not UTF-8.
//
// No client of ours sends either. Left alone, each service finds out one query
// at a time, from a database error it can only report as a 500 that an operator
// then reads as the server failing. Checked where the request is read, it is a
// 400 that names the caller's mistake.
package cleantext

import (
	"errors"
	"reflect"
	"strings"
	"unicode/utf8"
)

// ErrUnclean is what Check answers for text the database would refuse.
var ErrUnclean = errors.New("text holds a NUL character or bytes that are not UTF-8")

// Valid reports whether text is UTF-8 and holds no NUL.
func Valid(text string) bool {
	return utf8.ValidString(text) && !strings.ContainsRune(text, 0)
}

// Check reports whether every string in value, however deep in structs,
// pointers, slices and maps (keys included), is Valid. Byte slices are left
// alone: they are content, such as a file's, and not text to be stored.
func Check(value any) error {
	if !valid(reflect.ValueOf(value)) {
		return ErrUnclean
	}
	return nil
}

func valid(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.String:
		return Valid(value.String())
	case reflect.Pointer, reflect.Interface:
		return value.IsNil() || valid(value.Elem())
	case reflect.Struct:
		fields := value.Type()
		for index := range value.NumField() {
			// Only exported fields are ever filled by a decoder.
			if fields.Field(index).IsExported() && !valid(value.Field(index)) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return true
		}
		for index := range value.Len() {
			if !valid(value.Index(index)) {
				return false
			}
		}
	case reflect.Map:
		for iterator := value.MapRange(); iterator.Next(); {
			if !valid(iterator.Key()) || !valid(iterator.Value()) {
				return false
			}
		}
	}
	return true
}
