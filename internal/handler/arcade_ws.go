package handler

import (
	crand "crypto/rand"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"quorum/internal/auth"
)

// Networked arcade play. The server is a seat-managing RELAY, not a referee:
// game rules run inside every player's cartridge (the same perft-tested logic
// crate), turn order is enforced client-side, and the host's cartridge is
// authoritative for the real-time cabinets. The server's job is rooms, seats,
// ordered fan-out, and making sure only signed-in members get either.
//
// Single-replica note: rooms live in process memory, like the rate limiters.
// Multi-replica deployments would need sticky sessions for /arcade/ws.

// arcadeNetGames are the cabinets that accept networked seats.
var arcadeNetGames = map[string]struct{ minSeats, maxSeats int }{
	"chess":      {2, 2},
	"go":         {2, 2},
	"powder-keg": {2, 12},
	"hexfection": {2, 12},
	"interns":    {2, 2},
}

const (
	wsAuthDeadline = 5 * time.Second
	wsIdleDeadline = 70 * time.Second
	wsPingEvery    = 30 * time.Second
	// Big enough for an INTERNS level document (48 KB cap) plus envelope —
	// the host relays the level to its guest at round start.
	wsMaxMsgBytes   = 56 * 1024
	wsSendQueue     = 64
	maxArcadeRooms  = 200
	roomIdleTimeout = 30 * time.Minute
)

// arcadePeer abstracts the transport so room logic is unit-testable.
type arcadePeer interface {
	deliver(v any) // non-blocking; drops the peer if its queue is full
	kick()
}

type roomMember struct {
	userID string
	peer   arcadePeer
}

type arcadeRoom struct {
	code       string
	game       string
	seats      int // total seats incl. any bot-filled ones
	started    bool
	members    map[int]*roomMember // seat → member
	lastActive time.Time
}

func (r *arcadeRoom) present() []int {
	out := make([]int, 0, len(r.members))
	for seat := range r.members {
		out = append(out, seat)
	}
	// Insertion order of maps is random; the client sorts. Keep it stable
	// anyway for readable tests.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func (r *arcadeRoom) roomInfo(seat int) map[string]any {
	return map[string]any{
		"op":      "room",
		"code":    r.code,
		"game":    r.game,
		"seats":   r.seats,
		"seat":    seat,
		"present": r.present(),
	}
}

func (r *arcadeRoom) broadcast(v any, except int) {
	for seat, m := range r.members {
		if seat == except {
			continue
		}
		m.peer.deliver(v)
	}
}

// ArcadeHub owns every live room.
type ArcadeHub struct {
	mu        sync.Mutex
	rooms     map[string]*arcadeRoom
	jwtSecret string
	randCode  func() string
}

// NewArcadeHub constructs the hub and starts the idle-room sweeper.
func NewArcadeHub(jwtSecret string) *ArcadeHub {
	h := &ArcadeHub{
		rooms:     map[string]*arcadeRoom{},
		jwtSecret: jwtSecret,
		randCode:  defaultRoomCode,
	}
	go func() {
		for range time.Tick(time.Minute) {
			h.sweep(time.Now())
		}
	}()
	return h
}

func (h *ArcadeHub) sweep(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for code, room := range h.rooms {
		if len(room.members) == 0 || now.Sub(room.lastActive) > roomIdleTimeout {
			for _, m := range room.members {
				m.peer.deliver(map[string]any{"op": "error", "reason": "room_closed"})
				m.peer.kick()
			}
			delete(h.rooms, code)
		}
	}
}

// Unambiguous room-code alphabet (no 0/O, 1/I).
const roomAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func defaultRoomCode() string {
	var b [4]byte
	crandRead(b[:])
	out := make([]byte, 4)
	for i, v := range b {
		out[i] = roomAlphabet[int(v)%len(roomAlphabet)]
	}
	return string(out)
}

