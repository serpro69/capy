package platform

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
)

// Reject duplicate keys instead of letting a last-wins decoder hide a retained
// binding. UseNumber also preserves unknown metadata exactly during hook merges.
func decodeCodexJSON(data []byte) (map[string]any, error) {
	root, _, err := decodeOrderedJSON(data, true)
	return root, err
}

// jsonKeyOrder records each decoded object's source key order so setup rewrites
// only what it edits: other tools (e.g. jq-based template syncs) merge the same
// files order-preservingly, and Go's sorted map output would fight them on every
// run. Entries are keyed by map identity and hold their map, so an address
// cannot be reused by a different map while recorded.
type jsonKeyOrder map[uintptr]orderedJSONObject

type orderedJSONObject struct {
	object map[string]any
	keys   []string
}

// decodeOrderedJSON decodes one JSON object, recording key order. Strict mode
// rejects duplicate keys; lenient mode keeps json.Unmarshal's last-wins value at
// the key's first position.
func decodeOrderedJSON(data []byte, strict bool) (map[string]any, jsonKeyOrder, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	order := make(jsonKeyOrder)
	value, err := order.decodeValue(decoder, strict, 0)
	if err != nil {
		return nil, nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, nil, errors.New("expected one JSON object")
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, nil, errors.New("expected a JSON configuration object")
	}
	return root, order, nil
}

func (o jsonKeyOrder) decodeValue(decoder *json.Decoder, strict bool, depth int) (any, error) {
	if depth > 128 {
		return nil, errors.New("JSON configuration exceeds 128 nesting levels")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '{':
		object := make(map[string]any)
		var keys []string
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("expected a JSON object key")
			}
			_, exists := object[key]
			if exists && strict {
				return nil, fmt.Errorf("duplicate JSON key %q", key)
			}
			value, err := o.decodeValue(decoder, strict, depth+1)
			if err != nil {
				return nil, err
			}
			if !exists {
				keys = append(keys, key)
			}
			object[key] = value
		}
		o[reflect.ValueOf(object).Pointer()] = orderedJSONObject{object: object, keys: keys}
		_, err := decoder.Token()
		return object, err
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			value, err := o.decodeValue(decoder, strict, depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		_, err := decoder.Token()
		return array, err
	default:
		return nil, errors.New("unexpected JSON closing delimiter")
	}
}

// encodeOrderedJSON writes root with two-space indentation and a trailing
// newline. Recorded objects keep their source key order, with new keys appended
// sorted; unrecorded objects are fully sorted, as json.Marshal would. Unlike
// json.Marshal, <, > and & stay literal so shell commands round-trip unchanged.
func encodeOrderedJSON(root map[string]any, order jsonKeyOrder) ([]byte, error) {
	var compact bytes.Buffer
	if err := order.writeValue(&compact, root); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

func (o jsonKeyOrder) writeValue(buf *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case map[string]any:
		buf.WriteByte('{')
		for i, key := range o.keys(v) {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeJSONScalar(buf, key); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := o.writeValue(buf, v[key]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case []any:
		buf.WriteByte('[')
		for i, element := range v {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := o.writeValue(buf, element); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	default:
		return writeJSONScalar(buf, v)
	}
	return nil
}

func (o jsonKeyOrder) keys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	seen := make(map[string]bool, len(object))
	for _, key := range o[reflect.ValueOf(object).Pointer()].keys {
		if _, ok := object[key]; ok {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	added := make([]string, 0, len(object)-len(keys))
	for key := range object {
		if !seen[key] {
			added = append(added, key)
		}
	}
	slices.Sort(added)
	return append(keys, added...)
}

// writeJSONScalar also encodes any other typed value setup builds itself; such
// values were never decoded, so encoding/json's sorted key order is correct.
func writeJSONScalar(buf *bytes.Buffer, value any) error {
	encoder := json.NewEncoder(buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	buf.Truncate(buf.Len() - 1) // Encode terminates each value with a newline
	return nil
}
