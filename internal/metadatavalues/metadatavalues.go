package metadatavalues

// Bool returns the bool value for key, or false when the key is absent or has a
// different type.
func Bool(metadata map[string]any, key string) bool {
	value, _ := metadata[key].(bool)
	return value
}

// String returns the string value for key, or an empty string when the key is
// absent or has a different type.
func String(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return value
}

// Int returns the integer value for key. JSON-decoded numeric metadata often
// arrives as float64, while SDK-local metadata commonly uses int or int64.
func Int(metadata map[string]any, key string) int {
	switch value := metadata[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}