// createRoom reserves a seat 0 for the creator. Caller must NOT hold h.mu.
func (h *ArcadeHub) createRoom(game string, seats int, userID string, peer arcadePeer) (room *arcadeRoom, reason string) {
	limits, ok := arcadeNetGames[game]
	if !ok {
		return nil, "unknown_or_single_player_game"
	}
	if seats < limits.minSeats || seats > limits.maxSeats {
		return nil, "bad_seat_count"
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.rooms) >= maxArcadeRooms {
		return nil, "arcade_full"
	}
	var code string
	for range 20 {
		code = h.randCode()
		if _, taken := h.rooms[code]; !taken {
			break
		}
		code = ""
	}
	if code == "" {
		return nil, "arcade_full"
	}
	room = &arcadeRoom{
		code:       code,
		game:       game,
		seats:      seats,
		members:    map[int]*roomMember{0: {userID: userID, peer: peer}},
		lastActive: time.Now(),
	}
	h.rooms[code] = room
	return room, ""
}

// joinRoom seats a player in the lowest free seat. Caller must NOT hold h.mu.
func (h *ArcadeHub) joinRoom(code, userID string, peer arcadePeer) (*arcadeRoom, int, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	room, ok := h.rooms[code]
	if !ok {
		return nil, 0, "no_such_room"
	}
	if room.started {
		return nil, 0, "already_started"
	}
	seat := -1
	for s := 0; s < room.seats; s++ {
		if _, taken := room.members[s]; !taken {
			seat = s
			break
		}
	}
	if seat < 0 {
		return nil, 0, "room_full"
	}
	room.members[seat] = &roomMember{userID: userID, peer: peer}
	room.lastActive = time.Now()
	// Everyone (including the joiner) gets the fresh roster.
	for s, m := range room.members {
		m.peer.deliver(room.roomInfo(s))
	}
	return room, seat, ""
}

// startRoom locks the roster and tells every member the game is on.
func (h *ArcadeHub) startRoom(room *arcadeRoom, seat int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if seat != 0 {
		return "host_only"
	}
	if room.started {
		return "already_started"
	}
	limits := arcadeNetGames[room.game]
	if limits.minSeats == 2 && limits.maxSeats == 2 && len(room.members) < 2 {
		return "need_two_players" // chess/go cannot start short-handed
	}
	room.started = true
	room.lastActive = time.Now()
	for s, m := range room.members {
		m.peer.deliver(map[string]any{
			"op": "started", "seat": s, "seats": room.seats, "present": room.present(),
		})
	}
	return ""
}

// relay fans a game message out to every other member, stamping the sender's
// seat server-side so clients can never impersonate each other.
func (h *ArcadeHub) relay(room *arcadeRoom, seat int, data json.RawMessage) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !room.started {
		return "not_started"
	}
	room.lastActive = time.Now()
	room.broadcast(map[string]any{"op": "msg", "seat": seat, "data": data}, seat)
	return ""
}

// dropMember removes a connection from its room, notifying the remainder.
func (h *ArcadeHub) dropMember(room *arcadeRoom, seat int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := room.members[seat]; !ok {
		return
	}
	delete(room.members, seat)
	room.lastActive = time.Now()
	if len(room.members) == 0 {
		delete(h.rooms, room.code)
		return
	}
	if !room.started && seat == 0 {
		// Host abandoned the lobby: close it out.
		for _, m := range room.members {
			m.peer.deliver(map[string]any{"op": "error", "reason": "room_closed"})
			m.peer.kick()
		}
		delete(h.rooms, room.code)
		return
	}
	if room.started {
		room.broadcast(map[string]any{"op": "peer_left", "seat": seat}, -1)
	} else {
		for s, m := range room.members {
			m.peer.deliver(room.roomInfo(s))
		}
	}
}

// ---- transport ----

var arcadeUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Same-origin only: the page and the socket share a host.
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		return origin == "" || origin == "https://"+r.Host || origin == "http://"+r.Host
	},
}

type wsArcadePeer struct {
	conn *websocket.Conn
	out  chan []byte
	once sync.Once
}

