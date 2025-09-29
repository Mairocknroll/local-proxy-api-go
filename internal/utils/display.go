package utils

import (
	"log"
)

// DisplayHexByEnv แค่ stub log ออกมา
// (จริง ๆ ใส่โค้ดยิง packet ไป LED ได้ตรงนี้)
func DisplayHexByEnv(envKey string, port int, plate, direction, stateType, line3 string) {
	log.Printf("[LED] ip=%s port=%d plate=%s dir=%s state=%s line3=%s",
		envKey, port, plate, direction, stateType, line3)
}
