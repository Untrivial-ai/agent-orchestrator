package authutil

// JSONC removes comments and trailing commas from bounded native config data.
// JSON decoding remains the caller's responsibility; malformed comments return nil.
func JSONC(data []byte) []byte {
	if len(data) > MaxFileSize {
		return nil
	}
	out := append([]byte(nil), data...)
	quoted := false
	for i := 0; i < len(out); i++ {
		if quoted {
			if out[i] == '\\' {
				i++
				continue
			}
			if out[i] == '"' {
				quoted = false
			}
			continue
		}
		if out[i] == '"' {
			quoted = true
			continue
		}
		if out[i] != '/' || i+1 >= len(out) {
			continue
		}
		switch out[i+1] {
		case '/':
			for i < len(out) && out[i] != '\n' && out[i] != '\r' {
				out[i] = ' '
				i++
			}
		case '*':
			out[i], out[i+1] = ' ', ' '
			i += 2
			for i+1 < len(out) && (out[i] != '*' || out[i+1] != '/') {
				out[i] = ' '
				i++
			}
			if i+1 >= len(out) {
				return nil
			}
			out[i], out[i+1] = ' ', ' '
			i++
		}
	}
	quoted = false
	for i := 0; i < len(out); i++ {
		if quoted {
			if out[i] == '\\' {
				i++
				continue
			}
			if out[i] == '"' {
				quoted = false
			}
			continue
		}
		if out[i] == '"' {
			quoted = true
			continue
		}
		if out[i] != ',' {
			continue
		}
		j := i + 1
		for j < len(out) && (out[j] == ' ' || out[j] == '\t' || out[j] == '\r' || out[j] == '\n') {
			j++
		}
		if j < len(out) && (out[j] == '}' || out[j] == ']') {
			out[i] = ' '
		}
	}
	return out
}
