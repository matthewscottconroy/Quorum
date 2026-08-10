package handler

import (
	"encoding/json"
	"testing"
	"time"
)

// fakePeer records everything delivered to it.
type fakePeer struct {
	msgs   []map[string]any
	kicked bool
}

func (p *fakePeer) deliver(v any) {
	b, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	p.msgs = append(p.msgs, m)
}
func (p *fakePeer) kick() { p.kicked = true }

func (p *fakePeer) last() map[string]any {
	if len(p.msgs) == 0 {
		return nil
	}
	return p.msgs[len(p.msgs)-1]
}

func testHub() *ArcadeHub {
	n := 0
	return &ArcadeHub{
		rooms:     map[string]*arcadeRoom{},
		jwtSecret: "irrelevant-for-room-logic",
		randCode: func() string {
			n++
			return []string{"AAAA", "BBBB", "CCCC"}[n%3]
		},
	}
}

func TestArcadeRoom_CreateJoinSeatsInOrder(t *testing.T) {
	h := testHub()
	host := &fakePeer{}
	room, reason := h.createRoom("hexfection", 4, "u-host", host)
	if reason != "" {
		t.Fatalf("create: %s", reason)
	}
	if room.code == "" || room.seats != 4 {
		t.Fatalf("room: %+v", room)
	}
	g1, g2 := &fakePeer{}, &fakePeer{}
	_, s1, r1 := h.joinRoom(room.code, "u-1", g1)
	_, s2, r2 := h.joinRoom(room.code, "u-2", g2)
	if r1 != "" || r2 != "" {
		t.Fatalf("join: %s %s", r1, r2)
	}
	if s1 != 1 || s2 != 2 {
		t.Errorf("seats: got %d, %d", s1, s2)
	}
	// Everyone got a roster update listing three present seats.
	if got := g2.last()["present"].([]any); len(got) != 3 {
		t.Errorf("present: %v", got)
	}
}

func TestArcadeRoom_TwoSeatGamesRefuseShortStartAndOverfill(t *testing.T) {
	h := testHub()
	host := &fakePeer{}
	room, _ := h.createRoom("chess", 2, "u-host", host)
	if reason := h.startRoom(room, 0); reason != "need_two_players" {
		t.Errorf("short start: got %q", reason)
	}
	h.joinRoom(room.code, "u-1", &fakePeer{})
	if _, _, reason := h.joinRoom(room.code, "u-2", &fakePeer{}); reason != "room_full" {
		t.Errorf("overfill: got %q", reason)
	}
	if reason := h.startRoom(room, 0); reason != "" {
		t.Errorf("start: got %q", reason)
	}
}

func TestArcadeRoom_StartIsHostOnlyAndLocksJoins(t *testing.T) {
	h := testHub()
	room, _ := h.createRoom("powder-keg", 6, "u-host", &fakePeer{})
	h.joinRoom(room.code, "u-1", &fakePeer{})
	if reason := h.startRoom(room, 1); reason != "host_only" {
		t.Errorf("guest start: got %q", reason)
	}
	if reason := h.startRoom(room, 0); reason != "" {
		t.Errorf("host start: got %q", reason)
	}
	if _, _, reason := h.joinRoom(room.code, "u-2", &fakePeer{}); reason != "already_started" {
		t.Errorf("late join: got %q", reason)
	}
}

func TestArcadeRoom_RelayStampsSeatAndSkipsSender(t *testing.T) {
	h := testHub()
	host := &fakePeer{}
	room, _ := h.createRoom("go", 2, "u-host", host)
	guest := &fakePeer{}
	h.joinRoom(room.code, "u-1", guest)
	h.startRoom(room, 0)

	before := len(host.msgs)
	if reason := h.relay(room, 1, json.RawMessage(`{"t":"mv","pos":40}`)); reason != "" {
		t.Fatalf("relay: %s", reason)
	}
	if len(host.msgs) != before+1 {
		t.Fatalf("host should receive exactly one relayed message")
	}
	got := host.last()
	if got["op"] != "msg" || got["seat"].(float64) != 1 {
		t.Errorf("stamped relay: %v", got)
	}
	// The sender must not receive an echo.
	for _, m := range guest.msgs {
		if m["op"] == "msg" {
			t.Errorf("sender received its own relay: %v", m)
		}
	}
}

