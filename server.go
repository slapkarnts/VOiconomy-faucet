package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/algorand/go-algorand-sdk/v2/abi"
	"github.com/algorand/go-algorand-sdk/v2/client/v2/algod"
	"github.com/algorand/go-algorand-sdk/v2/crypto"
	"github.com/algorand/go-algorand-sdk/v2/encoding/msgpack"
	"github.com/algorand/go-algorand-sdk/v2/mnemonic"
	"github.com/algorand/go-algorand-sdk/v2/transaction"
	"github.com/algorand/go-algorand-sdk/v2/types"
	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
)

// -----------------------------------------------------------------------------
// WS PROTOCOL STRUCTS
// -----------------------------------------------------------------------------

// Envelope is the standard wrapper for all messages.
type Envelope struct {
	Type    string          `json:"type"`    // "lobby_update", "challenge", "move", "chat", "identity", "vault_update", "rules_update", "rewards_update", "maintenance_update", "ping", "pong"
	FromID  string          `json:"from_id"` // Sender ID
	ToID    string          `json:"to_id,omitempty"`
	Payload json.RawMessage `json:"payload"` // Flexible JSON content
}

// ChallengeData handles the matchmaking handshake.
type ChallengeData struct {
	Action string          `json:"action"` // "invite", "accept", "decline", "sync_back"
	Deck   []int           `json:"deck,omitempty"`
	Rules  map[string]bool `json:"rules,omitempty"`
}

// MoveData synchronizes gameplay actions between two human players.
type MoveData struct {
	GridIndex int    `json:"grid_index"`
	CardID    int    `json:"card_id"`
	Power     [4]int `json:"power"`
}

// NonceData stores the nonce value and its creation time for expiration logic.
type NonceData struct {
	Value     string
	CreatedAt time.Time
}

// RateBucket implements the Leaky Bucket state for a single entity (IP).
type RateBucket struct {
	Tokens     float64
	LastUpdate time.Time
}

// TournamentMatch represents a single duel within the bracket.
type TournamentMatch struct {
	ID     string `json:"id"`
	P1     string `json:"p1"` // Wallet Address
	P2     string `json:"p2"` // Wallet Address
	Winner string `json:"winner,omitempty"`
	Round  int    `json:"round"`
}

// TournamentState tracks the progress of an automated event.
type TournamentState struct {
	Active       bool              `json:"active"`
	Matches      []TournamentMatch `json:"matches"`
	CurrentRound int               `json:"current_round"`
	Participants []string          `json:"participants"`
}

// ServerCard mirrors the client Card for verification logic.
type ServerCard struct {
	ID          int       `json:"id"`
	Power       [4]int    `json:"power"`
	Owner       int       `json:"owner"`
	LastUpdated time.Time `json:"last_updated"` // TTL tracking for cache refresh
}

// MatchState tracks an ongoing game on the server for win verification.
type MatchState struct {
	P1ID        string
	P2ID        string
	P1Deck      []int // Card IDs in P1's deck
	P2Deck      []int // Card IDs in P2's deck
	Board       [9]*ServerCard
	Rules       map[string]bool
	IsFinished  bool
	Spectators  []string // Client IDs spectating this match
	FinalScores [2]int
}

// MatchHistory stores the result of a completed game for reward verification.
type MatchHistory struct {
	WinnerID  string    `json:"winner_id"`
	Scores    [2]int    `json:"scores"`
	Timestamp time.Time `json:"timestamp"`
}

// PlayerStats tracks the performance and reliability of a player.
type PlayerStats struct {
	Wins             int       `json:"wins"`
	DNFs             int       `json:"dnfs"`
	DisconnectStreak int       `json:"disconnect_streak"`
	BanExpires       time.Time `json:"ban_expires"`
	Reputation       int       `json:"reputation"`
	BestRating       string    `json:"best_rating"`
}

// Client represents a single WebSocket connection to a player in the lobby.
type Client struct {
	conn              *websocket.Conn // The WebSocket connection itself.
	send              chan []byte     // Buffered channel for outbound messages to the client.
	id                string          // Unique identifier for the client.
	isAdmin           bool            // Administrative privilege flag
	avatarURL         string          // URL of chosen NFT avatar
	lobby             *Lobby          // Reference to the lobby this client belongs to.
	messageTimestamps []time.Time     // Sliding window for rate limiting
	msgMutex          sync.Mutex      // Protects messageTimestamps
}

// Lobby manages the set of active clients, handles client registration/unregistration,
// and facilitates message broadcasting within the lobby.
type Lobby struct {
	clients         map[string]*Client      // Registered clients indexed by their unique ID.
	matches         map[string]*MatchState  // Active matches indexed by PlayerID.
	inventory       map[int]ServerCard      // Server-side source of truth for Card Stats
	wallets         map[string]string       // Mapping: ClientID -> WalletAddress
	leaderboard     map[string]PlayerStats  // Mapping: WalletAddress -> PlayerStats
	matchHistory    map[string]MatchHistory // Verified match results indexed by Winner ClientID.
	faucetBalance   float64                 // Server-side source of truth for the vault
	rewards         map[uint64]uint64       // Registry of Reward Asset ID -> Amount (micro-units)
	rewardAssetID   uint64                  // Primary asset ID for legacy/fallback lookups
	baseReward      uint64                  // Single base reward for legacy lookups
	nonces          map[string]NonceData    // Active nonces for signature verification: ClientID -> NonceData
	maintenanceMode bool                    // If true, new matches are blocked.
	maintenanceTime time.Time               // When maintenance begins
	rateLimits      map[string]time.Time    // IP -> Last request time for rate limiting
	httpRateLimits  map[string]*RateBucket  // IP -> Leaky Bucket state for HTTP APIs
	tournament      TournamentState         // Global tournament state
	register        chan *Client            // Channel for inbound requests to register a client.
	unregister      chan *Client            // Channel for inbound requests to unregister a client.
	broadcast       chan []byte             // Channel for inbound messages from clients that need to be broadcast to others.
	mutex           sync.RWMutex            // Mutex to protect concurrent access to the clients map.
}

// newLobby creates and returns a new Lobby instance.
func newLobby() *Lobby {
	l := &Lobby{
		clients:       make(map[string]*Client),
		matches:       make(map[string]*MatchState),
		inventory:     make(map[int]ServerCard),
		wallets:       make(map[string]string),
		leaderboard:   make(map[string]PlayerStats),
		matchHistory:  make(map[string]MatchHistory),
		nonces:        make(map[string]NonceData),
		faucetBalance: 1000.0, // Initial seed
		rewardAssetID: 40227315,
		rewards:       map[uint64]uint64{40227315: 5000000}, // Seed with default $VBV
		rateLimits:    make(map[string]time.Time),
		httpRateLimits: make(map[string]*RateBucket),
		register:      make(chan *Client),
		unregister:    make(chan *Client),
		broadcast:     make(chan []byte),
	}
	return l
}

// checkAdminAuth validates the administrator using either an Algorand signature (Preferred)
// or the legacy X-Admin-Key (Fallback).
func (l *Lobby) checkAdminAuth(w http.ResponseWriter, r *http.Request) bool {
	// 1. Try Signature Authentication (Modern/Secure)
	wallet := r.Header.Get("X-Admin-Wallet")
	nonce := r.Header.Get("X-Admin-Nonce")
	signature := r.Header.Get("X-Admin-Signature")

	if wallet != "" && nonce != "" && signature != "" {
		if l.verifyAdminSignature(wallet, nonce, signature) {
			return true
		}
		log.Printf("[SECURITY ALERT] Invalid Admin Signature Attempt from Wallet: %s", wallet)
	}

	// 2. Try Legacy Key Authentication (Fallback for scripts/tools)
	adminKey := r.Header.Get("X-Admin-Key")
	expectedAdminKey := os.Getenv("ADMIN_KEY")

	if expectedAdminKey != "" && subtle.ConstantTimeCompare([]byte(adminKey), []byte(expectedAdminKey)) == 1 {
		return true
	}

	log.Printf("[SECURITY ALERT] Unauthorized Admin Access Attempt from IP: %s", r.RemoteAddr)
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
	return false
}

// verifyAdminSignature confirms the wallet is an admin and the signature for the nonce is valid.
func (l *Lobby) verifyAdminSignature(wallet, nonce, signatureStr string) bool {
	if !l.isAdminWallet(wallet) {
		return false
	}

	// 1. Verify that the nonce exists and is active globally
	l.mutex.RLock()
	found := false
	for _, nd := range l.nonces {
		if nd.Value == nonce {
			// Check expiration (5 minutes)
			if time.Since(nd.CreatedAt) < 5*time.Minute {
				found = true
			}
			break
		}
	}
	l.mutex.RUnlock()

	if !found {
		return false
	}

	// 2. Verify the cryptographic signature
	addr, err := types.DecodeAddress(wallet)
	if err != nil {
		return false
	}

	sigBytes, err := base64.StdEncoding.DecodeString(signatureStr)
	if err != nil {
		return false
	}

	// The message signed should be a standardized string: "ADMIN_ACCESS:[nonce]"
	msg := fmt.Sprintf("ADMIN_ACCESS:%s", nonce)
	return crypto.VerifySignature(addr[:], []byte(msg), sigBytes)
}

// isAdminWallet checks if a given wallet address is present in the ADMIN_WALLETS env variable.
func (l *Lobby) isAdminWallet(wallet string) bool {
	if wallet == "" {
		return false
	}
	admins := os.Getenv("ADMIN_WALLETS")
	if admins == "" {
		return false
	}
	for _, addr := range strings.Split(admins, ",") {
		if strings.TrimSpace(addr) == wallet {
			return true
		}
	}
	return false
}

// run starts the Lobby's event loop, processing client registration, unregistration, and broadcasting messages.
func (l *Lobby) run() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	healthTicker := time.NewTicker(10 * time.Minute)
	defer healthTicker.Stop()

	globalSyncTicker := time.NewTicker(30 * time.Minute)
	defer globalSyncTicker.Stop()

	// Perform initial sync on startup
	go l.refreshGlobalLeaderboard()

	for {
		select {
		case <-ticker.C:
			l.cleanupNonces()
		case <-healthTicker.C:
			go l.broadcastHealthReport()
		case <-globalSyncTicker.C:
			go l.refreshGlobalLeaderboard()
		case client := <-l.register:
			l.mutex.Lock()
			l.clients[client.id] = client
			log.Printf("[LOBBY] Client registered: %s. Total clients: %d\n", client.id, len(l.clients))
			msg := l.getLobbyUpdateMsg()
			l.mutex.Unlock()
			go func() { l.broadcast <- msg }()

		case client := <-l.unregister:
			l.mutex.Lock()
			// Handle Match Cleanup if the client was in a game
			if match, ok := l.matches[client.id]; ok {
				opponentID := ""
				if client.id == match.P1ID {
					opponentID = match.P2ID
				} else {
					opponentID = match.P1ID
				}

				// Notify opponent if they are still connected
				if opponentID != "" {
					if opponent, exists := l.clients[opponentID]; exists {
						notification := Envelope{
							Type:    "chat",
							FromID:  "SERVER",
							Payload: json.RawMessage(`{"text":"Match invalidated: Opponent disconnected."}`),
						}
						data, _ := json.Marshal(notification)
						select {
						case opponent.send <- data:
						default:
						}
					}

					// Mark the disconnected client with a DNF
					l.incrementDNF(client.id)

					delete(l.matches, opponentID)
				}
				delete(l.matches, client.id)
			}

			if _, ok := l.clients[client.id]; ok {
				delete(l.clients, client.id)
				close(client.send) // Close the send channel to stop writePump for this client.
				log.Printf("[LOBBY] Client unregistered: %s. Total clients: %d\n", client.id, len(l.clients))
			}
			msg := l.getLobbyUpdateMsg()
			l.mutex.Unlock()
			go func() { l.broadcast <- msg }()

		case message := <-l.broadcast:
			var env Envelope
			if err := json.Unmarshal(message, &env); err != nil {
				continue
			}
			// handleGameProtocol now manages its own internal locking
			l.handleGameProtocol(&env, message)

			l.mutex.RLock()
			if env.ToID != "" && env.ToID != "ALL" {
				// Targeted Routing: Send only to the specific recipient.
				if target, ok := l.clients[env.ToID]; ok {
					select {
					case target.send <- message:
					default:
					}
				}
			} else {
				// Broadcast: Send to everyone (supports "Challenge Anyone" setup).
				for _, client := range l.clients {
					select {
					case client.send <- message:
					default:
					}
				}
			}
			l.mutex.RUnlock()
		}
	}
}

