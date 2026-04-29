package abtest

import "encoding/json"

func mapToJSON(value map[string]interface{}) (string, error) {
	if len(value) == 0 {
		return "{}", nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func jsonToMap(raw string) map[string]interface{} {
	if raw == "" {
		return map[string]interface{}{}
	}
	var value map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return map[string]interface{}{}
	}
	if value == nil {
		return map[string]interface{}{}
	}
	return value
}
