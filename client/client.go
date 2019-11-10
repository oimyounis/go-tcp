package client

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"regexp"
	"strings"
	"time"
)

const DELIMITER = "§"
const DELIMITER_LENGTH = len(DELIMITER)

var delimiterRegex *regexp.Regexp = regexp.MustCompile(`(.*[^\\])§((.*\n?.*)*)`)

type ConnectionHandler func(socket *Socket)
type MessageHandler func(data string)

type Socket struct {
	connection      net.Conn
	events          map[string]MessageHandler
	connectEvent    ConnectionHandler
	disconnectEvent ConnectionHandler
}

func (s *Socket) EmitSync(event, data string) {
	emit(s, event, data)
	time.Sleep(time.Millisecond * 5)
}

func (s *Socket) Emit(event, data string) {
	go emit(s, event, data)
}

func (s *Socket) Start() {
	s.connectEvent(s)
	go s.socketReceiver()
}

func (s *Socket) Listen() {
	s.connectEvent(s)
	s.socketReceiver()
}

func (s *Socket) On(event string, callback MessageHandler) {
	s.events[event] = callback
}

func (s *Socket) Off(event string) {
	if _, ok := s.events[event]; ok {
		delete(s.events, event)
	}
}

func (s *Socket) socketReceiver() {
	sockBuffer := bufio.NewReader(s.connection)
	for {
		recv, err := sockBuffer.ReadString('\x00')
		if err != nil {
			// log.Println(err)
			break
		}

		go func() {
			message := strings.TrimSpace(recv)
			if strings.Contains(message, DELIMITER) {
				parts := delimiterRegex.FindAllStringSubmatch(message, 2)
				if len(parts) == 1 && len(parts[0]) >= 3 {
					event := parts[0][1]

					if strings.Contains(event, "\\"+DELIMITER) {
						event = strings.ReplaceAll(event, "\\"+DELIMITER, DELIMITER)
					}

					if handler, ok := s.events[event]; ok {
						data := parts[0][2]

						if strings.Contains(data, "\\"+DELIMITER) {
							data = strings.ReplaceAll(data, "\\"+DELIMITER, DELIMITER)
						}

						data = strings.Trim(data, "\x00\x01")

						go handler(data)
					}
				} else {
					log.Printf("Received a malformed message: %v\n", message)
				}
			}
		}()
	}
	s.connection.Close()
	s.disconnectEvent(s)
}

func (s *Socket) OnConnect(handler ConnectionHandler) {
	s.connectEvent = handler
}

func (s *Socket) OnDisconnect(handler ConnectionHandler) {
	s.disconnectEvent = handler
}

func emit(socket *Socket, event, data string) {
	if strings.Contains(data, DELIMITER) {
		data = strings.ReplaceAll(data, DELIMITER, "\\"+DELIMITER)
	}
	if strings.ContainsRune(data, '\x00') {
		data = strings.ReplaceAll(data, "\x00", "\x01")
	}
	if strings.Contains(event, DELIMITER) {
		event = strings.ReplaceAll(event, DELIMITER, "\\"+DELIMITER)
	}
	if strings.ContainsRune(event, '\x00') {
		event = strings.ReplaceAll(event, "\x00", "\x01")
	}
	packet := []byte(fmt.Sprintf("%v%v%v", event, DELIMITER, data))
	packet = append(packet, '\x00')
	socket.connection.Write(packet)
}

func New(address string) (*Socket, error) {
	conn, err := net.Dial("tcp", address)
	if err != nil {
		return nil, err
	}

	return &Socket{
		connection:      conn,
		events:          map[string]MessageHandler{},
		connectEvent:    func(socket *Socket) {},
		disconnectEvent: func(socket *Socket) {},
	}, nil
}
