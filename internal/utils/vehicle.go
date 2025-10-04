package utils

import "strings"

func VehicleType(s string) int {
	t := strings.ToLower(strings.TrimSpace(s))
	switch t {
	case "truck":
		return 3
	case "motorcycle", "twowheelvehicle":
		return 2
	default:
		return 1
	}
}