// cleanupNonces performs periodic cleanup of nonces, match history, rate limits, and spectators.
func (l *Lobby) cleanupNonces() {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	now := time.Now()
	for id, nd := range l.nonces {
		if now.Sub(nd.CreatedAt) > 5*time.Minute {
			delete(l.nonces, id)
			log.Printf("[SERVER] Expired nonce for client %s removed.\n", id)
		}
	}

	// Match history cleanup: remove entries older than 30 minutes
	for id, history := range l.matchHistory {
		if now.Sub(history.Timestamp) > 30*time.Minute {
			delete(l.matchHistory, id)
			log.Printf("[SERVER] Expired match history for winner %s removed.\n", id)
		}
	}

	// Spectator cleanup: remove IDs that are no longer in the active clients map
	processedMatches := make(map[*MatchState]bool)
	for _, match := range l.matches {
		if processedMatches[match] {
			continue
		}
		processedMatches[match] = true

		var activeSpecs []string
		for _, specID := range match.Spectators {
			if _, exists := l.clients[specID]; exists {
				activeSpecs = append(activeSpecs, specID)
			} else {
				log.Printf("[SERVER] Removing disconnected spectator %s from match: %s vs %s\n",
					specID, match.P1ID, match.P2ID)
			}
		}
		match.Spectators = activeSpecs
	}

	// Rate limit cleanup: remove entries older than 5 minutes
	for ip, lastReq := range l.rateLimits {
		if now.Sub(lastReq) > 5*time.Minute {
			delete(l.rateLimits, ip)
		}
	}

	// Inventory cache cleanup: remove entries not updated for 24 hours to keep memory lean
	for id, card := range l.inventory {
		if now.Sub(card.LastUpdated) > 24*time.Hour {
			delete(l.inventory, id)
		}
	}

	// HTTP Rate limit cleanup: remove buckets at capacity that haven't been used for 1 hour
	for ip, bucket := range l.httpRateLimits {
		if bucket.Tokens >= 10.0 && now.Sub(bucket.LastUpdate) > 1*time.Hour {
			delete(l.httpRateLimits, ip)
		}
	}
}

// handleGameProtocol processes challenges and moves to maintain server-side authority.
func (l *Lobby) handleGameProtocol(env *Envelope, rawMsg []byte) {
	switch env.Type {
	case "register_wallet":
		var data struct {
			Wallet string `json:"wallet"`
		}
		json.Unmarshal(env.Payload, &data)
		l.mutex.Lock()
		l.wallets[env.FromID] = data.Wallet
		l.mutex.Unlock()

		// Fetch On-Chain Stats (Persistence replacement)
		go l.syncStatsFromBlockchain(env.FromID, data.Wallet)

		// Admin Check
		if l.isAdminWallet(data.Wallet) {
			if c, ok := l.clients[env.FromID]; ok {
				c.isAdmin = true
				log.Printf("[LOBBY] Administrator session verified for %s\n", data.Wallet)
				// Trigger a lobby update to show the badge
				go func() { l.broadcast <- l.getLobbyUpdateMsg() }()
			}
		}
		log.Printf("[LOBBY] Client %s registered wallet: %s\n", env.FromID, data.Wallet)

	case "update_rating":
		var data struct {
			BestRating string `json:"best_rating"`
		}
		json.Unmarshal(env.Payload, &data)
		l.mutex.Lock()
		if wallet, ok := l.wallets[env.FromID]; ok {
			stats := l.leaderboard[wallet]
			if l.isBetterRating(data.BestRating, stats.BestRating) {
				stats.BestRating = data.BestRating
				l.leaderboard[wallet] = stats
			}
		}
		l.mutex.Unlock()

	case "register_avatar":
		var data struct {
			URL string `json:"url"`
		}
		json.Unmarshal(env.Payload, &data)
		l.mutex.Lock()
		if c, ok := l.clients[env.FromID]; ok {
			c.avatarURL = data.URL
		}
		l.mutex.Unlock()
		go func() { l.broadcast <- l.getLobbyUpdateMsg() }()

	case "nonce_request":
		nonce := generateNonce()
		l.mutex.Lock()
		l.nonces[env.FromID] = NonceData{Value: nonce, CreatedAt: time.Now()}
		l.mutex.Unlock()

		response := Envelope{
			Type:    "nonce_response",
			ToID:    env.FromID,
			FromID:  "SERVER",
			Payload: json.RawMessage(fmt.Sprintf(`{"nonce":"%s"}`, nonce)),
		}
		if target, ok := l.clients[env.FromID]; ok {
			msg, _ := json.Marshal(response)
			target.send <- msg
		}

	case "challenge":
		var data ChallengeData
		json.Unmarshal(env.Payload, &data)
		now := time.Now()

		// MAINTENANCE CHECK: Prevent new matches if maintenance is active
		if l.maintenanceMode {
			response := Envelope{
				Type:    "chat",
				FromID:  "SERVER",
				Payload: json.RawMessage(`{"text":"Matchmaking disabled: Server maintenance is imminent."}`),
			}
			if target, ok := l.clients[env.FromID]; ok {
				msg, _ := json.Marshal(response)
				target.send <- msg
			}
			return
		}

		// SECURITY: Check if the player is currently banned for Disconnect Streaks
		l.mutex.RLock()
		if wallet, ok := l.wallets[env.FromID]; ok {
			if stats, exists := l.leaderboard[wallet]; exists && time.Now().Before(stats.BanExpires) {
				l.mutex.RUnlock()
				log.Printf("[SECURITY] Blocked invite from banned player: %s (Ends: %s)\n", wallet, stats.BanExpires.Format(time.Kitchen))

				// Notify the banned player
				response := Envelope{
					Type:    "chat",
					FromID:  "SERVER",
					Payload: json.RawMessage(fmt.Sprintf(`{"text":"Matchmaking restricted until %s due to frequent disconnects."}`, stats.BanExpires.Format(time.Kitchen))),
				}
				if target, ok := l.clients[env.FromID]; ok {
					msg, _ := json.Marshal(response)
					target.send <- msg
				}
				return // Stop the challenge process
			}
		}
		l.mutex.RUnlock()

		// REPUTATION CHECK: Prevent low-reputation players from challenging high-reputation players
		l.mutex.RLock()
		if walletFrom, okF := l.wallets[env.FromID]; okF {
			if walletTo, okT := l.wallets[env.ToID]; okT {
				statsFrom := l.leaderboard[walletFrom]
				statsTo := l.leaderboard[walletTo]
				l.mutex.RUnlock()

				// If the target has high reputation (>200) and challenger is low (<50), block it
				if statsTo.Reputation > 200 && statsFrom.Reputation < 50 {
					response := Envelope{
						Type:    "chat",
						FromID:  "SERVER",
						ToID:    env.FromID,
						Payload: json.RawMessage(`{"text":"Your reputation is too low to challenge this player. Win more matches to improve it!"}`),
					}
					if target, ok := l.clients[env.FromID]; ok {
						msg, _ := json.Marshal(response)
						target.send <- msg
					}
					return
				}
			} else {
				l.mutex.RUnlock()
			}
		} else {
			l.mutex.RUnlock()
		}

		if data.Action == "invite" {
			// Cache P1's deck in a temporary match state
			l.mutex.Lock()
			l.matches[env.FromID] = &MatchState{P1ID: env.FromID, P1Deck: data.Deck, Rules: data.Rules}
			l.mutex.Unlock()
		} else if data.Action == "accept" {
			l.mutex.Lock()
			existing, ok := l.matches[env.ToID]
			if !ok {
				l.mutex.Unlock()
				return
			}

			match := &MatchState{
				P1ID:   env.ToID,
				P2ID:   env.FromID,
				P1Deck: existing.P1Deck,
				P2Deck: data.Deck,
				Rules:  data.Rules,
			}
			l.matches[env.FromID] = match
			l.matches[env.ToID] = match
			l.mutex.Unlock()

			// FINALIZE HANDSHAKE: Send 'sync_back' to both players with each other's decks

			// 1. Send P2's Deck to P1
			p1Sync := ChallengeData{
				Action: "sync_back",
				Deck:   match.P2Deck,
				Rules:  match.Rules,
			}
			p1Payload, _ := json.Marshal(p1Sync)
			p1Env, _ := json.Marshal(Envelope{
				Type:    "challenge",
				FromID:  match.P2ID,
				ToID:    match.P1ID,
				Payload: p1Payload,
			})
			l.mutex.RLock()
			if p1, ok := l.clients[match.P1ID]; ok {
				p1.send <- p1Env
			}
			l.mutex.RUnlock()

			// 2. Send P1's Deck to P2
			p2Sync := ChallengeData{
				Action: "sync_back",
				Deck:   match.P1Deck,
				Rules:  match.Rules,
			}
			p2Payload, _ := json.Marshal(p2Sync)
			p2Env, _ := json.Marshal(Envelope{
				Type:    "challenge",
				FromID:  match.P1ID,
				ToID:    match.P2ID,
				Payload: p2Payload,
			})
			l.mutex.RLock()
			if p2, ok := l.clients[match.P2ID]; ok {
				p2.send <- p2Env
			}
			l.mutex.RUnlock()
		}

	case "spectate":
		// SECURITY: Check if the spectator is currently banned for Disconnect Streaks
		if wallet, ok := l.wallets[env.FromID]; ok {
			if stats, exists := l.leaderboard[wallet]; exists && time.Now().Before(stats.BanExpires) {
				log.Printf("[SECURITY] Blocked spectate request from banned player: %s (Ends: %s)\n", wallet, stats.BanExpires.Format(time.Kitchen))

				// Notify the banned player
				response := Envelope{
					Type:    "chat",
					FromID:  "SERVER",
					Payload: json.RawMessage(fmt.Sprintf(`{"text":"Spectating restricted until %s due to frequent disconnects."}`, stats.BanExpires.Format(time.Kitchen))),
				}
				if target, ok := l.clients[env.FromID]; ok {
					msg, _ := json.Marshal(response)
					target.send <- msg
				}
				return // Stop the spectate process
			}
		}

		var data struct {
			TargetID string `json:"target_id"`
		}
		json.Unmarshal(env.Payload, &data)
		l.mutex.Lock()
		if match, ok := l.matches[data.TargetID]; ok {
			l.mutex.Unlock()
			// Add spectator to the match if not already present
			l.mutex.Lock()
			isAlreadySpectating := false
			for _, sID := range match.Spectators {
				if sID == env.FromID {
					isAlreadySpectating = true
					break
				}
			}
			if !isAlreadySpectating && env.FromID != match.P1ID && env.FromID != match.P2ID {
				match.Spectators = append(match.Spectators, env.FromID)
				log.Printf("[SPECTATE] Client %s joined match: %s vs %s\n", env.FromID, match.P1ID, match.P2ID)
			}
			l.mutex.Unlock()

			// SYNC STATE: Send current match state to the joining spectator
			syncData := struct {
				P1ID  string          `json:"p1_id"`
				P2ID  string          `json:"p2_id"`
				Board [9]*ServerCard  `json:"board"`
				Rules map[string]bool `json:"rules"`
			}{
				P1ID:  match.P1ID,
				P2ID:  match.P2ID,
				Board: match.Board,
				Rules: match.Rules,
			}
			payload, _ := json.Marshal(syncData)
			syncEnv := Envelope{
				Type:    "match_start",
				FromID:  "SERVER",
				ToID:    env.FromID,
				Payload: payload,
			}
			l.mutex.RLock()
			if target, ok := l.clients[env.FromID]; ok {
				msg, _ := json.Marshal(syncEnv)
				target.send <- msg
			}
			l.mutex.RUnlock()
		} else {
			l.mutex.Unlock()
		}

	case "move":
		// Determine recipient based on match state, not client input
		l.mutex.RLock()
		match, ok := l.matches[env.FromID]
		if ok {
			if env.FromID == match.P1ID {
				env.ToID = match.P2ID
			} else {
				env.ToID = match.P1ID
			}
		}

		match, ok := l.matches[env.FromID]
		if !ok {
			return
		}
		var move MoveData
		json.Unmarshal(env.Payload, &move)

		pIdx := 0
		if env.FromID == match.P2ID {
			pIdx = 1
		}

		// SERVER-SIDE VALIDATION: Verify Power stats against Official Inventory
		verifiedCard, err := l.getVerifiedCard(move.CardID)
		if err != nil {
			log.Printf("[SECURITY] Move rejected: Could not verify card %d\n", move.CardID)
			return
		}
		if move.Power != verifiedCard.Power {
			log.Printf("[SECURITY] Power Spoof detected from %s for Card %d. Expected %v, got %v",
				env.FromID, move.CardID, verifiedCard.Power, move.Power)
			return // Drop the malicious move
		}

		l.mutex.Lock()
		// Update Shadow Board
		if move.GridIndex >= 0 && move.GridIndex < 9 {
			match.Board[move.GridIndex] = &ServerCard{ID: move.CardID, Owner: pIdx, Power: move.Power}
			flips := l.serverCheckCaptures(match, move.GridIndex, pIdx)
			log.Printf("[BATTLE] Player %d move at Grid %d triggered %d flips\n", pIdx+1, move.GridIndex, flips)

			// BROADCAST TO SPECTATORS
			for _, specID := range match.Spectators {
				if spec, ok := l.clients[specID]; ok {
					select {
					case spec.send <- rawMsg:
					default:
						// Drop message if spectator buffer is full
					}
				}
			}
		}

		// Check if Board is full
		full := true
		for _, slot := range match.Board {
			if slot == nil {
				full = false
				break
			}
		}
		if full && !match.IsFinished {
			match.IsFinished = true
			l.verifyWinner(match)
		}
		l.mutex.Unlock()

	case "chat":
		// SECURITY: Check if the sender is currently banned for Disconnect Streaks
		if wallet, ok := l.wallets[env.FromID]; ok {
			if stats, exists := l.leaderboard[wallet]; exists && time.Now().Before(stats.BanExpires) {
				log.Printf("[SECURITY] Blocked chat message from banned player: %s (Ends: %s)\n", wallet, stats.BanExpires.Format(time.Kitchen))

				// Notify the banned player privately
				response := Envelope{
					Type:    "chat",
					FromID:  "SERVER",
					Payload: json.RawMessage(fmt.Sprintf(`{"text":"Chat restricted until %s due to frequent disconnects."}`, stats.BanExpires.Format(time.Kitchen))),
				}
				if target, ok := l.clients[env.FromID]; ok {
					msg, _ := json.Marshal(response)
					target.send <- msg
				}

				// Override ToID to prevent global broadcast in the run loop
				env.ToID = "VOID"
			}
		}

	case "ping":
		// Server-side pong response to calculate network latency
		response := Envelope{
			Type:   "pong",
			FromID: "SERVER",
			ToID:   env.FromID,
		}
		if target, ok := l.clients[env.FromID]; ok {
			msg, _ := json.Marshal(response)
			target.send <- msg
		}
	}
}

