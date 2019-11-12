package server

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
)

type FrameType byte

const (
	FRAME_TYPE_MESSAGE       FrameType = 90
	FRAME_TYPE_HEARTBEAT     FrameType = 91
	FRAME_TYPE_HEARTBEAT_ACK FrameType = 92
)

type ConnectionHandler func(socket *Socket)
type MessageHandler func(data string)

type Socket struct {
	Id               string
	connection       net.Conn
	events           map[string]MessageHandler
	server           *Server
	connected        bool
	lastHeartbeatAck int64
}

type Server struct {
	address         string
	listener        net.Listener
	sockets         map[string]*Socket
	connectEvent    ConnectionHandler
	disconnectEvent ConnectionHandler
}

func (s *Server) addSocket(conn net.Conn) *Socket {
	uid := uuid.New().String()
	sock := &Socket{Id: uid, connection: conn, events: map[string]MessageHandler{}, server: s, connected: true}
	s.sockets[uid] = sock
	return sock
}

func (s *Server) removeSocket(socket *Socket) {
	if _, ok := s.sockets[socket.Id]; ok {
		socket.connected = false
		delete(s.sockets, socket.Id)
	}
}

func (s *Server) Listen() {
	defer s.listener.Close()
	log.Println("Server listening on " + s.listener.Addr().String())

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			log.Printf("Couldn't accept connection: %v\n", err)
		} else {
			go s.handleConnection(conn)
		}
	}
}

func (s *Server) OnConnection(handler ConnectionHandler) {
	s.connectEvent = handler
}

func (s *Server) OnDisconnection(handler ConnectionHandler) {
	s.disconnectEvent = handler
}

func (s *Server) EmitSync(event, data string) {
	for _, socket := range s.sockets {
		go socket.Emit(event, data)
	}
	time.Sleep(time.Millisecond * 2)
}

func (s *Server) Emit(event, data string) {
	for _, socket := range s.sockets {
		go socket.Emit(event, data)
	}
}

func (s *Server) Connection() net.Listener {
	return s.listener
}

func (s *Socket) On(event string, callback MessageHandler) {
	s.events[event] = callback
}

func (s *Socket) Off(event string) {
	if _, ok := s.events[event]; ok {
		delete(s.events, event)
	}
}

func (s *Socket) EmitSync(event, data string) {
	emit(s, event, data)
	time.Sleep(time.Millisecond * 2)
}

func (s *Socket) Emit(event, data string) {
	go emit(s, event, data)
}

func (s *Socket) BroadcastSync(event, data string) {
	for id, socket := range s.server.sockets {
		if id == s.Id {
			continue
		}
		go socket.Emit(event, data)
	}
	time.Sleep(time.Millisecond * 2)
}

func (s *Socket) Broadcast(event, data string) {
	for id, socket := range s.server.sockets {
		if id == s.Id {
			continue
		}
		go socket.Emit(event, data)
	}
}

func (s *Socket) Connected() bool {
	return s.connected
}

func (s *Socket) Connection() net.Conn {
	return s.connection
}

func (s *Socket) Disconnect() {
	s.connected = false
}

func (s *Socket) Send(event string, data []byte) {
	send(s, event, data, FRAME_TYPE_MESSAGE)
}

func (s *Socket) envokeEvent(name, data string) {
	if handler, ok := s.events[name]; ok {
		handler(data)
	}
}

func (s *Socket) startHeartbeat() {
	time.Sleep(time.Second * 5)
	for {
		if !s.connected {
			break
		}

		log.Println("sending heartbeat")
		start := time.Now().UnixNano() / 1000000
		raw(s, []byte{}, FRAME_TYPE_HEARTBEAT)
		time.Sleep(time.Second * 5)
		if !s.connected {
			break
		}
		log.Println("heartbeat wakeup", s.lastHeartbeatAck == 0, s.lastHeartbeatAck-start)
		if s.lastHeartbeatAck == 0 || s.lastHeartbeatAck-start > 5000 {
			log.Println("disconnecting client")
			s.connected = false
			break
		}
	}
}

func (s *Server) handleConnection(conn net.Conn) {
	// log.Printf("Accepted connection from %v\n", conn.RemoteAddr().String())
	socket := s.addSocket(conn)
	s.connectEvent(socket)
	go socket.startHeartbeat()
	socket.listen()
}

