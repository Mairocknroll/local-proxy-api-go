package order

import "encoding/xml"

type eventXML struct {
	XMLName   xml.Name `xml:"http://www.isapi.org/ver20/XMLSchema EventNotificationAlert"`
	IPAddress string   `xml:"ipAddress"`
	DateTime  string   `xml:"dateTime"`
	UUID      string   `xml:"UUID"`
	ANPR      struct {
		VehicleType  string `xml:"vehicleType"`
		LicensePlate string `xml:"licensePlate"`
	} `xml:"ANPR"`
}

// แบบไม่มี namespace (fallback)
type eventXMLNoNS struct {
	XMLName   xml.Name `xml:"EventNotificationAlert"`
	IPAddress string   `xml:"ipAddress"`
	DateTime  string   `xml:"dateTime"`
	UUID      string   `xml:"UUID"`
	ANPR      struct {
		VehicleType  string `xml:"vehicleType"`
		LicensePlate string `xml:"licensePlate"`
	} `xml:"ANPR"`
}