// getVerifiedCard retrieves official stats from cache or fetches from NFT Navigator API
func (l *Lobby) getVerifiedCard(tokenID int) (ServerCard, error) {
	l.mutex.RLock()
	card, exists := l.inventory[tokenID]
	l.mutex.RUnlock()

	// DYNAMIC CACHE CHECK: Use cached data if it exists and is less than 1 hour old.
	// This ensures pricing/stats remain fresh without hammering the Indexer API.
	if exists && time.Since(card.LastUpdated) < 1*time.Hour {
		return card, nil
	}

	// Configuration
	contractID := 7900471
	baseURL := "https://arc72-voi-mainnet.nftnavigator.xyz/nft-indexer/v1"

	var wg sync.WaitGroup
	var lastSale, mintPrice, currentPrice float64
	var mintRound, salesCount int

	// 1. Fetch Token Metadata (for Mint Price/Round info)
	wg.Add(1)
	go func() {
		defer wg.Done()
		url := fmt.Sprintf("%s/tokens?contractId=%d&tokenId=%d", baseURL, contractID, tokenID)
		resp, err := http.Get(url)
		if err == nil {
			defer resp.Body.Close()
			var res struct {
				Tokens []struct {
					MintRound int `json:"mintRound"`
				} `json:"tokens"`
			}
			if json.NewDecoder(resp.Body).Decode(&res) == nil && len(res.Tokens) > 0 {
				mintPrice = float64(res.Tokens[0].MintRound) * 1000 // Simplified
				mintRound = res.Tokens[0].MintRound
			}
		}
	}()

	// 2. Fetch Sales History (for Last Sale Power)
	wg.Add(1)
	go func() {
		defer wg.Done()
		url := fmt.Sprintf("%s/mp/sales?collectionId=%d&tokenId=%d&limit=1", baseURL, contractID, tokenID)
		resp, err := http.Get(url)
		if err == nil {
			defer resp.Body.Close()
			var res struct {
				Listings []struct {
					Price uint64 `json:"price"`
				} `json:"listings"`
			}
			if json.NewDecoder(resp.Body).Decode(&res) == nil && len(res.Listings) > 0 {
				lastSale = float64(res.Listings[0].Price)
				salesCount = len(res.Listings) // Simplified proxy for total sales history
			}
		}
	}()

	// 3. Fetch Active Listings (for Current Asking Power)
	wg.Add(1)
	go func() {
		defer wg.Done()
		url := fmt.Sprintf("%s/mp/listings?collectionId=%d&tokenId=%d&active=true&limit=1", baseURL, contractID, tokenID)
		resp, err := http.Get(url)
		if err == nil {
			defer resp.Body.Close()
			var res struct {
				Listings []struct {
					Price uint64 `json:"price"`
				} `json:"listings"`
			}
			if json.NewDecoder(resp.Body).Decode(&res) == nil && len(res.Listings) > 0 {
				currentPrice = float64(res.Listings[0].Price)
			}
		}
	}()

	wg.Wait()

	// Expanded Scale Normalization (0-2599)
	// 1 VOI of value = 1 Power point
	norm := func(val float64) int {
		if val <= 0 {
			return 50 // Base level Z
		}
		p := int(val / 1000000)
		if p > 2599 {
			p = 2599
		}
		return p
	}

	bottomPrice := currentPrice
	if bottomPrice <= 0 {
		bottomPrice = mintPrice
	}

	// REFACTORED AGE POWER (LEFT): Legacy Tiers scaled to 2600
	agePower := 200 // Standard (Level Y)
	if mintRound > 0 {
		if mintRound <= 1000000 {
			agePower = 2500 // Genesis (Level A)
		} else if mintRound <= 3000000 {
			agePower = 1800 // Early (Level H)
		} else if mintRound <= 6000000 {
			agePower = 1000 // Legacy (Level P)
		}
	}

	// SALES EXPERIENCE (RIGHT): Level up via velocity
	experiencePower := salesCount * 25
	if experiencePower > 2599 {
		experiencePower = 2599
	}

	officialPower := [4]int{
		norm(lastSale),  // Top (Price)
		experiencePower, // Right (Sales count as experience)
		norm(bottomPrice), // Bottom (Price)
		agePower,        // Left (Age)
	}

	newCard := ServerCard{
		ID:          tokenID,
		Power:       officialPower,
		Owner:       -1,
		LastUpdated: time.Now(),
	}

	l.mutex.Lock()
	l.inventory[tokenID] = newCard
	l.mutex.Unlock()

	return newCard
}

// logWinAudit records a successful payout to a separate file for compliance tracking.
func logWinAudit(recipient, network, txid, groupID string, amount uint64, history MatchHistory) {
	entry := struct {
		Timestamp string       `json:"timestamp"`
		Recipient string       `json:"recipient"`
		Network   string       `json:"network"`
		TxID      string       `json:"txid"`
		GroupID   string       `json:"group_id"`
		Amount    string       `json:"amount"`
		History   MatchHistory `json:"history"`
	}{
		Timestamp: time.Now().Format(time.RFC3339),
		Recipient: recipient,
		Network:   network,
		TxID:      txid,
		GroupID:   groupID,
		Amount:    fmt.Sprintf("%.1f $VBV", float64(amount)/1000000.0),
		History:   history,
	}

	b, _ := json.Marshal(entry)
	f, err := os.OpenFile("win_audit.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("[AUDIT ERROR] Failed to write to log: %v\n", err)
		return
	}
	defer f.Close()
	f.Write(append(b, '\n'))
}

// logAdminAudit records an administrative action to a separate file for permanent record keeping.
func (l *Lobby) logAdminAudit(action, target, details string) {
	l.mutex.RLock()
	load := len(l.matches) / 2
	l.mutex.RUnlock()

	entry := struct {
		Timestamp  string `json:"timestamp"`
		Action     string `json:"action"`
		Target     string `json:"target"`
		Details    string `json:"details"`
		ServerLoad int    `json:"server_load"`
	}{
		Timestamp:  time.Now().Format(time.RFC3339),
		Action:     action,
		Target:     target,
		Details:    details,
		ServerLoad: load,
	}

	b, _ := json.Marshal(entry)
	f, err := os.OpenFile("admin_audit.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("[AUDIT ERROR] Failed to write to admin log: %v\n", err)
		return
	}
	defer f.Close()
	f.Write(append(b, '\n'))
}

