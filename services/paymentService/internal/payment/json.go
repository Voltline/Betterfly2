package payment

import "encoding/json"

func encodeStringMap(value map[string]string) string {
	if len(value) == 0 {
		return "{}"
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func decodeStringMap(raw string) map[string]string {
	result := map[string]string{}
	if raw == "" {
		return result
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return map[string]string{}
	}
	return result
}
