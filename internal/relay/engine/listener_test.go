package engine

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/azuki774/khatru-redbean/internal/nostr"
	"github.com/stretchr/testify/require"
)

func idFromSeqUpper(seq int) string { return idFromSeq(seq, 65, 90) }
func idFromSeqLower(seq int) string { return idFromSeq(seq, 97, 122) }
func idFromSeq(seq int, min, max int) string {
	maxSeq := max - min + 1
	nLetters := seq/maxSeq + 1
	result := strings.Builder{}
	result.Grow(nLetters)
	for l := 0; l < nLetters; l++ {
		letter := rune(seq%maxSeq + min)
		result.WriteRune(letter)
	}
	return result.String()
}

func TestRandomListenerClientRemoving(t *testing.T) {
	rand := rand.New(rand.NewSource(1))
	rl := NewRelay()

	f := nostr.Filter{Kinds: []int{1}}
	cancel := func(cause error) {}

	websockets := make([]*WebSocket, 0, 20)

	l := 0

	for i := 0; i < 20; i++ {
		ws := &WebSocket{}
		websockets = append(websockets, ws)
		rl.clients[ws] = nil
	}

	for j := 0; j < 20; j++ {
		for i := 0; i < 20; i++ {
			ws := websockets[i]
			w := idFromSeqUpper(i)

			if rand.Intn(2) < 1 {
				l++
				rl.addListener(ws, w+":"+idFromSeqLower(j), rl, f, cancel)
			}
		}
	}

	require.Len(t, rl.clients, 20)
	require.Len(t, rl.listeners, l)

	for ws := range rl.clients {
		rl.removeClientAndListeners(ws)
	}

	require.Len(t, rl.clients, 0)
	require.Len(t, rl.listeners, 0)
}

func TestRandomListenerIdRemoving(t *testing.T) {
	rand := rand.New(rand.NewSource(1))
	rl := NewRelay()

	f := nostr.Filter{Kinds: []int{1}}
	cancel := func(cause error) {}

	websockets := make([]*WebSocket, 0, 20)

	type wsid struct {
		ws *WebSocket
		id string
	}

	subs := make([]wsid, 0, 20*20)
	extra := 0

	for i := 0; i < 20; i++ {
		ws := &WebSocket{}
		websockets = append(websockets, ws)
		rl.clients[ws] = nil
	}

	for j := 0; j < 20; j++ {
		for i := 0; i < 20; i++ {
			ws := websockets[i]
			w := idFromSeqUpper(i)

			if rand.Intn(2) < 1 {
				id := w + ":" + idFromSeqLower(j)
				rl.addListener(ws, id, rl, f, cancel)
				subs = append(subs, wsid{ws, id})

				if rand.Intn(5) < 1 {
					rl.addListener(ws, id, rl, f, cancel)
					extra++
				}
			}
		}
	}

	require.Len(t, rl.clients, 20)
	require.Len(t, rl.listeners, len(subs)+extra)

	rand.Shuffle(len(subs), func(i, j int) {
		subs[i], subs[j] = subs[j], subs[i]
	})
	for _, wsidToRemove := range subs {
		rl.removeListenerId(wsidToRemove.ws, wsidToRemove.id)
	}

	require.Len(t, rl.listeners, 0)
	require.Len(t, rl.clients, 20)
	for _, specs := range rl.clients {
		require.Len(t, specs, 0)
	}
}