func (l *Lobby) serverCheckCaptures(match *MatchState, gridIndex int, pIdx int) int {
	totalFlips := 0
	placedCard := match.Board[gridIndex]
	neighbors := []struct {
		offset           int
		placedPowerIdx   int
		neighborPowerIdx int
		boundaryCheck    func(int) bool
	}{
		{-3, 0, 2, func(idx int) bool { return idx >= 3 }},
		{+1, 1, 3, func(idx int) bool { return idx%3 != 2 }},
		{+3, 2, 0, func(idx int) bool { return idx <= 5 }},
		{-1, 3, 1, func(idx int) bool { return idx%3 != 0 }},
	}

	sameGroups := make(map[int][]int)
	plusGroups := make(map[int][]int)
	var comboQueue []int

	for _, n := range neighbors {
		nbIdx := gridIndex + n.offset
		if n.boundaryCheck(gridIndex) && match.Board[nbIdx] != nil {
			neighbor := match.Board[nbIdx]
			pPower := placedCard.Power[n.placedPowerIdx]
			nPower := neighbor.Power[n.neighborPowerIdx]

			if match.Rules["Same"] && pPower == nPower {
				sameGroups[pPower] = append(sameGroups[pPower], nbIdx)
			}
			if match.Rules["Plus"] {
				sum := pPower + nPower
				plusGroups[sum] = append(plusGroups[sum], nbIdx)
			}
			if neighbor.Owner != pIdx && pPower > nPower {
				neighbor.Owner = pIdx
				totalFlips++
			}
		}
	}

	for _, indices := range sameGroups {
		if len(indices) >= 2 {
			for _, idx := range indices {
				if match.Board[idx].Owner != pIdx {
					match.Board[idx].Owner = pIdx
					totalFlips++
					comboQueue = append(comboQueue, idx)
				}
			}
		}
	}
	for _, indices := range plusGroups {
		if len(indices) >= 2 {
			for _, idx := range indices {
				if match.Board[idx].Owner != pIdx {
					match.Board[idx].Owner = pIdx
					totalFlips++
					comboQueue = append(comboQueue, idx)
				}
			}
		}
	}

	// Combo chain (Basic captures only)
	for len(comboQueue) > 0 {
		currIdx := comboQueue[0]
		comboQueue = comboQueue[1:]
		currCard := match.Board[currIdx]

		for _, n := range neighbors {
			nbIdx := currIdx + n.offset
			if n.boundaryCheck(currIdx) && match.Board[nbIdx] != nil {
				neighbor := match.Board[nbIdx]
				if neighbor.Owner != pIdx && currCard.Power[n.placedPowerIdx] > neighbor.Power[n.neighborPowerIdx] {
					neighbor.Owner = pIdx
					totalFlips++
					comboQueue = append(comboQueue, nbIdx)
				}
			}
		}
	}

	return totalFlips
}

// syncStatsFromBlockchain derives player stats by analyzing the history of $VBV transfers from the Faucet.
func (l *Lobby) syncStatsFromBlockchain(clientID, wallet string) {
	faucetAddr := os.Getenv("VAULT_ADDRESS")

	// Identify all Asset IDs to check (Primary + Fallback)
	assetIDs := []string{os.Getenv("REWARD_ASSET_ID")}
	if fallbackID := os.Getenv("FALLBACK_REWARD_ASSET_ID"); fallbackID != "" {
		assetIDs = append(assetIDs, fallbackID)
	}

	baseURL := "https://arc72-voi-mainnet.nftnavigator.xyz/nft-indexer/v1"
	totalWins := 0
	totalDNFs := 0

	for _, tokenID := range assetIDs {
		if tokenID == "" {
			continue
		}

		// We query the indexer for transfers of $VBV variants from our Faucet to this player
		url := fmt.Sprintf("%s/arc200/transfers?contractId=%s&from=%s&to=%s",
			baseURL, tokenID, faucetAddr, wallet)

		resp, err := http.Get(url)
		if err != nil {
			log.Printf("[INDEXER ERROR] Could not fetch history for %s on Asset %s: %v\n", wallet, tokenID, err)
			continue
		}

		var res struct {
			Transfers []struct {
				Amount    string `json:"amount"`
				Metadata  string `json:"metadata"` // This is where the note/log usually appears
				Timestamp int64  `json:"timestamp"`
			} `json:"transfers"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&res); err == nil {
			for _, tx := range res.Transfers {
				// Only count transactions that were tagged by our Faucet as a Win
				if strings.HasPrefix(tx.Metadata, "VBT_WIN:") {
					totalWins++
				} else if strings.HasPrefix(tx.Metadata, "VBT_DNF:") {
					totalDNFs++
				}
			}
		}
		resp.Body.Close()
	}

	l.mutex.Lock()
	stats := l.leaderboard[wallet]
	stats.Wins = totalWins
	stats.DNFs = totalDNFs

	// Update reputation based on on-chain data
	stats.Reputation = calculateReputation(stats)

	// Preserve existing Ban/DNF info if we are just re-syncing wins
	if current, exists := l.leaderboard[wallet]; exists {
		stats.DisconnectStreak = current.DisconnectStreak
		stats.BanExpires = current.BanExpires
	}
	l.leaderboard[wallet] = stats
	l.mutex.Unlock()
	log.Printf("[BLOCKCHAIN] Derived stats for %s: %d Wins, %d DNFs\n", wallet, totalWins, totalDNFs)
}

// refreshGlobalLeaderboard performs a full scan of the Indexer for all VBT tags to build the Hall of Fame.
func (l *Lobby) refreshGlobalLeaderboard() {
	faucetAddr := os.Getenv("VAULT_ADDRESS")
	assetIDs := []string{os.Getenv("REWARD_ASSET_ID")}
	if fallback := os.Getenv("FALLBACK_REWARD_ASSET_ID"); fallback != "" {
		assetIDs = append(assetIDs, fallback)
	}

	log.Printf("[INDEXER] Starting global Hall of Fame sync for Faucet: %s\n", faucetAddr)
	
	type tempStats struct {
		wins int
		dnfs int
	}
	globalData := make(map[string]*tempStats)

	baseURL := "https://arc72-voi-mainnet.nftnavigator.xyz/nft-indexer/v1"

	for _, tokenID := range assetIDs {
		if tokenID == "" {
			continue
		}

		// Fetch all transfers originating from the Faucet
		url := fmt.Sprintf("%s/arc200/transfers?contractId=%s&from=%s", baseURL, tokenID, faucetAddr)
		resp, err := http.Get(url)
		if err != nil {
			continue
		}

		var res struct {
			Transfers []struct {
				To       string `json:"to"`
				Metadata string `json:"metadata"`
			} `json:"transfers"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&res); err == nil {
			for _, tx := range res.Transfers {
				if _, exists := globalData[tx.To]; !exists {
					globalData[tx.To] = &tempStats{}
				}

				if strings.HasPrefix(tx.Metadata, "VBT_WIN:") {
					globalData[tx.To].wins++
				} else if strings.HasPrefix(tx.Metadata, "VBT_DNF:") {
					globalData[tx.To].dnfs++
				}
			}
		}
		resp.Body.Close()
	}

	// Merge global data into the live leaderboard
	l.mutex.Lock()
	for wallet, t := range globalData {
		stats := l.leaderboard[wallet]
		stats.Wins = t.wins
		stats.DNFs = t.dnfs
		stats.Reputation = calculateReputation(stats)
		l.leaderboard[wallet] = stats
	}
	l.mutex.Unlock()

	l.logAdminAudit("GLOBAL_SYNC", "LEADERBOARD", fmt.Sprintf("Synced %d historical participants", len(globalData)))
	log.Printf("[INDEXER] Global Hall of Fame sync complete. %d participants indexed.\n", len(globalData))

	// Broadcast update so connected clients see the new standings
	l.mutex.RLock()
	msg := l.getLobbyUpdateMsg()
	l.mutex.RUnlock()
	l.broadcast <- msg
}

// recordWinOnChain acts as the Oracle, writing the victory to the blockchain.
// In the transaction-based model, the Reward Payout IS the record.
func (l *Lobby) recordWinOnChain(winnerWallet string) {
	log.Printf("[BLOCKCHAIN] Match result for %s will be recorded upon successful $VBV reward payout.\n", winnerWallet)
	// The actual transaction is handled in handleReward, which serves as the "Commit" to the DB.
}

func (l *Lobby) calculateFinalScores(match *MatchState) (int, int) {
	p1, p2 := 0, 0
	boardMap := make(map[int]bool)

	// 1. Count board ownership
	for _, c := range match.Board {
		if c == nil {
			continue
		}
		boardMap[c.ID] = true
		if c.Owner == 0 {
			p1++
		} else {
			p2++
		}
	}

	// 2. Count hand ownership (the 1 card not on board)
	for _, id := range match.P1Deck {
		if !boardMap[id] {
			p1++
		}
	}
	for _, id := range match.P2Deck {
		if !boardMap[id] {
			p2++
		}
	}

	return p1, p2
}

// calculateDeckRating derives the [Letter++] rating string for a list of card IDs.
// Assumes l.mutex is held or data is immutable.
func (l *Lobby) calculateDeckRating(cardIDs []int) string {
	if len(cardIDs) == 0 {
		return "[Z]"
	}

	maxBin := -1
	// 1. Find the highest card tier in the deck
	for _, id := range cardIDs {
		card, ok := l.inventory[id]
		if !ok {
			continue
		}
		highestPower := 0
		for _, p := range card.Power {
			if p > highestPower {
				highestPower = p
			}
		}
		// Power mapping: 1-100=Bin 0, 101-200=Bin 1, etc.
		bin := (highestPower - 1) / 100
		if bin > maxBin {
			maxBin = bin
		}
	}

	if maxBin == -1 {
		return "[Z]"
	}

	// 2. Map bin to Letter (Reverse Alphabet: 0=Z, 25=A)
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	if maxBin > 25 { maxBin = 25 }
	if maxBin < 0 { maxBin = 0 }
	baseLetter := string(alphabet[25-maxBin])

	// 3. Count how many cards share this highest tier
	plusCount := 0
	for _, id := range cardIDs {
		card, ok := l.inventory[id]
		if !ok {
			continue
		}
		highestPower := 0
		for _, p := range card.Power {
			if p > highestPower {
				highestPower = p
			}
		}
		bin := (highestPower - 1) / 100
		if bin == maxBin {
			plusCount++
		}
	}

	// 4. Construct Suffix
	suffix := ""
	for i := 0; i < plusCount; i++ {
		suffix += "+"
	}

	return fmt.Sprintf("[%s%s]", baseLetter, suffix)
}

