package domain

import "strconv"

// FormatRub форматирует сумму в рублях с разделителями разрядов: 1 500 000 ₽.
func FormatRub(v int64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	s := strconv.FormatInt(v, 10)
	out := make([]byte, 0, len(s)+len(s)/3+4)
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ' ')
		}
		out = append(out, c)
	}
	res := string(out) + " ₽"
	if neg {
		return "−" + res
	}
	return res
}

func itoa(i int) string { return strconv.Itoa(i) }
