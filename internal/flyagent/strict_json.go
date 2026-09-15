package flyagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// rejectNonExactJSONFields closes encoding/json's case-insensitive struct-key
// fallback. Artifact schemas use exact JSON tags so differently-cased keys
// cannot alias a declared field while evading exact duplicate-key checks.
func rejectNonExactJSONFields(document []byte, destination any) error {
	if destination == nil {
		return errors.New("exact JSON destination is nil")
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	if err := scanExactJSONValue(decoder, reflect.TypeOf(destination), "$"); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func scanExactJSONValue(decoder *json.Decoder, destination reflect.Type, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	destination = indirectJSONType(destination)
	switch delimiter {
	case '{':
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			var valueType reflect.Type
			switch {
			case destination == nil || destination.Kind() == reflect.Interface:
			case destination.Kind() == reflect.Map:
				valueType = destination.Elem()
			case destination.Kind() == reflect.Struct:
				fieldType, exact, caseAlias := exactJSONField(destination, key)
				if !exact {
					if caseAlias {
						return fmt.Errorf("non-canonical JSON field %q at %s", key, path)
					}
					return fmt.Errorf("json: unknown field %q at %s", key, path)
				}
				valueType = fieldType
			default:
				return fmt.Errorf("JSON object at %s does not match %s", path, destination)
			}
			if err := scanExactJSONValue(decoder, valueType, path+"."+key); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return fmt.Errorf("unexpected JSON delimiter %q at %s", closing, path)
		}
		return nil
	case '[':
		var elementType reflect.Type
		if destination != nil && (destination.Kind() == reflect.Slice || destination.Kind() == reflect.Array) {
			elementType = destination.Elem()
		}
		index := 0
		for decoder.More() {
			if err := scanExactJSONValue(decoder, elementType, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
			index++
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return fmt.Errorf("unexpected JSON delimiter %q at %s", closing, path)
		}
		return nil
	default:
		return fmt.Errorf("unexpected JSON delimiter %q at %s", delimiter, path)
	}
}

func indirectJSONType(value reflect.Type) reflect.Type {
	for value != nil && value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	return value
}

func exactJSONField(structType reflect.Type, key string) (reflect.Type, bool, bool) {
	var caseAlias bool
	for index := 0; index < structType.NumField(); index++ {
		field := structType.Field(index)
		if field.PkgPath != "" && !field.Anonymous {
			continue
		}
		tag := field.Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name == "-" {
			continue
		}
		if field.Anonymous && name == "" {
			nestedType := indirectJSONType(field.Type)
			if nestedType == nil || nestedType.Kind() != reflect.Struct {
				continue
			}
			if nested, exact, alias := exactJSONField(nestedType, key); exact {
				return nested, true, false
			} else if alias {
				caseAlias = true
			}
			continue
		}
		if name == "" {
			name = field.Name
		}
		if key == name {
			return field.Type, true, false
		}
		caseAlias = caseAlias || strings.EqualFold(key, name)
	}
	return nil, false, caseAlias
}