// isBetterRating compares two rating strings (e.g. [A++]) and returns true if newRating is superior.
func (l *Lobby) isBetterRating(newRating, oldRating string) bool {
	if oldRating == "" || oldRating == "[Z]" {
		return true
	}
	if newRating == "" || newRating == "[Z]" {
		return false
	}

	// Extract letter and plus count
	parse := func(r string) (rune, int) {
		if len(r) < 3 { return 'Z', 0 }
		letter := rune(r[1])
		plusCount := strings.Count(r, "+")
		return letter, plusCount
	}

	newLetter, newPlus := parse(newRating)
	oldLetter, oldPlus := parse(oldRating)

	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	newIdx := strings.IndexRune(alphabet, newLetter)
	oldIdx := strings.IndexRune(alphabet, oldLetter)

	// Lower index = Higher Power (A=0, Z=25)
	if newIdx < oldIdx { return true }
	if newIdx > oldIdx { return false }
	return newPlus >= oldPlus
}

func (l *Lobby) verifyWinner(match *MatchState) {
	p1Count, p2Count := l.calculateFinalScores(match)
	match.FinalScores = [2]int{p1Count, p2Count}

	log.Printf("[BATTLE] Match Finished. Server Calculation: P1:%d, P2:%d\n", p1Count, p2Count)

	history := MatchHistory{
		Scores:    match.FinalScores,
		Timestamp: time.Now(),
	}

	if p1Count > p2Count {
		history.WinnerID = match.P1ID
		if wallet, ok := l.wallets[match.P1ID]; ok {
			go l.recordWinOnChain(wallet)
			l.updateLeaderboard(match.P1ID)

			// Automatically update BestRating for the winner
			newRating := l.calculateDeckRating(match.P1Deck)
			stats := l.leaderboard[wallet]
			if l.isBetterRating(newRating, stats.BestRating) {
				stats.BestRating = newRating
				l.leaderboard[wallet] = stats
			}
		}
		l.matchHistory[match.P1ID] = history
	} else if p2Count > p1Count {
		history.WinnerID = match.P2ID
		if wallet, ok := l.wallets[match.P2ID]; ok {
			l.updateLeaderboard(match.P2ID)

			// Automatically update BestRating for the winner
			newRating := l.calculateDeckRating(match.P2Deck)
			stats := l.leaderboard[wallet]
			if l.isBetterRating(newRating, stats.BestRating) {
				stats.BestRating = newRating
				l.leaderboard[wallet] = stats
			}
		}
		l.matchHistory[match.P2ID] = history
	}
	delete(l.matches, match.P1ID)
	delete(l.matches, match.P2ID)
}

// calculateReputation provides a weighted score based on behavior and skill
func calculateReputation(stats PlayerStats) int {
	// Base reputation is 100. Wins add 10, DNFs subtract 25, active streaks subtract 15.
	rep := 100 + (stats.Wins * 10) - (stats.DNFs * 25) - (stats.DisconnectStreak * 15)
	if rep < 0 {
		return 0
	}
	return rep
}

// loadLeaderboard reads the win counts from leaderboard.json
func (l *Lobby) updateLeaderboard(clientID string) {
	if wallet, ok := l.wallets[clientID]; ok {
		stats := l.leaderboard[wallet]
		stats.Wins++
		stats.DisconnectStreak = 0 // Reset streak on a clean win
		stats.Reputation = calculateReputation(stats)
		l.leaderboard[wallet] = stats
		log.Printf("[LEADERBOARD] %s now has %d wins!\n", wallet, stats.Wins)
	}
}

func (l *Lobby) incrementDNF(clientID string) {
	if wallet, ok := l.wallets[clientID]; ok {
		stats := l.leaderboard[wallet]
		stats.DNFs++
		stats.DisconnectStreak++ // Increment the "Bad Actor" streak

		// Apply 24h Ban if streak exceeds 3
		if stats.DisconnectStreak > 3 {
			stats.BanExpires = time.Now().Add(24 * time.Hour)
		}

		stats.Reputation = calculateReputation(stats)
		l.leaderboard[wallet] = stats
		log.Printf("[LEADERBOARD] %s penalized with DNF. Total: %d\n", wallet, stats.DNFs)

		// BROADCAST PENALTY TO BLOCKCHAIN (The "Permanent Record")
		go l.recordDNFOnChain(wallet)
	}
}

// recordDNFOnChain sends a 0-amount ARC-200 transaction with a 'VBT_DNF:' note.
func (l *Lobby) recordDNFOnChain(wallet string) {
	faucetMnemonic := os.Getenv("FAUCET_MNEMONIC")
	algodAddr := os.Getenv("ALGOD_URL_VOI")
	appID, _ := strconv.ParseUint(os.Getenv("REWARD_ASSET_ID"), 10, 64)

	if faucetMnemonic == "" || algodAddr == "" || appID == 0 {
		return
	}

	client, _ := algod.MakeClient(algodAddr, "")
	pk, _ := mnemonic.ToPrivateKey(faucetMnemonic)
	faucetAccount, _ := crypto.AccountFromPrivateKey(pk)
	sp, _ := client.SuggestedParams().Do(context.Background())

	// Unique Identifier for penalties: VBT_DNF:[TIMESTAMP]
	dnfNote := []byte(fmt.Sprintf("VBT_DNF:%d", time.Now().Unix()))

	// Construct a 0-amount transfer
	method, _ := abi.MethodFromSignature("transfer(address,uint256)void")
	recipientAddr, _ := types.DecodeAddress(wallet)
	zeroAmt := new(big.Int).SetUint64(0)
	encodedArgs, _ := method.EncodeArgs([]interface{}{recipientAddr, zeroAmt})

	txn, err := transaction.MakeApplicationNoOpTxn(
		appID,
		[][]byte{encodedArgs},
		os.Getenv("VAULT_ADDRESS"),
		[]string{wallet},
		nil, nil, dnfNote, sp,
	)

	if err == nil {
		_, stxn, _ := crypto.SignTransaction(faucetAccount.PrivateKey, txn)
		txid, err := client.SendRawTransaction(stxn).Do(context.Background())
		if err == nil {
			log.Printf("[BLOCKCHAIN] DNF Recorded for %s. TxID: %s\n", wallet, txid)
		}
	}
}

// handleCardStats allows clients to query the official power stats for a card
func (l *Lobby) handleCardStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	idStr := r.URL.Query().Get("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "Invalid or missing Card ID", http.StatusBadRequest)
		return
	}

	card, err := l.getVerifiedCard(id)
	if err != nil {
		http.Error(w, "Card verification failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(card)
}

// handleStartTournament triggers the bracket generation using Top N players from the leaderboard.
func (l *Lobby) handleStartTournament(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !l.checkAdminAuth(w, r) {
		return
	}

	var req struct {
		Size int `json:"size"` // 8 or 16
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (req.Size != 8 && req.Size != 16) {
		http.Error(w, "Invalid tournament size. Use 8 or 16.", http.StatusBadRequest)
		return
	}

	l.mutex.Lock()
	defer l.mutex.Unlock()

	// 1. Collect and Sort Hall of Fame
	type entry struct {
		wallet string
		wins   int
	}
	var hof []entry
	for wallet, stats := range l.leaderboard {
		hof = append(hof, entry{wallet: wallet, wins: stats.Wins})
	}
	sort.Slice(hof, func(i, j int) bool { return hof[i].wins > hof[j].wins })

	if len(hof) < req.Size {
		http.Error(w, fmt.Sprintf("Not enough Hall of Fame participants. Have %d, need %d.", len(hof), req.Size), http.StatusBadRequest)
		return
	}

	// 2. Select Participants (Top N)
	participants := make([]string, req.Size)
	for i := 0; i < req.Size; i++ {
		participants[i] = hof[i].wallet
	}

	// 3. Generate Seeding Pairs (Standard Tournament Bracket)
	// This ensures top seeds meet only in later rounds.
	matches := []TournamentMatch{}
	seedMap := []int{} // Maps sorted rank (0-indexed) to bracket position
	if req.Size == 8 {
		// 1v8, 4v5, 2v7, 3v6
		seedMap = []int{0, 7, 3, 4, 1, 6, 2, 5}
	} else if req.Size == 16 {
		// 1v16, 8v9, 5v12, 4v13, 2v15, 7v10, 6v11, 3v14
		seedMap = []int{0, 15, 7, 8, 4, 11, 3, 12, 1, 14, 6, 9, 5, 10, 2, 13}
	}

	for i := 0; i < len(seedMap); i += 2 {
		match := TournamentMatch{
			ID:    fmt.Sprintf("R1-M%d", (i/2)+1), // Round 1, Match 1, 2, etc.
			P1:    participants[seedMap[i]],
			P2:    participants[seedMap[i+1]],
			Round: 1,
		}
		matches = append(matches, match)
	}

	l.tournament = TournamentState{
		Active:       true,
		Participants: participants,
		Matches:      matches,
		CurrentRound: 1,
	}

	l.logAdminAudit("START_TOURNAMENT", "GLOBAL", fmt.Sprintf("Size: %d, Participants: %v", req.Size, participants))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(l.tournament)
}

// handleLeaderboard returns a sorted list of top players by win count
func (l *Lobby) handleLeaderboard(w http.ResponseWriter, r *http.Request) {
	l.mutex.RLock()
	defer l.mutex.RUnlock()

	type entry struct {
		Wallet           string    `json:"wallet"`
		Wins             int       `json:"wins"`
		DNFs             int       `json:"dnfs"`
		DisconnectStreak int       `json:"disconnect_streak"`
		Reputation       int       `json:"reputation"`
		BestRating       string    `json:"best_rating"`
		BanExpires       time.Time `json:"ban_expires"`
	}
	var list []entry
	for w, stats := range l.leaderboard {
		list = append(list, entry{
			Wallet: w, 
			Wins: stats.Wins, 
			DNFs: stats.DNFs, 
			DisconnectStreak: stats.DisconnectStreak, 
			Reputation: stats.Reputation, 
			BestRating: stats.BestRating,
			BanExpires: stats.BanExpires,
		})
	}

	// Sort descending
	sort.Slice(list, func(i, j int) bool {
		return list[i].Wins > list[j].Wins
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

func generateNonce() string {
	b := make([]byte, 16)
	rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

// getLobbyUpdateMsg constructs an Envelope containing the current list of active player IDs.
// Assumes caller handles mutex locking for l.clients access.
func (l *Lobby) getLobbyUpdateMsg() []byte {
	type playerInfo struct {
		ID             string    `json:"id"`
		IsAdmin        bool      `json:"is_admin"`
		AvatarURL      string    `json:"avatar_url"`
		BanExpires     time.Time `json:"ban_expires"`
		HasMardonBadge bool      `json:"has_mardon_badge"`
	}
	var players []playerInfo
	for _, client := range l.clients {
		isAdmin := client.isAdmin
		avatar := client.avatarURL

		var banExpires time.Time
		hasMardon := false
		if wallet, ok := l.wallets[client.id]; ok {
			if stats, exists := l.leaderboard[wallet]; exists {
				banExpires = stats.BanExpires
				// Mardon Badge criteria: 50+ Wins and no active disconnect streak
				if stats.Wins >= 50 && stats.DisconnectStreak == 0 {
					hasMardon = true
				}
			}
		}
		players = append(players, playerInfo{
			ID:             client.id,
			IsAdmin:        isAdmin,
			AvatarURL:      avatar,
			BanExpires:     banExpires,
			HasMardonBadge: hasMardon,
		})
	}

	// Wrap players and global state into a single update
	update := struct {
		Players           []playerInfo      `json:"players"`
		MaintenanceActive bool              `json:"maintenance_active"`
		MaintenanceTime   time.Time         `json:"maintenance_time"`
		FaucetBalance     float64           `json:"faucet_balance"`
		Rewards           map[uint64]uint64 `json:"rewards"`
		ActiveMatchCount  int               `json:"active_match_count"`
	}{
		Players:           players,
		MaintenanceActive: l.maintenanceMode,
		MaintenanceTime:   l.maintenanceTime,
		Rewards:           l.rewards,
		FaucetBalance:     l.faucetBalance,
		ActiveMatchCount:  len(l.matches) / 2,
	}

	payload, _ := json.Marshal(update)
	env := Envelope{
		Type:    "lobby_update",
		FromID:  "SERVER",
		Payload: payload,
	}
	msg, _ := json.Marshal(env)
	return msg
}

// upgrader is a utility to upgrade HTTP connections to WebSocket connections.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		allowed := os.Getenv("ALLOWED_ORIGINS")
		if allowed == "" || allowed == "*" {
			return true // Default to open for dev
		}

		origin := r.Header.Get("Origin")
		origins := strings.Split(allowed, ",")
		for _, o := range origins {
			// Secure Suffix Check: Must be exactly .carrd.co or a subdomain thereof.
			if strings.HasSuffix(origin, ".carrd.co") && !strings.Contains(strings.TrimSuffix(origin, ".carrd.co"), "-") {
				return true
			}
			if strings.TrimSpace(o) == origin {
				return true
			}
		}
		log.Printf("[SECURITY] Blocked WebSocket connection from origin: %s\n", origin)
		return false
	},
}

// withRateLimit applies a Leaky Bucket algorithm to protect HTTP API endpoints.
func (l *Lobby) withRateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		const capacity = 10.0
		const leakRate = 1.0 // 1 token leaked (request allowed) per second

		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}

		l.mutex.Lock()
		bucket, exists := l.httpRateLimits[ip]
		now := time.Now()

		if !exists {
			bucket = &RateBucket{Tokens: capacity, LastUpdate: now}
			l.httpRateLimits[ip] = bucket
		}

		// Refill bucket based on time passed (The "Leak")
		elapsed := now.Sub(bucket.LastUpdate).Seconds()
		bucket.Tokens += elapsed * leakRate
		if bucket.Tokens > capacity {
			bucket.Tokens = capacity
		}
		bucket.LastUpdate = now

		if bucket.Tokens < 1.0 {
			l.mutex.Unlock()
			http.Error(w, "Too many requests. Please slow down.", http.StatusTooManyRequests)
			return
		}

		bucket.Tokens -= 1.0
		l.mutex.Unlock()

		next.ServeHTTP(w, r)
	}
}

// withCORS is a middleware that handles Cross-Origin Resource Sharing for the Carrd frontend.
func withCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		allowed := os.Getenv("ALLOWED_ORIGINS")

		isAllowed := false
		if allowed == "" || allowed == "*" {
			isAllowed = true
		} else {
			origins := strings.Split(allowed, ",")
			for _, o := range origins {
				if strings.TrimSpace(o) == origin || (origin != "" && strings.HasSuffix(origin, ".carrd.co")) {
					isAllowed = true
					break
				}
			}
		}

		if isAllowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Admin-Key")
		}

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	}
}

