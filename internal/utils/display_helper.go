package utils

import (
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"unicode/utf16"
)

// ------------------------------------------------------------
// Utilities: จัดการสตริงเฮกซ์ / เข้ารหัส UTF-16BE / ถอดรหัส
// ------------------------------------------------------------

// hexStringToByteArray แปลงสตริงเฮกซ์ (อาจมีช่องว่าง, \n, \r, #) เป็น []byte
func hexStringToByteArray(hexStr string) ([]byte, error) {
	clean := cleanHex(hexStr)
	// hex.DecodeString ต้องไม่มีช่องว่าง
	return hex.DecodeString(clean)
}

// SendUDPPacket ส่งแพ็กเก็ต UDP ด้วยสตริงเฮกซ์ (หลายบรรทัดได้)
func SendUDPPacket(ip string, port int, hexStr string) error {
	data, err := hexStringToByteArray(hexStr)
	if err != nil {
		return fmt.Errorf("decode hex failed: %w", err)
	}
	addr := fmt.Sprintf("%s:%d", ip, port)
	conn, err := net.Dial("udp", addr)
	if err != nil {
		return fmt.Errorf("udp dial failed: %w", err)
	}
	defer conn.Close()

	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("udp write failed: %w", err)
	}
	return nil
}

// StrToUTF16BEHex แปลงข้อความเป็นสตริงเฮกซ์ไบต์ UTF-16BE (คั่นด้วยช่องว่าง)
func StrToUTF16BEHex(text string) string {
	// แปลง rune -> UTF16 (uint16) จากนั้นแตกเป็น big-endian byte
	u16 := utf16.Encode([]rune(text))
	var b strings.Builder
	for i, v := range u16 {
		hi := byte(v >> 8)
		lo := byte(v & 0xFF)
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(fmt.Sprintf("%02x %02x", hi, lo))
	}
	return b.String()
}

// UTF16HexToStr ถอดสตริงเฮกซ์ (UTF16 BE/LE) กลับเป็นข้อความ (ignore error แบบเดียวกับ Python errors="ignore")
func UTF16HexToStr(hexStr string, endian string) (string, error) {
	clean := cleanHex(hexStr)
	by, err := hex.DecodeString(clean)
	if err != nil {
		return "", fmt.Errorf("decode hex failed: %w", err)
	}
	// แปลงเป็นคู่ uint16 ตาม endian
	if len(by)%2 != 0 {
		// ตัดไบต์ท้ายทิ้งเพื่อเลียนแบบ ignore
		by = by[:len(by)-1]
	}
	u16s := make([]uint16, 0, len(by)/2)
	for i := 0; i+1 < len(by); i += 2 {
		var val uint16
		if strings.ToLower(endian) == "le" {
			val = uint16(by[i]) | (uint16(by[i+1]) << 8)
		} else {
			val = (uint16(by[i]) << 8) | uint16(by[i+1])
		}
		u16s = append(u16s, val)
	}
	// ถอดเป็น rune
	runes := utf16.Decode(u16s)
	return string(runes), nil
}

// EncodeThaiLicensePlateToCustomUTF16BEHex
// กติกาเดียวกับ Python:
// - ASCII (<0x80): ถ้าตัวก่อนหน้าเป็นไทย -> "0e xx" ไม่งั้น "00 xx"
// - Non-ASCII: เอา low byte (code & 0xFF)
//   - ถ้าเป็นไทย (0x0E00..0x0E7F) และยังไม่เคยเจอไทยมาก่อน -> "00 xx"
//   - ไม่งั้น -> "0e xx"
//
// - ติดตามสถานะ prevThai / firstThaiFound เหมือนเดิม
func EncodeThaiLicensePlateToCustomUTF16BEHex(text string) string {
	var result []string
	prevThai := false
	firstThaiFound := false

	for _, r := range text {
		code := int(r)
		isThai := code >= 0x0E00 && code <= 0x0E7F
		if code < 0x80 {
			// ASCII
			if prevThai {
				result = append(result, fmt.Sprintf("0e %02x", code))
			} else {
				result = append(result, fmt.Sprintf("00 %02x", code))
			}
			prevThai = false
		} else {
			low := code & 0xFF
			if isThai && !firstThaiFound {
				result = append(result, fmt.Sprintf("00 %02x", low))
				firstThaiFound = true
			} else {
				result = append(result, fmt.Sprintf("0e %02x", low))
			}
			prevThai = true
		}
	}
	return strings.Join(result, " ")
}