func TestArcadeRoom_RelayRefusedBeforeStart(t *testing.T) {
	h := testHub()
	room, _ := h.createRoom("chess", 2, "u-host", &fakePeer{})
	if reason := h.relay(room, 0, json.RawMessage(`{}`)); reason != "not_started" {
		t.Errorf("got %q", reason)
	}
}

func TestArcadeRoom_HostLeavingLobbyClosesRoom(t *testing.T) {
	h := testHub()
	host := &fakePeer{}
	room, _ := h.createRoom("hexfection", 3, "u-host", host)
	guest := &fakePeer{}
	h.joinRoom(room.code, "u-1", guest)
	h.dropMember(room, 0)
	if !guest.kicked {
		t.Error("guest should be kicked when the lobby host leaves")
	}
	if len(h.rooms) != 0 {
		t.Error("room should be gone")
	}
}

func TestArcadeRoom_MidGameLeaveNotifiesEveryone(t *testing.T) {
	h := testHub()
	host := &fakePeer{}
	room, _ := h.createRoom("powder-keg", 3, "u-host", host)
	g1, g2 := &fakePeer{}, &fakePeer{}
	h.joinRoom(room.code, "u-1", g1)
	h.joinRoom(room.code, "u-2", g2)
	h.startRoom(room, 0)
	h.dropMember(room, 1)
	for _, p := range []*fakePeer{host, g2} {
		got := p.last()
		if got["op"] != "peer_left" || got["seat"].(float64) != 1 {
			t.Errorf("expected peer_left seat 1, got %v", got)
		}
	}
}

func TestArcadeRoom_DroppedPlayerReclaimsOwnSeatMidGame(t *testing.T) {
	h := testHub()
	host, g1 := &fakePeer{}, &fakePeer{}
	room, _ := h.createRoom("powder-keg", 4, "u-host", host)
	_, s1, _ := h.joinRoom(room.code, "u-guest", g1)
	if reason := h.startRoom(room, 0); reason != "" {
		t.Fatalf("start: %s", reason)
	}
	h.dropMember(room, s1)
	if _, still := room.members[s1]; still {
		t.Fatal("dropped member should be out of the roster")
	}

	// A stranger cannot take the vacated seat.
	stranger := &fakePeer{}
	if _, _, reason := h.joinRoom(room.code, "u-somebody-else", stranger); reason != "already_started" {
		t.Errorf("stranger join: got %q, want already_started", reason)
	}

	// The same member reclaims exactly their old seat and lands in-game.
	back := &fakePeer{}
	rejoined, seat, reason := h.joinRoom(room.code, "u-guest", back)
	if reason != "" || rejoined == nil || seat != s1 {
		t.Fatalf("reclaim: seat=%d reason=%q", seat, reason)
	}
	last := back.last()
	if last["op"] != "started" || last["rejoined"] != true {
		t.Errorf("rejoiner should be booted straight into the running game, got %v", last)
	}
	// Seat is spent: a second reclaim attempt (stale tab) is refused.
	again := &fakePeer{}
	if _, _, reason := h.joinRoom(room.code, "u-guest", again); reason != "already_started" {
		t.Errorf("double reclaim: got %q, want already_started", reason)
	}
}