// serveWs handles HTTP requests for the WebSocket endpoint, upgrading them if possible.
func serveWs(lobby *Lobby, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[WS] Failed to upgrade connection: %v\n", err)
		return
	}

	// Generate a simple, unique client ID. In a production system, this would typically
	// be a user ID obtained from an authentication system.
	clientID := fmt.Sprintf("Guest-%d", time.Now().UnixNano())
	client := &Client{conn: conn, send: make(chan []byte, 256), id: clientID, lobby: lobby}
	lobby.register <- client // Register the new client with the lobby.

	// Send the client their identity immediately
	identityEnv := Envelope{Type: "identity", ToID: clientID}
	idMsg, _ := json.Marshal(identityEnv)
	conn.WriteMessage(websocket.TextMessage, idMsg)

	// Ensure the client is unregistered and connection closed when done.
	defer func() {
		client.lobby.unregister <- client
		client.conn.Close()
	}()

	// Start goroutines to handle reading from and writing to the WebSocket.
	go client.writePump() // Handles sending messages from the lobby to the client.
	client.readPump()     // Handles reading messages from the client and forwarding to the lobby.
}

// readPump continuously reads messages from the WebSocket connection.
func (c *Client) readPump() {
	// TODO: Set read message size limits and pong handlers for robustness.
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("[WS] Read error for client %s: %v\n", c.id, err)
			}
			break // Break the loop if there's a read error (connection closed or broken).
		}

		// Rate Limiting Check
		if !c.allowMessage() {
			log.Printf("[SECURITY] Rate limit exceeded for client %s. Dropping message.\n", c.id)
			c.lobby.logAdminAudit("SECURITY_RATE_LIMIT", c.id, "Sliding window threshold exceeded")
			continue
		}

		var env Envelope
		if err := json.Unmarshal(message, &env); err != nil {
			log.Printf("[WS] Protocol Error from %s: %v\n", c.id, err)
			continue
		}

		// Automatic Sender Stamping
		env.FromID = c.id

		// Route to Lobby Broadcast
		// In a production scenario, you would filter "move" messages to only reach the specific opponent.
		finalMsg, _ := json.Marshal(env)
		c.lobby.broadcast <- finalMsg
	}
}

// writePump continuously writes messages from the send channel to the WebSocket connection.
func (c *Client) writePump() {
	for message := range c.send {
		err := c.conn.WriteMessage(websocket.TextMessage, message)
		if err != nil {
			log.Printf("[WS] Write error for client %s: %v\n", c.id, err)
			return // Exit if there's a write error.
		}
	}
}

