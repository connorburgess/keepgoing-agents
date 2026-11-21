package report

import (
	"bytes"
	"encoding/json"
	"io"
)

// validDeliveryJSON rejects ambiguity before Go's case-insensitive struct
// decoding can collapse it. Apply the same rule to both history versions so an
// unrelated save cannot repair or normalize a damaged older record either.
func validDeliveryJSON(body []byte) bool {
	return deliveryJSONObject(body, 0)
}

func deliveryJSONObject(body []byte, level int) bool {
	fields := [][]string{
		{"schemaVersion", "nextSequence", "dropped", "records"},
		{"sequence", "event", "startedAt", "updatedAt", "outcome", "receipt", "attempts"},
		{"startedAt", "finishedAt", "outcome", "httpStatus"},
	}
	if level < 0 || level >= len(fields) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(body))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	seen := make(map[string]bool)
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return false
		}
		known := false
		for _, field := range fields[level] {
			if field == key {
				known = true
				break
			}
		}
		if !known {
			return false
		}
		seen[key] = true
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return false
		}
		if (level == 0 && key == "records") || (level == 1 && key == "attempts") {
			// Previous writers can encode an empty slice as null. Preserve that
			// representation; typed invariants still require attempts for sends.
			var items []json.RawMessage
			if json.Unmarshal(raw, &items) != nil {
				return false
			}
			for _, item := range items {
				if !deliveryJSONObject(item, level+1) {
					return false
				}
			}
		} else if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return false
		}
	}
	token, err = d.Token()
	return err == nil && token == json.Delim('}') && d.Decode(new(any)) == io.EOF
}
