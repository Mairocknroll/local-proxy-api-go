package mqtt

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/goburrow/modbus"
)

type Config struct {
	ServerURL   string
	MQTTHost    string
	MQTTPort    int
	ParkingCode string
}

func loadConfigFromEnv() Config {
	serverURL := getenvDefault("SERVER_URL", "https://api-pms.jparkdev.co")
	mqttPort := getenvIntDefault("MQTT_PORT", 1883)
	parkingCode := os.Getenv("PARKING_CODE")

	var mqttHost string
	if serverURL == "https://api-pms.promptpark.co" {
		mqttHost = "52.77.148.42"
	} else {
		mqttHost = "54.179.35.242"
	}

	return Config{
		ServerURL:   serverURL,
		MQTTHost:    mqttHost,
		MQTTPort:    mqttPort,
		ParkingCode: parkingCode,
	}
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvIntDefault(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		var n int
		_, err := fmt.Sscanf(v, "%d", &n)
		if err == nil {
			return n
		}
	}
	return def
}

type Listener struct {
	cfg    Config
	client paho.Client
}

func NewFromEnv() *Listener {
	return &Listener{cfg: loadConfigFromEnv()}
}

// Start: เชื่อม MQTT และรอ ctx cancel
func (l *Listener) Start(ctx context.Context) error {
	opts := paho.NewClientOptions().
		AddBroker(fmt.Sprintf("tcp://%s:%d", l.cfg.MQTTHost, l.cfg.MQTTPort)).
		SetClientID(fmt.Sprintf("barrier-listener-%d", time.Now().UnixNano())).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(2 * time.Second).
		SetOnConnectHandler(func(c paho.Client) {
			log.Printf("[MQTT] Connected to %s:%d", l.cfg.MQTTHost, l.cfg.MQTTPort)
			topic := fmt.Sprintf("+/%s/+/+/barrier/command", wildcardIfEmpty(l.cfg.ParkingCode))
			if token := c.Subscribe(topic, 1, l.onMessage); token.Wait() && token.Error() != nil {
				log.Printf("[MQTT][ERROR] subscribe %s: %v", topic, token.Error())
			} else {
				log.Printf("[MQTT] Subscribed to %s", topic)
			}
		}).
		SetConnectionLostHandler(func(c paho.Client, err error) {
			log.Printf("[MQTT][WARN] connection lost: %v", err)
		})

	l.client = paho.NewClient(opts)
	if token := l.client.Connect(); token.Wait() && token.Error() != nil {
		return fmt.Errorf("connect mqtt: %w", token.Error())
	}

	<-ctx.Done()
	log.Println("[MQTT] context cancelled → disconnecting…")
	l.client.Disconnect(250)
	return nil
}

func wildcardIfEmpty(s string) string {
	if s == "" {
		return "+"
	}
	return s
}

func (l *Listener) onMessage(_ paho.Client, msg paho.Message) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[MQTT][PANIC] %v", r)
		}
	}()

	payload := strings.TrimSpace(strings.ToLower(string(msg.Payload())))
	topic := msg.Topic()
	log.Printf("[MQTT] Received → Topic: %s | Payload: %s", topic, payload)

	// {location}/{parking_code}/{direction}/{gate_no}/barrier/command
	parts := strings.Split(topic, "/")
	if len(parts) < 6 || parts[4] != "barrier" || parts[5] != "command" {
		log.Println("[MQTT] Invalid topic structure")
		return
	}

	location := parts[0]
	direction := parts[2] // ent|ext
	gateNo := parts[3]    // ex: 01

	ip := getGateIP(direction, gateNo, location)
	if ip == "" {
		log.Printf("[MQTT] No IP configured for %s-%s (location=%s)", strings.ToUpper(direction), gateNo, location)
		return
	}

	switch payload {
	case "open":
		l.toggle(location, ip, true, direction)
	case "close":
		l.toggle(location, ip, false, direction)
	default:
		log.Printf("[MQTT] Unknown command: %s", payload)
	}
}

func getGateIP(direction, gateNo, location string) string {
	barrier := "GATE"
	if location != "parking" {
		barrier = "ZONE"
	}
	keyRaw := fmt.Sprintf("%s_%s_%s", strings.ToUpper(direction), barrier, gateNo)
	keyPad := fmt.Sprintf("%s_%s_%02s", strings.ToUpper(direction), barrier, gateNo)

	if v := os.Getenv(keyRaw); v != "" {
		return v
	}
	return os.Getenv(keyPad)
}

func (l *Listener) toggle(location, ip string, open bool, direction string) {
	if location == "parking" {
		l.toggleBarrier(ip, open)
	} else {
		l.toggleZoningBarrier(ip, open, direction)
	}
}

func (l *Listener) toggleBarrier(ip string, open bool) {
	coil := uint16(1)
	if !open {
		coil = 4
	}
	if err := pulseCoil(ip, 504, coil, 500*time.Millisecond); err != nil {
		log.Printf("[MODBUS][ERROR] %v", err)
		return
	}
	log.Printf("[MODBUS] %s sent to %s (coil=%d)", tern(open, "OPEN", "CLOSE"), ip, coil)
}

func (l *Listener) toggleZoningBarrier(ip string, open bool, direction string) {
	var coil uint16
	if strings.ToLower(direction) == "ent" {
		if open {
			coil = 1
		} else {
			coil = 4
		}
	} else {
		if open {
			coil = 1
		} else {
			coil = 4
		}
	}
	if err := pulseCoil(ip, 504, coil, 500*time.Millisecond); err != nil {
		log.Printf("[MODBUS][ERROR] %v", err)
		return
	}
	log.Printf("[MODBUS] %s sent to %s (coil=%d)", tern(open, "OPEN", "CLOSE"), ip, coil)
}

func pulseCoil(ip string, port int, coil uint16, dur time.Duration) error {
	handler := modbus.NewTCPClientHandler(fmt.Sprintf("%s:%d", ip, port))
	handler.Timeout = 2 * time.Second
	handler.IdleTimeout = 5 * time.Second // v0.2.1 มีฟิลด์นี้
	// handler.SlaveId = 1 // ถ้าคุณต้องการกำหนด Unit ID เฉพาะ

	if err := handler.Connect(); err != nil {
		return fmt.Errorf("connect modbus %s: %w", handler.Address, err)
	}
	defer handler.Close()

	client := modbus.NewClient(handler)
	if _, err := client.WriteSingleCoil(coil, 0xFF00); err != nil { // TRUE
		return fmt.Errorf("write coil=%d true: %w", coil, err)
	}
	time.Sleep(dur)
	if _, err := client.WriteSingleCoil(coil, 0x0000); err != nil { // FALSE
		return fmt.Errorf("write coil=%d false: %w", coil, err)
	}
	return nil
}

func tern[T any](cond bool, a, b T) T {
	if cond {
		return a
	}
	return b
}