func TestHoldemDealer_PrivateHolesPublicBoard(t *testing.T) {
	h := testHub()
	host, g1, g2 := &fakePeer{}, &fakePeer{}, &fakePeer{}
	room, _ := h.createRoom("texas-holdem", 3, "u-host", host)
	h.joinRoom(room.code, "u-1", g1)
	h.joinRoom(room.code, "u-2", g2)
	if reason := h.startRoom(room, 0); reason != "" {
		t.Fatalf("start: %s", reason)
	}
	// Only the acting host paces the dealer.
	if reason := h.holdemOp(room, 1, "deal"); reason == "" {
		t.Fatal("guest must not be able to deal")
	}
	if reason := h.holdemOp(room, 0, "deal"); reason != "" {
		t.Fatalf("deal: %s", reason)
	}
	// Each member privately received exactly their own two cards.
	seen := map[int]bool{}
	for _, p := range []*fakePeer{host, g1, g2} {
		var cards []any
		var dealt int
		for _, m := range p.msgs {
			switch m["op"] {
			case "cards":
				cards = m["hole"].([]any)
			case "dealt":
				dealt++
			}
		}
		if len(cards) != 2 {
			t.Fatalf("every seat gets two hole cards, got %v", cards)
		}
		if dealt != 1 {
			t.Errorf("every seat hears the public deal notice once, got %d", dealt)
		}
		for _, c := range cards {
			card := int(c.(float64))
			if card < 0 || card > 51 || seen[card] {
				t.Errorf("card %d out of range or dealt twice", card)
			}
			seen[card] = true
		}
	}
	// Streets: 3, 1, 1, then the deck is closed.
	for i, want := range []int{3, 1, 1} {
		if reason := h.holdemOp(room, 0, "street"); reason != "" {
			t.Fatalf("street %d: %s", i, reason)
		}
		last := host.last()
		if last["op"] != "board" || len(last["cards"].([]any)) != want {
			t.Fatalf("street %d: want %d cards, got %v", i, want, last)
		}
	}
	if reason := h.holdemOp(room, 0, "street"); reason == "" {
		t.Error("a sixth board card must be refused")
	}
	// Reveal shows every dealt hand to everyone — but only after the river.
	if reason := h.holdemOp(room, 0, "reveal"); reason != "" {
		t.Fatalf("reveal after river: %s", reason)
	}
	holes := g1.last()["holes"].(map[string]any)
	if len(holes) != 3 {
		t.Errorf("reveal covers all three dealt seats, got %v", holes)
	}
}

func TestHoldemDealer_NoEarlyReveal(t *testing.T) {
	h := testHub()
	host, g1 := &fakePeer{}, &fakePeer{}
	room, _ := h.createRoom("texas-holdem", 2, "u-host", host)
	h.joinRoom(room.code, "u-1", g1)
	h.startRoom(room, 0)
	h.holdemOp(room, 0, "deal")
	if reason := h.holdemOp(room, 0, "reveal"); reason != "river_first" {
		t.Errorf("pre-river reveal must be refused, got %q", reason)
	}
	h.holdemOp(room, 0, "street") // flop
	if reason := h.holdemOp(room, 0, "reveal"); reason != "river_first" {
		t.Errorf("pre-river reveal must be refused on the flop too, got %q", reason)
	}
}

func TestArcadeRoom_SweepClosesIdleRooms(t *testing.T) {
	h := testHub()
	host := &fakePeer{}
	room, _ := h.createRoom("chess", 2, "u-host", host)
	room.lastActive = time.Now().Add(-time.Hour)
	h.sweep(time.Now())
	if !host.kicked {
		t.Error("idle room member should be kicked")
	}
	if len(h.rooms) != 0 {
		t.Error("idle room should be deleted")
	}
}

func TestArcadeRoom_UnknownGameAndBadSeats(t *testing.T) {
	h := testHub()
	if _, reason := h.createRoom("brickfall", 2, "u", &fakePeer{}); reason == "" {
		t.Error("single-player cabinet must refuse rooms")
	}
	if _, reason := h.createRoom("chess", 3, "u", &fakePeer{}); reason == "" {
		t.Error("chess with 3 seats must refuse")
	}
	if _, reason := h.createRoom("powder-keg", 13, "u", &fakePeer{}); reason == "" {
		t.Error("13 seats must refuse")
	}
}