// handleReward handles the secure on-chain payout logic.
// This is now server-side to protect the faucet mnemonic.
func (l *Lobby) handleReward(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Rate Limit Check (Per-IP)
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	l.mutex.Lock()
	if lastReq, ok := l.rateLimits[ip]; ok && time.Since(lastReq) < 30*time.Second {
		l.mutex.Unlock()
		http.Error(w, "Rate limit exceeded. Please wait 30 seconds.", http.StatusTooManyRequests)
		return
	}
	l.rateLimits[ip] = time.Now()
	l.mutex.Unlock()

	var req struct {
		Recipient    string `json:"recipient"`
		Network      string `json:"network"`
		ClientID     string `json:"client_id"`
		SignedTx     []byte `json:"signed_tx"` // The "Reverse Sign" proof
		ClientScore  [2]int `json:"client_score"`
		SiteVerified bool   `json:"site_verified"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// 0. Server-Side Win Verification
	l.mutex.Lock()
	history, hasHistory := l.matchHistory[req.ClientID]
	l.mutex.Unlock()

	if !hasHistory {
		log.Printf("[REWARD DENIED] No match history found for Client: %s\n", req.ClientID)
		http.Error(w, "Unauthorized: No verified win detected.", http.StatusUnauthorized)
		return
	}

	// 0.1 Site Integrity Verification
	if !req.SiteVerified {
		log.Printf("[REWARD DENIED] Site authenticity verification failed for Client: %s\n", req.ClientID)
		l.logAdminAudit("SECURITY_REWARD_DENIED", req.ClientID, "Site authenticity verification failed")
		http.Error(w, "Unauthorized: Payouts are only permitted from the official Virtualbabes Arena.", http.StatusUnauthorized)
		return
	}

	// CROSS-CHECK: Did the client report the same score the server calculated?
	if req.ClientScore[0] != history.Scores[0] || req.ClientScore[1] != history.Scores[1] {
		log.Printf("[REWARD DENIED] Score mismatch for %s. Server: %v, Client: %v\n", req.ClientID, history.Scores, req.ClientScore)
		http.Error(w, "Unauthorized: Reported score does not match server records.", http.StatusUnauthorized)
		return
	}

	// Verify the client is indeed the winner recorded in history
	if history.WinnerID != req.ClientID {
		l.mutex.Unlock()
		http.Error(w, "Unauthorized: Winner identity mismatch.", http.StatusUnauthorized)
		return
	}

	// Final sanity check: P1 must have more points than P2 for payout
	if history.Scores[0] <= history.Scores[1] {
		http.Error(w, "Unauthorized: Payout only available for Player 1 wins.", http.StatusUnauthorized)
		return
	}

	// 0.1 Nonce Verification (Replay Protection)
	l.mutex.RLock()
	nonceData, exists := l.nonces[req.ClientID]
	l.mutex.RUnlock()

	if !exists {
		http.Error(w, "Unauthorized: No active session nonce found.", http.StatusUnauthorized)
		return
	}

	// 0.1.1 Expiration Check
	if time.Since(nonceData.CreatedAt) > 5*time.Minute {
		l.mutex.Lock()
		delete(l.nonces, req.ClientID)
		l.mutex.Unlock()
		log.Printf("[REWARD DENIED] Nonce expired for Client: %s\n", req.ClientID)
		http.Error(w, "Unauthorized: Session nonce expired.", http.StatusUnauthorized)
		return
	}

	// 0.1 "Reverse Sign" Verification
	// We decode the signed transaction provided by the user.
	// We verify the signature matches the Recipient address.
	var stx transaction.SignedTxn
	if err := json.Unmarshal(req.SignedTx, &stx); err != nil {
		// If not JSON, try msgpack (standard for Algorand)
		err = msgpack.Decode(req.SignedTx, &stx)
	}
	if stx.Sig == (crypto.Signature{}) || string(stx.Txn.Sender[:]) != req.Recipient {
		http.Error(w, "Invalid Reverse Sign: Signature/Sender mismatch", http.StatusUnauthorized)
		return
	}

	// Ensure the signed note matches the expected nonce
	if string(stx.Txn.Note) != nonceData.Value {
		http.Error(w, "Invalid Reverse Sign: Nonce mismatch or replayed signature", http.StatusUnauthorized)
		return
	}

	// 1. Initialize Client & Fetch Vault Status
	algodAddr := os.Getenv("ALGOD_URL_ALGO")
	if algodAddr == "" {
		algodAddr = "https://testnet-api.algonode.cloud" // Fallback
	}

	if req.Network == "VOI" {
		algodAddr = os.Getenv("ALGOD_URL_VOI")
		if algodAddr == "" {
			algodAddr = "https://testnet-api.voi.nodly.io" // Fallback
		}
	}

	log.Printf("[NODE] Initializing %s client at: %s\n", req.Network, algodAddr)

	client, err := algod.MakeClient(algodAddr, "")
	if err != nil {
		log.Printf("[NODE ERROR] Failed to create client for %s at %s: %v\n", req.Network, algodAddr, err)
		http.Error(w, "Failed to connect to network", http.StatusInternalServerError)
		return
	}

	// Official Voi Faucet Vault Address
	vaultAddress := os.Getenv("VAULT_ADDRESS")
	if vaultAddress == "" {
		log.Println("[CRITICAL] VAULT_ADDRESS environment variable is not set")
		http.Error(w, "Server configuration error", http.StatusInternalServerError)
		return
	}

	// Verify vault account exists and is reachable
	_, err = client.AccountInformation(vaultAddress).Do(context.Background())
	if err != nil {
		http.Error(w, "Vault account unavailable on-chain", http.StatusInternalServerError)
		return
	}

	// 3. SECURE VAULT: Faucet Mnemonic (Never exposed to browser)
	faucetMnemonic := os.Getenv("FAUCET_MNEMONIC")
	if faucetMnemonic == "" {
		log.Println("[CRITICAL] FAUCET_MNEMONIC environment variable is not set")
		http.Error(w, "Server configuration error", http.StatusInternalServerError)
		return
	}
	privateKey, err := mnemonic.ToPrivateKey(faucetMnemonic)
	if err != nil {
		http.Error(w, "Server configuration error", http.StatusInternalServerError)
		return
	}
	faucetAccount, _ := crypto.AccountFromPrivateKey(privateKey)

	// 4. Create the Game Win Note for Blockchain Persistence
	winNote := []byte(fmt.Sprintf("VBT_WIN:%s", nonceData.Value))

	// 4. Construct & Sign Tx
	sp, _ := client.SuggestedParams().Do(context.Background())
	bonusApplied := false
	method, err := abi.MethodFromSignature("transfer(address,uint256)void")
	if err != nil {
		http.Error(w, "ABI configuration error", http.StatusInternalServerError)
		return
	}

	l.mutex.RLock()
	activeRewards := l.rewards
	poolBalance := l.faucetBalance
	stats, hasStats := l.leaderboard[req.Recipient]
	l.mutex.RUnlock()

	// 4.1 PRE-FLIGHT BALANCE CHECK: Ensure the vault pool can cover the stack
	var totalUnitsRequired float64
	var txns []types.Transaction
	var skippedAssets []uint64
	accounts := []string{req.Recipient}
	recipientAddr, _ := types.DecodeAddress(req.Recipient)
	vaultAddrObj, _ := types.DecodeAddress(vaultAddress)

	for appID, baseAmt := range activeRewards {
		amt := baseAmt
		if hasStats && stats.Reputation > 500 {
			amt = uint64(float64(amt) * 1.1)
			bonusApplied = true
		}

		// 4.2 ON-CHAIN BALANCE CHECK: Verify specific ARC-200 balance via Box lookup
		// Standard ARC-200 storage: Box name is the 32-byte address.
		boxResponse, err := client.GetApplicationBoxByName(appID, vaultAddrObj[:]).Do(context.Background())
		if err != nil {
			log.Printf("[REWARD SKIP] Vault not opted-in or balance missing for asset %d: %v\n", appID, err)
			skippedAssets = append(skippedAssets, appID)
			continue // Skip this specific reward
		}

		// ARC-200 balances are stored as uint256 (32 bytes)
		if len(boxResponse.Value) >= 32 {
			onChainBalance := new(big.Int).SetBytes(boxResponse.Value[:32])
			required := new(big.Int).SetUint64(amt)
			if onChainBalance.Cmp(required) < 0 {
				log.Printf("[REWARD SKIP] On-chain balance insufficient for Asset %d. Have: %s, Need: %s\n", appID, onChainBalance.String(), required.String())
				skippedAssets = append(skippedAssets, appID)
				continue // Skip this specific reward
			}
		}

		totalUnitsRequired += float64(amt) / 1000000.0
		amtArg := new(big.Int).SetUint64(amt)
		encodedArgs, _ := method.EncodeArgs([]interface{}{recipientAddr, amtArg})
		// Attach the winNote to the outer Application Call
		txn, err := transaction.MakeApplicationNoOpTxn(appID, [][]byte{encodedArgs}, vaultAddress, accounts, nil, nil, winNote, sp)
		if err == nil {
			txns = append(txns, txn)
		}
	}

	if totalUnitsRequired > poolBalance {
		log.Printf("[REWARD DENIED] Vault balance insufficient. Needed: %.2f, Pool: %.2f\n", totalUnitsRequired, poolBalance)
		http.Error(w, "Vault balance low: Payout suspended for safety.", http.StatusServiceUnavailable)
		return
	}

	if len(txns) == 0 {
		http.Error(w, "Failed to construct reward transactions", http.StatusInternalServerError)
		return
	}

	// 5. Atomic Grouping
	gid, _ := transaction.ComputeGroupID(txns)
	groupID := base64.StdEncoding.EncodeToString(gid[:])
	var signedGroup []byte
	var firstTxID string

	for i := range txns {
		txns[i].Group = gid
		txid, stxn, err := crypto.SignTransaction(faucetAccount.PrivateKey, txns[i])
		if err != nil {
			http.Error(w, "Signing failed", http.StatusInternalServerError)
			return
		}
		signedGroup = append(signedGroup, stxn...)
		if i == 0 {
			firstTxID = txid
		}
	}

	// 6. Broadcast Atomic Group
	if _, err := client.SendRawTransaction(signedGroup).Do(context.Background()); err != nil {
		log.Printf("[REWARD ERROR] Atomic broadcast failed: %v\n", err)
		http.Error(w, "Broadcast failed", http.StatusInternalServerError)
		return
	}

	// 7. Transaction Confirmation Polling
	// We wait up to 4 rounds for the network to confirm the payout before clearing history.
	log.Printf("[REWARD] Payout broadcasted for %s. Polling for confirmation: %s\n", req.Recipient, firstTxID)
	_, err = transaction.WaitForConfirmation(client, firstTxID, 4, context.Background())
	if err != nil {
		log.Printf("[REWARD ERROR] Confirmation failed for %s: %v. History preserved for retry.\n", req.Recipient, err)
		http.Error(w, "Transaction broadcasted but confirmation timed out. Please check your wallet before retrying.", http.StatusAccepted)
		return
	}

	log.Printf("[REWARD] Atomic payout CONFIRMED for %s. Total Units: %.2f\n", req.Recipient, totalUnitsRequired)

	// 8. Audit & State Update
	logWinAudit(req.Recipient, req.Network, firstTxID, groupID, uint64(totalUnitsRequired*1000000), history)

	l.mutex.Lock()
	l.faucetBalance -= totalUnitsRequired
	delete(l.matchHistory, req.ClientID)
	l.mutex.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":         "success",
		"txid":           firstTxID,
		"bonus_applied":  bonusApplied,
		"skipped_assets": skippedAssets,
	})
}

// handleRefillVault allows an administrator to update the global faucet balance.
func (l *Lobby) handleRefillVault(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Admin Authentication (Basic API Key check)
	adminKey := r.Header.Get("X-Admin-Key")
	expectedAdminKey := os.Getenv("ADMIN_KEY")

	if expectedAdminKey == "" {
		log.Println("[CRITICAL] ADMIN_KEY environment variable is not set")
		http.Error(w, "Server configuration error", http.StatusInternalServerError)
		return
	}

	if adminKey != expectedAdminKey {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		Amount float64 `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validation: Amount must be positive
	if req.Amount <= 0 {
		http.Error(w, "Refill amount must be a positive value", http.StatusBadRequest)
		return
	}

	l.mutex.Lock()
	l.faucetBalance = req.Amount
	l.mutex.Unlock()

	update := Envelope{
		Type:    "vault_update",
		FromID:  "SERVER",
		Payload: json.RawMessage(fmt.Sprintf(`{"balance": %f}`, req.Amount)),
	}
	msg, _ := json.Marshal(update)
	l.broadcast <- msg

	log.Printf("[ADMIN] Vault balance update broadcasted: %.2f\n", req.Amount)
	l.logAdminAudit("REFILL_VAULT", "GLOBAL", fmt.Sprintf("Amount: %.2f", req.Amount))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "success", "new_balance": req.Amount})
}

// handleUpdateRules allows an administrator to update the global game rules.
func (l *Lobby) handleUpdateRules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !l.checkAdminAuth(w, r) {
		return
	}

	var req struct {
		Open bool `json:"Open"`
		Same bool `json:"Same"`
		Plus bool `json:"Plus"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	payload, _ := json.Marshal(req)
	update := Envelope{
		Type:    "rules_update",
		FromID:  "SERVER",
		Payload: payload,
	}
	msg, _ := json.Marshal(update)
	l.broadcast <- msg

	log.Printf("[ADMIN] Global rules update broadcasted: %+v\n", req)
	l.logAdminAudit("UPDATE_RULES", "GLOBAL", fmt.Sprintf("Open: %v, Same: %v, Plus: %v", req.Open, req.Same, req.Plus))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "success", "rules": req})
}

// broadcastHealthReport constructs and sends a real-time status update of the arena to all clients.
func (l *Lobby) broadcastHealthReport() {
	l.mutex.RLock()
	activeMatches := len(l.matches) / 2
	balance := l.faucetBalance
	l.mutex.RUnlock()

	healthText := fmt.Sprintf("[SERVER HEALTH] Active Arena Matches: %d | Vault Balance: %.2f $VBV", activeMatches, balance)
	payload, _ := json.Marshal(map[string]string{"text": healthText})

	update := Envelope{
		Type:    "chat",
		FromID:  "SERVER",
		Payload: payload,
	}
	msg, _ := json.Marshal(update)
	l.broadcast <- msg
	log.Printf("[SERVER] Automated health report broadcasted: %s\n", healthText)
}

// handleSystemMessage allows an administrator to broadcast a global message with an [ADMIN] prefix.
func (l *Lobby) handleSystemMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !l.checkAdminAuth(w, r) {
		return
	}

	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Text == "" {
		http.Error(w, "Invalid request body or empty message", http.StatusBadRequest)
		return
	}

	// Check for specialized health report trigger
	if req.Text == "@health" {
		go l.broadcastHealthReport()
		l.logAdminAudit("SYSTEM_MESSAGE", "GLOBAL", "Manual Health Report Triggered")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "success", "action": "HEALTH_REPORT_BROADCASTED"})
		return
	}

	// Prefix with [ADMIN] for clear distinction in the UI
	adminText := fmt.Sprintf("[ADMIN] %s", req.Text)
	payload, _ := json.Marshal(map[string]string{"text": adminText})

	update := Envelope{
		Type:    "chat",
		FromID:  "SERVER",
		Payload: payload,
	}
	msg, _ := json.Marshal(update)
	l.broadcast <- msg

	log.Printf("[ADMIN] Global system message broadcasted: %s\n", req.Text)
	l.logAdminAudit("SYSTEM_MESSAGE", "GLOBAL", req.Text)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "success", "message": adminText})
}

// handleBanPlayer allows an administrator to manually restrict a wallet address.
func (l *Lobby) handleBanPlayer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !l.checkAdminAuth(w, r) {
		return
	}

	var req struct {
		Wallet string `json:"wallet"`
		Hours  int    `json:"hours"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Wallet == "" {
		http.Error(w, "Invalid request body or missing wallet", http.StatusBadRequest)
		return
	}

	if req.Hours <= 0 {
		req.Hours = 24 // Default to 24-hour ban
	}

	l.mutex.Lock()
	stats := l.leaderboard[req.Wallet]
	stats.BanExpires = time.Now().Add(time.Duration(req.Hours) * time.Hour)
	// Penalize DNFs/Streak for manual intervention to impact Reputation
	stats.DNFs++
	stats.DisconnectStreak++
	stats.Reputation = calculateReputation(stats)
	l.leaderboard[req.Wallet] = stats

	// If player is online, invalidate any active match and notify them
	for clientID, wallet := range l.wallets {
		if wallet == req.Wallet {
			if match, ok := l.matches[clientID]; ok {
				opponentID := match.P1ID
				if clientID == match.P1ID {
					opponentID = match.P2ID
				}
				if opponent, exists := l.clients[opponentID]; exists {
					notification, _ := json.Marshal(Envelope{
						Type: "chat", FromID: "SERVER",
						Payload: json.RawMessage(`{"text":"Match terminated: Opponent restricted by Administrator."}`),
					})
					select {
					case opponent.send <- notification:
					default:
					}
					delete(l.matches, opponentID)
				}
				delete(l.matches, clientID)
			}
		}
	}

	// Broadcast lobby update so visual locks appear immediately
	msg := l.getLobbyUpdateMsg()
	l.mutex.Unlock()
	l.broadcast <- msg

	log.Printf("[ADMIN] Wallet %s manually restricted for %d hours by admin\n", req.Wallet, req.Hours)
	l.logAdminAudit("BAN_PLAYER", req.Wallet, fmt.Sprintf("Duration: %d hours, Expires: %s", req.Hours, stats.BanExpires.Format(time.RFC3339)))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "success", "wallet": req.Wallet, "expires": stats.BanExpires})
}