func (s *Socket) listen() {
	sockBuffer := bufio.NewReader(s.connection)

	for {
		if !s.connected {
			break
		}

		recv, err := sockBuffer.ReadBytes(10)
		if err != nil {
			// log.Println(err)
			break
		}

		log.Printf("in [%v] > %v", len(recv), "recv")

		go func(frame []byte) {
			if !s.connected {
				return
			}
			frameLen := len(frame)

			if frameLen > 0 {
				switch frame[0] {
				case byte(FRAME_TYPE_MESSAGE):
					if frameLen > 2 {
						eventLen := binary.BigEndian.Uint16(frame[1:3])
						eventName := strings.Trim(string(frame[3:3+eventLen]), "\x00")
						if frameLen > 3+int(eventLen) {
							data := frame[3+eventLen : frameLen-1]
							dataLen := len(data)

							filtered := make([]byte, 0, dataLen)
							skip := 0
							for i := 0; i < dataLen; i++ {
								if skip > 1 {
									skip--
									continue
								}
								if data[i] == 92 && i != dataLen-2 && data[i+1] == 92 && data[i+2] == 0 {
									skip = 2
									continue
								}
								if skip == 1 {
									filtered = append(filtered, 10)
									skip--
								} else {
									filtered = append(filtered, data[i])
								}
							}

							go s.envokeEvent(eventName, string(filtered))
						}
					}
				case byte(FRAME_TYPE_HEARTBEAT):
					raw(s, []byte{}, FRAME_TYPE_HEARTBEAT_ACK)
				case byte(FRAME_TYPE_HEARTBEAT_ACK):
					s.lastHeartbeatAck = time.Now().UnixNano() / 1000000
				}
			}
		}(recv)
	}
	s.connection.Close()
	s.server.removeSocket(s)
	s.server.disconnectEvent(s)
}

func buildMessageFrameHeader(event string, frameType FrameType) ([]byte, error) {
	if len(event) > 1<<16-2 {
		return nil, fmt.Errorf("Event Name length exceeds the maximum of %v bytes\n", 1<<16-2)
	}

	frameBuff := []byte{}
	frameBuff = append(frameBuff, byte(frameType))

	event = strings.ReplaceAll(event, "\n", "")

	eventLenBuff := make([]byte, 2)
	eventBytes := []byte(event)
	eventLen := len(eventBytes)

	if eventLen/256 == 10 {
		for i := 0; i < 256-eventLen%256; i++ {
			eventBytes = append(eventBytes, 0)
		}
	} else if eventLen%256 == 10 {
		eventBytes = append(eventBytes, 0)
	}

	binary.BigEndian.PutUint16(eventLenBuff, uint16(len(eventBytes)))
	frameBuff = append(frameBuff, eventLenBuff...)
	frameBuff = append(frameBuff, eventBytes...)

	return frameBuff, nil
}

func buildMessageFrame(event string, data []byte, frameType FrameType) ([]byte, error) {
	frame, err := buildMessageFrameHeader(event, frameType)
	if err != nil {
		return nil, err
	}

	frame = append(frame, (bytes.ReplaceAll(data, []byte{10}, []byte{92, 92, 0}))...)
	frame = append(frame, 10)

	return frame, nil
}

func buildFrame(data []byte, frameType FrameType) ([]byte, error) {
	frame := []byte{}
	frame = append(frame, byte(frameType))

	frame = append(frame, (bytes.ReplaceAll(data, []byte{10}, []byte{92, 92, 0}))...)
	frame = append(frame, 10)

	return frame, nil
}

func send(socket *Socket, event string, data []byte, frameType FrameType) {
	if !socket.connected {
		return
	}
	frame, err := buildMessageFrame(event, data, frameType)
	if err != nil {
		return
	}
	log.Printf("out < %v\n", frame)
	if _, err = socket.connection.Write(frame); err != nil {
		return
	}
}

func raw(socket *Socket, data []byte, frameType FrameType) {
	if !socket.connected {
		return
	}
	frame, err := buildFrame(data, frameType)
	if err != nil {
		return
	}
	log.Printf("out < %v\n", frame)
	if _, err = socket.connection.Write(frame); err != nil {
		return
	}
}

func emit(socket *Socket, event, data string) {
	send(socket, event, []byte(data), FRAME_TYPE_MESSAGE)
}

func New(address string) (*Server, error) {
	l, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}

	return &Server{
		address:         address,
		listener:        l,
		sockets:         map[string]*Socket{},
		connectEvent:    func(socket *Socket) {},
		disconnectEvent: func(socket *Socket) {},
	}, nil
}