func (p *wsArcadePeer) deliver(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	select {
	case p.out <- b:
	default:
		p.kick() // slow consumer: drop rather than block the room
	}
}

func (p *wsArcadePeer) kick() {
	p.once.Do(func() { close(p.out) })
}

func (p *wsArcadePeer) writer() {
	ping := time.NewTicker(wsPingEvery)
	defer ping.Stop()
	defer p.conn.Close()
	for {
		select {
		case b, ok := <-p.out:
			if !ok {
				_ = p.conn.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
				return
			}
			p.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)) //nolint:errcheck
			if err := p.conn.WriteMessage(websocket.TextMessage, b); err != nil {
				return
			}
		case <-ping.C:
			p.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)) //nolint:errcheck
			if err := p.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

type wsClientMsg struct {
	Op    string          `json:"op"`
	Token string          `json:"token,omitempty"`
	Game  string          `json:"game,omitempty"`
	Seats int             `json:"seats,omitempty"`
	Code  string          `json:"code,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// Serve upgrades the connection and runs the session. Authentication happens
// as the FIRST websocket message (browsers cannot set an Authorization header
// on a WebSocket), so this route is intentionally outside mw.Auth.
func (h *ArcadeHub) Serve(w http.ResponseWriter, r *http.Request) {
	conn, err := arcadeUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	peer := &wsArcadePeer{conn: conn, out: make(chan []byte, wsSendQueue)}
	go peer.writer()
	conn.SetReadLimit(wsMaxMsgBytes)
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsIdleDeadline))
	})

	fail := func(reason string) {
		peer.deliver(map[string]any{"op": "error", "reason": reason})
		time.Sleep(50 * time.Millisecond) // give the writer a beat to flush
		peer.kick()
	}

	// First message: auth, on a short deadline.
	conn.SetReadDeadline(time.Now().Add(wsAuthDeadline)) //nolint:errcheck
	var hello wsClientMsg
	if err := conn.ReadJSON(&hello); err != nil || hello.Op != "auth" {
		fail("auth_required")
		return
	}
	claims, err := auth.ParseToken(hello.Token, h.jwtSecret)
	if err != nil || !roleAtLeast(claims.Role, "member") {
		fail("forbidden")
		return
	}
	userID := claims.UserID
	peer.deliver(map[string]any{"op": "welcome"})

	var room *arcadeRoom
	seat := -1
	defer func() {
		if room != nil {
			h.dropMember(room, seat)
		}
		peer.kick()
	}()

	for {
		conn.SetReadDeadline(time.Now().Add(wsIdleDeadline)) //nolint:errcheck
		var msg wsClientMsg
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}
		switch msg.Op {
		case "create":
			if room != nil {
				fail("already_in_room")
				return
			}
			created, reason := h.createRoom(msg.Game, msg.Seats, userID, peer)
			if reason != "" {
				fail(reason)
				return
			}
			room, seat = created, 0
			peer.deliver(room.roomInfo(0))
		case "join":
			if room != nil {
				fail("already_in_room")
				return
			}
			joined, s, reason := h.joinRoom(msg.Code, userID, peer)
			if reason != "" {
				fail(reason)
				return
			}
			room, seat = joined, s
		case "start":
			if room == nil {
				fail("not_in_room")
				return
			}
			if reason := h.startRoom(room, seat); reason != "" {
				peer.deliver(map[string]any{"op": "error", "reason": reason})
			}
		case "msg":
			if room == nil {
				fail("not_in_room")
				return
			}
			if reason := h.relay(room, seat, msg.Data); reason != "" {
				peer.deliver(map[string]any{"op": "error", "reason": reason})
			}
		case "leave":
			return
		default:
			fail("unknown_op")
			return
		}
	}
}

// crandRead fills b from crypto/rand (rand.Read never fails on supported
// platforms; it panics internally if the OS entropy source is broken).
func crandRead(b []byte) {
	_, _ = crand.Read(b)
}
