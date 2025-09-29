package utils

// VehicleType แปลง string → int code
// (อันนี้เป็นตัวอย่าง นายปรับ mapping ตามระบบจริงได้)
func VehicleType(s string) int {
	switch s {

	case "motorcycle":
		return 2
	case "truck":
		return 3
	case "car":
		return 1
	default:
		return 1
	}
}