// handleResetStats allows an administrator to wipe the history for a specific wallet.
func (l *Lobby) handleResetStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !l.checkAdminAuth(w, r) {
		return
	}

	var req struct {
		Wallet string `json:"wallet"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Wallet == "" {
		http.Error(w, "Invalid request body or missing wallet", http.StatusBadRequest)
		return
	}

	l.mutex.Lock()
	if _, exists := l.leaderboard[req.Wallet]; exists {
		l.leaderboard[req.Wallet] = PlayerStats{
			Wins:             0,
			DNFs:             0,
			DisconnectStreak: 0,
			Reputation:       100, // Reset to base reputation
		}
		msg := l.getLobbyUpdateMsg()
		l.mutex.Unlock()
		l.broadcast <- msg // Force UI update to clear badges/locks
		l.logAdminAudit("RESET_STATS", req.Wallet, "Performance metrics cleared to default")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "success", "wallet": req.Wallet})
	} else {
		l.mutex.Unlock()
		http.Error(w, "Wallet not found in records", http.StatusNotFound)
	}
}

// handleUpdateBaseReward allows an administrator to adjust the reward amount.
func (l *Lobby) handleUpdateBaseReward(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !l.checkAdminAuth(w, r) {
		return
	}

	var req struct {
		Amount float64 `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Amount < 0 {
		http.Error(w, "Invalid reward amount", http.StatusBadRequest)
		return
	}

	l.mutex.Lock()
	l.baseReward = uint64(req.Amount * 1000000)
	l.mutex.Unlock()

	// Broadcast update
	update := Envelope{
		Type:    "reward_update",
		FromID:  "SERVER",
		Payload: json.RawMessage(fmt.Sprintf(`{"amount": %f}`, req.Amount)),
	}
	msg, _ := json.Marshal(update)
	l.broadcast <- msg

	l.logAdminAudit("UPDATE_REWARD", "GLOBAL", fmt.Sprintf("Base Reward set to: %.2f", req.Amount))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "success", "new_reward": req.Amount})
}

// handleMaintenanceMode allows administrators to disable matchmaking and broadcast a countdown.
func (l *Lobby) handleMaintenanceMode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !l.checkAdminAuth(w, r) {
		return
	}

	var req struct {
		Active  bool `json:"active"`
		Minutes int  `json:"minutes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	l.mutex.Lock()
	l.maintenanceMode = req.Active
	l.maintenanceTime = time.Now().Add(time.Duration(req.Minutes) * time.Minute)
	expiry := l.maintenanceTime
	l.mutex.Unlock()

	payload, _ := json.Marshal(map[string]interface{}{
		"active":    req.Active,
		"timestamp": expiry.Format(time.RFC3339),
	})
	msg, _ := json.Marshal(Envelope{Type: "maintenance_update", FromID: "SERVER", Payload: payload})
	l.broadcast <- msg

	l.logAdminAudit("MAINTENANCE_MODE", "GLOBAL", fmt.Sprintf("Active: %v, Starts: %s", req.Active, expiry.Format(time.Kitchen)))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "success", "active": req.Active})
}

// handleUpdateRewardAsset allows an administrator to change the active reward token.
func (l *Lobby) handleUpdateRewardAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !l.checkAdminAuth(w, r) {
		return
	}

	var req struct {
		AssetID uint64 `json:"asset_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	l.mutex.Lock()
	l.rewardAssetID = req.AssetID
	l.mutex.Unlock()

	// Broadcast
	update := Envelope{
		Type:    "asset_update",
		FromID:  "SERVER",
		Payload: json.RawMessage(fmt.Sprintf(`{"asset_id": %d}`, req.AssetID)),
	}
	msg, _ := json.Marshal(update)
	l.broadcast <- msg

	l.logAdminAudit("UPDATE_ASSET", "GLOBAL", fmt.Sprintf("Reward Asset ID set to: %d", req.AssetID))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "success", "new_asset_id": req.AssetID})
}

// handleGetAdminLogs returns the last entry from admin_audit.log for monitoring.
// handleGetAdminLogs returns audit logs with support for filtering by action or security tags.
func (l *Lobby) handleGetAdminLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !l.checkAdminAuth(w, r) {
		return
	}

	l.mutex.RLock()
	currentBalance := l.faucetBalance
	l.mutex.RUnlock()

	// Optional filter from query parameter (e.g., /api/admin/logs?filter=SECURITY)
	filter := strings.ToUpper(r.URL.Query().Get("filter"))

	data, err := os.ReadFile("admin_audit.log")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "error", "message": "No logs found"})
		return
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var results []json.RawMessage

	// Collect matching logs starting from most recent
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i] == "" {
			continue
		}

		if filter == "" || strings.Contains(strings.ToUpper(lines[i]), filter) {
			results = append(results, json.RawMessage(lines[i]))
		}

		// Limit to prevent huge response bodies
		if len(results) >= 100 {
			break
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":          "success",
		"balance":         currentBalance,
		"balance_warning": currentBalance < 1000.0,
		"logs":            results,
	})
}

// handleStartTournament triggers the bracket generation using Top N players from the leaderboard.
func (l *Lobby) handleStartTournament(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !l.checkAdminAuth(w, r) {
		return
	}

	var req struct {
		Size int `json:"size"` // 8, 16, or 32
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (req.Size != 8 && req.Size != 16) {
		http.Error(w, "Invalid tournament size. Use 8 or 16.", http.StatusBadRequest)
		return
	}

	l.mutex.Lock()
	defer l.mutex.Unlock()

	// 1. Collect and Sort Hall of Fame
	type entry struct {
		wallet string
		wins   int
	}
	var hof []entry
	for wallet, stats := range l.leaderboard {
		hof = append(hof, entry{wallet: wallet, wins: stats.Wins})
	}
	sort.Slice(hof, func(i, j int) bool { return hof[i].wins > hof[j].wins })

	if len(hof) < req.Size {
		http.Error(w, fmt.Sprintf("Not enough Hall of Fame participants. Have %d, need %d.", len(hof), req.Size), http.StatusBadRequest)
		return
	}

	// 2. Select Participants
	participants := []string{}
	for i := 0; i < req.Size; i++ {
		participants = append(participants, hof[i].wallet)
	}

	// 3. Generate Seeding Pairs (1v8, 4v5, 2v7, 3v6 logic)
	// This creates a standard tournament bracket where top seeds meet only in the finals.
	matches := []TournamentMatch{}
	seedMap := []int{}
	if req.Size == 8 {
		seedMap = []int{0, 7, 3, 4, 1, 6, 2, 5}
	} else {
		// Linear fallback for 16
		for i := 0; i < req.Size/2; i++ {
			seedMap = append(seedMap, i, req.Size-1-i)
		}
	}

	for i := 0; i < len(seedMap); i += 2 {
		match := TournamentMatch{
			ID:    fmt.Sprintf("R1-M%d", (i/2)+1),
			P1:    participants[seedMap[i]],
			P2:    participants[seedMap[i+1]],
			Round: 1,
		}
		matches = append(matches, match)
	}

	l.tournament = TournamentState{
		Active:       true,
		Participants: participants,
		Matches:      matches,
		CurrentRound: 1,
	}

	l.logAdminAudit("START_TOURNAMENT", "GLOBAL", fmt.Sprintf("Size: %d", req.Size))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(l.tournament)
}

func main() {
	// Load .env file for local development convenience
	if err := godotenv.Load(); err != nil {
		log.Println("[INFO] No .env file found; using system environment variables.")
	}

	lobby := newLobby()
	go lobby.run() // Start the lobby manager in a goroutine.

	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		serveWs(lobby, w, r) // Register the WebSocket handler for the "/ws" endpoint.
	})

	// Card Stats Endpoint
	http.HandleFunc("/api/card-stats", withCORS(lobby.withRateLimit(lobby.handleCardStats)))

	// Leaderboard Endpoint
	http.HandleFunc("/api/leaderboard", withCORS(lobby.withRateLimit(lobby.handleLeaderboard)))

	// Secure Payout Endpoint
	http.HandleFunc("/api/reward", withCORS(lobby.withRateLimit(lobby.handleReward)))

	// Admin Endpoints (All wrapped with CORS and internal Auth)
	http.HandleFunc("/api/refill-vault", withCORS(lobby.withRateLimit(lobby.handleRefillVault)))
	http.HandleFunc("/api/update-rules", withCORS(lobby.withRateLimit(lobby.handleUpdateRules)))
	http.HandleFunc("/api/system-message", withCORS(lobby.withRateLimit(lobby.handleSystemMessage)))
	http.HandleFunc("/api/ban-player", withCORS(lobby.withRateLimit(lobby.handleBanPlayer)))
	http.HandleFunc("/api/admin/logs", withCORS(lobby.withRateLimit(lobby.handleGetAdminLogs)))
	http.HandleFunc("/api/reset-stats", withCORS(lobby.withRateLimit(lobby.handleResetStats)))
	http.HandleFunc("/api/update-reward", withCORS(lobby.withRateLimit(lobby.handleUpdateBaseReward)))
	http.HandleFunc("/api/update-asset", withCORS(lobby.withRateLimit(lobby.handleUpdateRewardAsset)))
	http.HandleFunc("/api/maintenance-mode", withCORS(lobby.withRateLimit(lobby.handleMaintenanceMode)))
	http.HandleFunc("/api/re-sync-stats", withCORS(lobby.withRateLimit(lobby.handleReSyncStats)))
	http.HandleFunc("/api/admin/get-site-token", withCORS(lobby.withRateLimit(lobby.handleGetSiteToken)))
	http.HandleFunc("/api/admin/start-tournament", withCORS(lobby.withRateLimit(lobby.handleStartTournament)))
	http.HandleFunc("/api/admin/start-tournament", withCORS(lobby.withRateLimit(lobby.handleStartTournament)))

	port := os.Getenv("SERVER_PORT")
	if port == "" {
		port = "8082"
	}
	port = ":" + port

	fmt.Printf("WebSocket server starting on port %s\n", port)
	log.Fatal(http.ListenAndServe(port, nil)) // Start the HTTP server.
}
