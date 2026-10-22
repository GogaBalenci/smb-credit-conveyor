package domain

// Контрольные коэффициенты ИНН (приказ ФНС).
var (
	innCoef10 = []int{2, 4, 10, 3, 5, 9, 4, 6, 8}
	innCoef11 = []int{7, 2, 4, 10, 3, 5, 9, 4, 6, 8}
	innCoef12 = []int{3, 7, 2, 4, 10, 3, 5, 9, 4, 6, 8}
)

func innCheckDigit(digits []int, coef []int) int {
	sum := 0
	for i, c := range coef {
		sum += digits[i] * c
	}
	return sum % 11 % 10
}

// ValidINN проверяет формат и контрольные цифры ИНН юрлица (10 цифр) или ИП (12 цифр).
func ValidINN(inn string) bool {
	d := make([]int, len(inn))
	for i, r := range inn {
		if r < '0' || r > '9' {
			return false
		}
		d[i] = int(r - '0')
	}
	switch len(d) {
	case 10:
		return innCheckDigit(d, innCoef10) == d[9]
	case 12:
		return innCheckDigit(d, innCoef11) == d[10] && innCheckDigit(d, innCoef12) == d[11]
	default:
		return false
	}
}

// INNKind — вид субъекта по длине ИНН.
func INNKind(inn string) CompanyKind {
	if len(inn) == 12 {
		return CompanyIndividual
	}
	return CompanyLegal
}

// CompleteINN дописывает контрольные цифры к основе ИНН (9 цифр для юрлица,
// 10 — для ИП). Используется для генерации синтетических данных.
func CompleteINN(base string) string {
	d := make([]int, 0, 12)
	for _, r := range base {
		d = append(d, int(r-'0'))
	}
	switch len(d) {
	case 9:
		d = append(d, innCheckDigit(d, innCoef10))
	case 10:
		d = append(d, innCheckDigit(d, innCoef11))
		d = append(d, innCheckDigit(d, innCoef12))
	default:
		return ""
	}
	out := make([]byte, len(d))
	for i, x := range d {
		out[i] = byte('0' + x)
	}
	return string(out)
}