// AmountHexTo16Bytes ทำ padding ให้ได้ 16 ไบต์ ตามกติกาใน Python เวอร์ชันเดิม
func AmountHexTo16Bytes(amountHex string) string {
	hexClean := removeSpaces(amountHex)
	lengthBytes := len(hexClean) / 2
	if lengthBytes >= 16 {
		return amountHex
	}

	var need int
	var needStr string
	var prefix string

	if lengthBytes == 14 {
		need = 16 - lengthBytes // 2
		needStr = repeatZero(need)
		prefix = ""
	} else if lengthBytes == 12 {
		need = 16 - lengthBytes        // 4
		needStr = repeatZero(need - 2) // 2
		prefix = "00 20 "
	} else {
		need = 16 - lengthBytes
		needStr = repeatZero(need - 4)
		prefix = "00 20 00 20 "
	}
	return strings.TrimSpace(prefix + needStr + amountHex)
}

// AmountHexTo14Bytes ทำ padding ให้ได้ 14 ไบต์ ตามกติกาใน Python เวอร์ชันเดิม
func AmountHexTo14Bytes(amountHex string) string {
	hexClean := removeSpaces(amountHex)
	lengthBytes := len(hexClean) / 2
	if lengthBytes >= 14 {
		return amountHex
	}

	var need int
	var needStr string
	var prefix string

	if lengthBytes < 10 {
		need = 14 - lengthBytes
		needStr = repeatZero(need - 4)
		prefix = "00 20 00 20 "
	} else {
		need = 14 - lengthBytes
		needStr = repeatZero(need - 2)
		prefix = "00 20"
	}
	return strings.TrimSpace(prefix + needStr + " " + amountHex)
}

// ------------------------------------------------------------
// DisplayHexData: สร้างเฟรมเฮกซ์แล้วส่ง UDP ตามสูตรเดิม
// ------------------------------------------------------------

func DisplayHexData(
	screenIP string,
	screenPort int,
	licensePlate string,
	direction string, // "ent" / "ext"
	stateType string, // "clear" / "main" / ...
	line3 string,
) error {
	// record1 = "JPARK" ใน UTF-16BE hex
	record1 := StrToUTF16BEHex("JPARK")

	// record2: ป้ายทะเบียนไทย (หรือเคลียร์)
	var record2 string
	if stateType == "clear" {
		record2 = repeatZero(14)
	} else {
		record2Hex := EncodeThaiLicensePlateToCustomUTF16BEHex(licensePlate)
		record2 = AmountHexTo14Bytes(record2Hex)
	}

	// record3: บรรทัด 3 แสดงเฉพาะ state_type=="main" และทิศ ent/ext เท่านั้น
	var record3 string
	if stateType == "main" && (direction == "ent" || direction == "ext") {
		if strings.TrimSpace(line3) != "" {
			record3Hex := StrToUTF16BEHex(line3)
			record3 = AmountHexTo16Bytes(record3Hex)
		} else {
			record3 = repeatZero(16)
		}
	} else {
		record3 = repeatZero(16)
	}

	// record4: ent -> "Welcome" + " 00 00", ext -> "ThankYou"
	var record4 string
	if direction == "ent" {
		record4 = StrToUTF16BEHex("Welcome") + " 00 00"
	} else {
		record4 = StrToUTF16BEHex("ThankYou")
	}

	// ประกอบแพ็กเก็ตตามสัดส่วนเดิม (อนุญาตมีช่องว่าง/ขึ้นบรรทัด)
	hexData := fmt.Sprintf(`
55 aa 00 00 01 00 00 db 00 00
b9 00 00 00 01 01 b9 00 00 00 01 01 b8 00 00 00
04 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00
00 00

00 01 24 00 00 00 0e 00 00 00 00 3f 00 0f
00 04 00 00 01 00 0a 05
11 0a 00 00
%s

00 02 28 00 00 00 0e 00 00 10 00 3f 00 1f
00 02 00 00 01 00 0a 05
10 10 00 00
%s

00 03 2a 00 00 00 0e 00 00 20 00 3f 00 2f
00 01 00 00 01 00 0a 05
10 10 00 00
%s

00 04 2a 00 00 00 0e 00 00 30 00 3f 00 3f
00 04 00 3F 20 00 14 02
10 10 00 00
%s

00 00 00 0d 0a
`, record1, record2, record3, record4)

	// ส่ง UDP
	if err := SendUDPPacket(screenIP, screenPort, hexData); err != nil {
		return err
	}
	return nil
}

// ------------------------------------------------------------
// Helpers ภายในไฟล์
// ------------------------------------------------------------

func cleanHex(s string) string {
	// ตัด \n \r # และช่องว่างทั้งหมดออก
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "#", " ")
	// ยุบช่องว่างซ้ำ แล้วเอาออกให้หมด
	s = strings.Join(strings.Fields(s), "")
	return strings.ToLower(s)
}

func removeSpaces(s string) string {
	return strings.Join(strings.Fields(s), "")
}

func repeatZero(n int) string {
	if n <= 0 {
		return ""
	}
	// "00 " * n แล้ว trim ท้าย
	return strings.TrimSpace(strings.Repeat("00 ", n))
}
