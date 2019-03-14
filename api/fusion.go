package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi"
	"github.com/gofrs/uuid"
	"github.com/gorilla/websocket"

	"github.com/wtg/shuttletracker/log"
	"github.com/wtg/shuttletracker/eta"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// Messages from clients must be in this envelope. Depending on Type, fusionManager
// unmarshals Message into the associated type of struct. fusionManager also uses
// this struct to send messages to clients.
type fusionMessageEnvelope struct {
	Type    string      `json:"type"`
	Message interface{} `json:"message"`
}

type fusionMessageSubscribe struct {
	Topic string `json:"topic"`
}

type fusionMessageUnsubscribe struct {
	Topic string `json:"topic"`
}

type fusionMessageETAs []eta.VehicleETA

type fusionPosition struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`

	// Meters per second. Yes, this is different from shuttletracker.Location,
	// which is in miles per hour...
	// It's a pointer because it's often unknown and therefore nil.
	Speed *float64 `json:"speed"`

	// Pointer because it may be unknown.
	Heading *float64 `json:"heading"`

	// Client-provided UUID that associates a list of positions to form a track.
	Track string `json:"track"`

	// Time is when fusionManager receives the position. We don't want to trust
	// the client's timestamp.
	Time time.Time `json:"time"`
}

type fusionBusButton struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type fusionClient struct {
	id              string
	conn            *websocket.Conn
	lastMessageTime time.Time
	userAgent       string
}

type clientMessage struct {
	clientID string
	msg      interface{}
}

type serverMessage struct {
	topic string
	msg interface{}
}

type fusionManagerDebug struct {
	// subscriptions    []sub
	clients        []fusionClient
	tracks         [][]fusionPosition
	busButtonCount uint64
}

type fusionManager struct {
	addClient    chan *fusionClient
	removeClient chan string

	clientMsg chan clientMessage
	serverMsg chan serverMessage

	// This is a little gnarly... basically we can ask fusionManager to send some
	// information about itself to a channel so that we don't have to put its internal
	// state behind a mutex to inspect it. No locks around maps or slices required.
	debug chan chan *fusionManagerDebug

	// Everything after this is considered internal state. Only fm.run will read
	// or modify these fields, and it is considered the owner of this state.

	// Clients can subscribe to topics that they're interested in. We only track
	// their IDs here.
	subscriptions map[string][]string

	clients        map[string]*fusionClient
	tracks         map[string][]fusionPosition
	busButtonCount uint64
}

func newFusionManager(etaManager *eta.ETAManager) *fusionManager {
	fm := &fusionManager{
		addClient:     make(chan *fusionClient),
		removeClient:  make(chan string),
		clientMsg:     make(chan clientMessage),
		serverMsg:     make(chan serverMessage),
		debug:         make(chan chan *fusionManagerDebug),
		clients:       map[string]*fusionClient{},
		tracks:        map[string][]fusionPosition{},
		subscriptions: map[string][]string{},
	}
	etaManager.Subscribe(fm.handleETA)
	go fm.run()
	return fm
}

// Select handle client connections, disconnections, and messages.
// Responsible (along with any methods it calls) for managing fusionManager state.
// Anything run calls should obtain the lock on fusionManager state.
func (fm *fusionManager) run() {
	for {
		select {
		case c := <-fm.addClient:
			fm.processAddClient(c)
		case clientID := <-fm.removeClient:
			fm.processRemoveClient(clientID)
		case cm := <-fm.clientMsg:
			fm.processMessage(cm)
		case sm := <-fm.serverMsg:
			fm.processServerMessage(sm)
		case debugChan := <-fm.debug:
			fm.processDebug(debugChan)
		}
	}
}

func (fm *fusionManager) sendToTopic(topic string, msg fusionMessageEnvelope) {
	sm := serverMessage{
		topic: topic,
		msg: msg,
	}
	fm.serverMsg <- sm
}

// this is a callback for ETAManager to inform Fusion to push out a new ETA
func (fm *fusionManager) handleETA(eta eta.VehicleETA) {
	fme := fusionMessageEnvelope{
		Type: "eta",
		Message: eta,
	}
	fm.sendToTopic("eta", fme)
}

func decodeFusionMessage(r io.Reader) (string, json.RawMessage, error) {
	var message json.RawMessage
	fm := fusionMessageEnvelope{
		Message: &message,
	}
	dec := json.NewDecoder(r)
	err := dec.Decode(&fm)
	if err != nil {
		return "", message, err
	}
	return fm.Type, message, nil
}

// Generate a UUID (v1, based on timestamp, since we don't care if it can be predicted;
// it just needs to be unique) and associate this client with it.
func (fm *fusionManager) processAddClient(client *fusionClient) {
	fm.clients[client.id] = client
	go fm.handleClient(client)
}

func (fm *fusionManager) processRemoveClient(clientID string) {
	// find all of this client's subscriptions and remove them
	for topic, subs := range fm.subscriptions {
		for i, subbedClient := range subs {
			if subbedClient == clientID {
				subs = append(subs[:i], subs[i+1:]...)
				fm.subscriptions[topic] = subs

				// we're done since handleMsgSubscribe doesn't let a client
				// subscribe more than once to the same topic
				break
			}
		}
	}

	// remove from clients
	delete(fm.clients, clientID)
}

// processMessage handles messages from clients after they are parsed. it does not
// need any locks or mutexes since it is only called from the goroutine that "owns"
// the state inside of fusionManager.
func (fm *fusionManager) processMessage(cm clientMessage) {
	switch t := cm.msg.(type) {
	case fusionMessageSubscribe:
		fms := cm.msg.(fusionMessageSubscribe)
		fm.handleMsgSubscribe(cm.clientID, fms)
	case fusionMessageUnsubscribe:
		fmu := cm.msg.(fusionMessageUnsubscribe)
		fm.handleMsgUnsubscribe(cm.clientID, fmu)
	case fusionPosition:
		fp := cm.msg.(fusionPosition)
		fm.handleMsgPosition(fp)
	case fusionBusButton:
		fbb := cm.msg.(fusionBusButton)
		fm.handleMsgBusButton(fbb)
	default:
		// This is an error since it means that an unhandled message type was sent to
		// the channel, probably by handleClient. This shouldn't happen, so please fix
		// it if it does (make sure all possible message types are being handled).
		log.Errorf("unknown message type \"%s\"", t)
	}
}

func (fm *fusionManager) processServerMessage(sm serverMessage) {
	b, err := json.Marshal(sm.msg)
	if err != nil {
		log.WithError(err).Error("unable to marshal")
		return
	}

	// find clients subscribed to topic
	for _, clientID := range fm.subscriptions[sm.topic] {
		client := fm.clients[clientID]
		err = client.conn.WriteMessage(websocket.TextMessage, b)
		if err != nil {
			log.WithError(err).Error("unable to write")
		}
	}
}

func (fm *fusionManager) handleMsgSubscribe(clientID string, fms fusionMessageSubscribe) {
	// grab the list of existing subscriptions
	subs := fm.subscriptions[fms.Topic]
	if subs == nil {
		// this is the first subscriber, so the list doesn't exist
		subs = []string{}
	}

	// if client is already subscribed, do nothing
	for _, subbedClient := range subs {
		if subbedClient == clientID {
			return
		}
	}

	subs = append(subs, clientID)
	fm.subscriptions[fms.Topic] = subs
}

func (fm *fusionManager) handleMsgUnsubscribe(clientID string, fmu fusionMessageUnsubscribe) {
	subs := fm.subscriptions[fmu.Topic]
	for i, subbedClient := range subs {
		if subbedClient == clientID {
			subs = append(subs[:i], subs[i+1:]...)
			fm.subscriptions[fmu.Topic] = subs

			// we're done since handleMsgSubscribe doesn't let a client
			// subscribe more than once to the same topic
			return
		}
	}
	log.Warnf("client requested unsubscribe from topic it's not subscribed to")
}

func (fm *fusionManager) handleMsgPosition(fp fusionPosition) {
	fp.Time = time.Now()
	fm.tracks[fp.Track] = append(fm.tracks[fp.Track], fp)
}

func (fm *fusionManager) handleMsgBusButton(fbb fusionBusButton) {
	fm.busButtonCount++
	fme := fusionMessageEnvelope{
		Type:    "bus_button",
		Message: fbb,
	}
	b, err := json.Marshal(fme)
	if err != nil {
		log.WithError(err).Error("unable to marshal")
		return
	}

	// find clients subscribed to topic
	for _, clientID := range fm.subscriptions["bus_button"] {
		client := fm.clients[clientID]
		err = client.conn.WriteMessage(websocket.TextMessage, b)
		if err != nil {
			log.WithError(err).Error("unable to write")
		}
	}
}

// handleClient is expected to be called inside of a goroutine associated with a client.
// It does not directly manipulate fusionManager state—this is done by sending messages
// through a chan that is read elsewhere. We do as much JSON parsing here as possible
// since each connection is handled concurrently.
func (fm *fusionManager) handleClient(client *fusionClient) {
	for {
		_, r, err := client.conn.NextReader()
		if err != nil {
			// did the client e.g. close the tab? then we expect a normal error
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway) {
				log.WithError(err).Error("unable to get reader")
			}
			break
		}
		client.lastMessageTime = time.Now()
		messageType, message, err := decodeFusionMessage(r)
		if err != nil {
			log.WithError(err).Error("unable to decode message")
			continue
		}

		switch messageType {
		case "subscribe":
			fms := fusionMessageSubscribe{}
			err = json.Unmarshal(message, &fms)
			if err != nil {
				log.WithError(err).Error("unable to decode fusionMessageSubscribe")
				break
			}
			fm.clientMsg <- clientMessage{client.id, fms}
		case "unsubscribe":
			fmu := fusionMessageUnsubscribe{}
			err = json.Unmarshal(message, &fmu)
			if err != nil {
				log.WithError(err).Error("unable to decode fusionMessageUnsubscribe")
				break
			}
			fm.clientMsg <- clientMessage{client.id, fmu}
		case "position":
			fp := fusionPosition{}
			err = json.Unmarshal(message, &fp)
			if err != nil {
				log.WithError(err).Error("unable to decode fusionPosition")
				break
			}
			fp.Time = time.Now()
			fm.clientMsg <- clientMessage{client.id, fp}
		case "bus_button":
			fbb := fusionBusButton{}
			err = json.Unmarshal(message, &fbb)
			if err != nil {
				log.WithError(err).Error("unable to decode fusionBusButton")
				break
			}
			fm.clientMsg <- clientMessage{client.id, fbb}
		default:
			// This is just a warning and not an error since messageType comes straight
			// from the client. We can't trust it.
			log.WithError(err).Warnf("unknown message type \"%s\"", messageType)
		}
	}

	// remove client since the connection is dead
	fm.removeClient <- client.id
}

func (fm *fusionManager) processDebug(ch chan *fusionManagerDebug) {
	// assemble the data...
	debug := &fusionManagerDebug{
		clients:        make([]fusionClient, 0, len(fm.clients)),
		tracks:         make([][]fusionPosition, 0, len(fm.tracks)),
		busButtonCount: fm.busButtonCount,
	}

	for _, v := range fm.clients {
		newClient := fusionClient{
			// don't copy the websocket conn
			id:              v.id,
			lastMessageTime: v.lastMessageTime,
			userAgent:       v.userAgent,
		}
		debug.clients = append(debug.clients, newClient)
	}

	for _, v := range fm.tracks {
		newTrack := make([]fusionPosition, len(v))
		copy(newTrack, v)
		debug.tracks = append(debug.tracks, newTrack)
	}

	// send it 📬
	ch <- debug
}

func (fm *fusionManager) debugInfo() *fusionManagerDebug {
	copyChan := make(chan *fusionManagerDebug)
	fm.debug <- copyChan
	return <-copyChan
}

// debugHandler gets a copy of fusionManager's state and then writes some interesting
// information to an HTTP request. This handler (and any modifications you're thinking
// of making to it) MUST NOT perform any operations on fusionManager's state. In order
// to avoid data races, use the copy.
func (fm *fusionManager) debugHandler(w http.ResponseWriter, r *http.Request) {
	_, err := fmt.Fprint(w, "fusionManager debug\n\n")
	if err != nil {
		log.WithError(err).Error("unable to write response")
		return
	}

	// ask fusionManager for debug info
	fmDebug := fm.debugInfo()

	_, err = fmt.Fprintf(w, "%d tracks\n", len(fmDebug.tracks))
	if err != nil {
		log.WithError(err).Error("unable to write response")
		return
	}

	numPositions := 0
	for _, track := range fmDebug.tracks {
		numPositions += len(track)
	}
	_, err = fmt.Fprintf(w, "%d positions\n", numPositions)
	if err != nil {
		log.WithError(err).Error("unable to write response")
		return
	}

	_, err = fmt.Fprintf(w, "%d bus buttons\n\n", fmDebug.busButtonCount)
	if err != nil {
		log.WithError(err).Error("unable to write response")
		return
	}

	_, err = fmt.Fprintf(w, "%d clients:\n", len(fmDebug.clients))
	if err != nil {
		log.WithError(err).Error("unable to write response")
		return
	}
	for _, client := range fmDebug.clients {
		_, err = fmt.Fprintf(w, "%s\t%s\n", client.lastMessageTime.Format(time.RFC3339), client.userAgent)
		if err != nil {
			log.WithError(err).Error("unable to write response")
			return
		}
	}
}

func (fm *fusionManager) exportHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	fmDebug := fm.debugInfo()
	err := enc.Encode(fmDebug.tracks)
	if err != nil {
		log.WithError(err).Error("unable to encode")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (fm *fusionManager) webSocketHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.WithError(err).Error("unable to upgrade connection")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	u1, err := uuid.NewV1()
	if err != nil {
		log.WithError(err).Error("unable to generate UUID")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	c := &fusionClient{
		id:              u1.String(),
		conn:            conn,
		lastMessageTime: time.Now(),
		userAgent:       r.UserAgent(),
	}
	fm.addClient <- c
}
func (fm *fusionManager) router(auth func(http.Handler) http.Handler) http.Handler {
	r := chi.NewRouter()
	r.HandleFunc("/", fm.webSocketHandler)
	r.With(auth).Get("/debug", fm.debugHandler)
	r.With(auth).Get("/export", fm.exportHandler)
	return r
}
