package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
	"syscall/js"
	"strings"
	"sort"
	"time"
)

// -----------------------------------------------------------------------------
// 1. ASSET EMBEDDING
// -----------------------------------------------------------------------------
//go:embed Assets/*.mp3
//go:embed Assets/*.wav
//go:embed Assets/cute/*.mp3
//go:embed Assets/Lady/*.mp3
//go:embed Assets/Witch/*.mp3
//go:embed Public_assets/Ambient_sfx/*.mp3
var soundAssets embed.FS

// -----------------------------------------------------------------------------
// 2. DATA VAULT (The Unified State Machine)
// -----------------------------------------------------------------------------

type MetadataAttribute struct {
	TraitType string      `json:"trait_type"`
	Value     interface{} `json:"value"`
}

type ARC72Metadata struct {
	Name       string              `json:"name"`
	Image      string              `json:"image"`
	Attributes []MetadataAttribute `json:"attributes"`
}

type Card struct {
	ID        int     `json:"id"`
	Name      string  `json:"name"`
	Power     [4]int  `json:"power"` // [Top, Right, Bottom, Left]
	Owner     int     `json:"owner"` // 0 for Player 1, 1 for Player 2
	Image     string  `json:"image"`
	Tier      string  `json:"tier"`       // Iron, Bronze, Gold, Diamond
	GlowColor string  `json:"glow_color"` // Hex color for UI effects
	IsCombo   bool    `json:"is_combo"`   // True if flipped during a chain reaction
	Rarity    float64 `json:"rarity"`     // Power multiplier based on supply
}

type Player struct {
	ID         string    `json:"id"`
	Wallet     string    `json:"wallet"` // The connected blockchain address
	Decks      [4][]Card `json:"decks"`  // 4 saved deck slots
	ActiveDeck int       `json:"active_deck"`
	Ready      bool      `json:"ready"`
	Reputation int       `json:"reputation"`
	AvatarURL  string    `json:"avatar_url"`
}

// Engine acts as the supreme state machine for the entire App
type Engine struct {
	Network     string
	Faucet      float64
	Phase       string          // "Lobby", "Active", "Finished"
	Rules       map[string]bool // Holds Custom Rules (Open, Same, Plus)
	Rewards     map[uint64]float64
	Inventory   []Card    // Global pool of cards to pick from
	
	// Asset Pools for Demo/AI
	DemoPool    []string 
	WitchPool   []string
	LadyPool    []string

	Players     [2]Player // 2P Lobby System
	Board       [9]*Card  // 3x3 Battle Grid
	Multiplayer bool      // True if playing against a human, false for AI
	Turn        int       // 0 for Player 1, 1 for Player 2
	Scores      [2]int    // Final scores [P1, P2]
	Maintenance bool      // True if the arena is under maintenance
	TestingMode bool      // If true, Player 1 always wins against AI
	IsAdmin     bool      // True if the connected wallet is an administrator
	Winner      int       // -1: None, 0: P1, 1: P2, 2: Draw
	AssetBase   string    // The CDN URL for sounds/images (e.g., GitHub Pages)
	AmbientAudio js.Value // Current background music object
	CurrentAmbientTrack string
	ShowLeaderboard bool  // UI Toggle for Hall of Fame
	HardMode    bool      // If true, AI uses tactical weighted scoring
	AIScore     int       // Tactical value of the bot's intended move
	ServerLoad  int       // Current active matches on the server
	SpecialFanfare string // Archetype for specific win/loss tracks: "Emotional", "Witch"
	SiteVerified bool     // Integrity flag for official domain handshake
	VaultLow    bool      // Warning flag for low faucet balance
	DeckRating  string    // Current player's active deck rating (e.g., [A++])
	Latency     int       // WebSocket ping in milliseconds
	NetworkHealth string  // "Excellent", "Good", "Poor", "Critical"
}

// Initialize the single source of truth
var Game = Engine{
	Network:     "VOI",
	Faucet:      1000.0,
	Phase:       "Lobby",
	Rules:       map[string]bool{"Open": true, "Same": false, "Plus": false},
	Rewards:     map[uint64]float64{40227315: 5.0},
	Players:     [2]Player{{ID: "Player 1"}, {ID: "Player 2"}},
	Board:       [9]*Card{},
	Multiplayer: false,
	Turn:        0,
	Winner:      -1,
	DemoPool: []string{
		"Public_assets/4kvisuals/Images/portraits/cute/Easy/Alana.webp",
		"Public_assets/4kvisuals/Images/portraits/cute/Easy/Bella.webp",
		"Public_assets/4kvisuals/Images/portraits/cute/Easy/Clohey.webp",
		"Public_assets/4kvisuals/Images/portraits/cute/Easy/Ellie.webp",
		"Public_assets/4kvisuals/Images/portraits/cute/Easy/Fran.webp",
		"Public_assets/4kvisuals/Images/portraits/cute/Easy/Karren.webp",
		"Public_assets/4kvisuals/Images/portraits/cute/Easy/Kat.webp",
		"Public_assets/4kvisuals/Images/portraits/cute/Easy/Kay.webp",
		"Public_assets/4kvisuals/Images/portraits/cute/Easy/Lucy.webp",
		"Public_assets/4kvisuals/Images/portraits/cute/Easy/Pip.webp",
		"Public_assets/4kvisuals/Images/portraits/cute/Easy/Roxy.webp",
		"Public_assets/4kvisuals/Images/portraits/cute/Easy/Sally.webp",
		"Public_assets/4kvisuals/Images/portraits/cute/Easy/Tammara.webp",
		"Public_assets/4kvisuals/Images/portraits/cute/Easy/Taya.webp",
		"Public_assets/4kvisuals/Images/portraits/cute/Easy/Triz.webp",
		"Public_assets/4kvisuals/Images/portraits/cute/Easy/Xai.webp",
	},
	TestingMode: false,
	HardMode:    false,
	AIScore:     0,
	ServerLoad:  0,
	SpecialFanfare: "",
	SiteVerified: false,
	VaultLow:    false,
	DeckRating:  "[Z]",
	Latency:     0,
	NetworkHealth: "Excellent",
	AssetBase:   "", // Default to relative, can be set via SetAssetBase
}

// -----------------------------------------------------------------------------
// 3. FAUCET & NETWORK (The Ecosystem)
// -----------------------------------------------------------------------------

func connectWallet(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return map[string]interface{}{"status": "error", "message": "No address provided"}
	}
	address := args[0].String()
	Game.Players[0].Wallet = address
	Game.Players[0].ID = address[:6] + "..." + address[len(address)-4:]
	
	// Transition to Setup Phase for Avatar selection
	Game.Phase = "Setup"

	fmt.Printf("[ENGINE] Wallet %s Connected to: %s\n", address, Game.Network)
	PlaySound("click")
	UpdateAmbientMusic()
	return map[string]interface{}{"status": "success", "address": address, "network": Game.Network}
}

func disconnectWallet(this js.Value, args []js.Value) interface{} {
	Game.Players[0].Wallet = ""
	Game.Players[0].ID = "Player 1"
	Game.IsAdmin = false
	Game.Players[0].Ready = false
	fmt.Println("[ENGINE] Wallet Disconnected.")
	PlaySound("click")
	UpdateAmbientMusic()
	return true
}

func toggleNetwork(this js.Value, args []js.Value) interface{} {
	if Game.Network == "VOI" {
		Game.Network = "ALGO"
	} else {
		Game.Network = "VOI"
	}
	fmt.Printf("[ENGINE] Network Switched to: %s\n", Game.Network)
	PlaySound("click")
	return Game.Network
}

// SetAvatar sets the player's profile image and transitions the game to Lobby phase
func SetAvatar(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return false
	}
	url := args[0].String()
	Game.Players[0].AvatarURL = url
	Game.Phase = "Lobby"
	fmt.Println("[ENGINE] Avatar Set. Transitioning to LOBBY.")
	PlaySound("Gear_up_shot")
	return true
}

func SendReward(this js.Value, args []js.Value) interface{} {
	recipientAddr := Game.Players[0].Wallet
	if recipientAddr == "" {
		fmt.Println("[FAUCET ERROR] No wallet connected. Payout aborted.")
		return Game.Faucet
	}

	// Decrement locally for immediate UI feedback.
	for _, amt := range Game.Rewards {
		Game.Faucet -= amt
	}

	// Payout is now handled by the secure backend to prevent mnemonic exposure.
	// We use a goroutine to trigger a JS fetch call so we don't block the WASM thread.
	go func() {
		fmt.Printf("[ENGINE] Requesting Payout for %s via Backend...\n", recipientAddr)

		clientID := js.Global().Get("myClientId").String()

		payload, _ := json.Marshal(map[string]interface{}{
			"recipient":    recipientAddr,
			"network":      Game.Network,
			"client_id":    clientID,
			"client_score": Game.Scores,
		})

		// Hand off to JavaScript to manage the UI feedback and Transaction lifecycle
		js.Global().Call("processRewardPayout", string(payload))
	}()

	return Game.Faucet
}

// VerifySiteHandshake receives the result of the site authenticity check from the JS frontend.
func VerifySiteHandshake(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return false
	}
	success := args[0].Bool()
	if success {
		Game.SiteVerified = true
		fmt.Println("[SECURITY] Site Authenticity Verified. Authoritative Payouts Enabled.")
	} else {
		Game.SiteVerified = false
		fmt.Println("[SECURITY] Site Verification Failed! Payouts Restricted.")
	}
	return Game.SiteVerified
}

// -----------------------------------------------------------------------------
// 4. LOBBY & DECK LOGIC (The Preparation)
// -----------------------------------------------------------------------------

func ToggleRule(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return false
	}
	rule := args[0].String()

	Game.Rules[rule] = !Game.Rules[rule]
	fmt.Printf("[LOBBY] Rule '%s' set to: %v\n", rule, Game.Rules[rule])
	PlaySound("click")
	return Game.Rules[rule]
}

// PlaySelectSound triggers the card selection audio feedback
func PlaySelectSound(this js.Value, args []js.Value) interface{} {
	PlaySound("Select-place-card")
	return nil
}

// SelectDeck changes the active deck slot for a player after checking reputation
func SelectDeck(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return false
	}
	slot := args[0].Int()
	if slot < 0 || slot > 3 {
		return false
	}

	// Reputation Thresholds
	thresholds := [4]int{0, 250, 600, 1000}
	if Game.Players[0].Reputation < thresholds[slot] {
		fmt.Printf("[ENGINE] Deck slot %d locked. Need %d Reputation.\n", slot+1, thresholds[slot])
		return false
	}

	Game.Players[0].ActiveDeck = slot
	fmt.Printf("[ENGINE] Deck slot %d selected.\n", slot+1)
	PlaySound("Gear_up_shot")
	return true
}

// RemoveFromDeck clears a specific card from the current active deck
func RemoveFromDeck(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return false
	}
	cardIdx := args[0].Int()
	p := &Game.Players[0]
	if cardIdx >= 0 && cardIdx < len(p.Decks[p.ActiveDeck]) {
		p.Decks[p.ActiveDeck] = append(p.Decks[p.ActiveDeck][:cardIdx], p.Decks[p.ActiveDeck][cardIdx+1:]...)
		PlaySound("click")
		return true
	}
	return false
}

func AddToDeck(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return false
	}
	cardID := args[0].Int()
	p := &Game.Players[0]
	activeSlot := p.ActiveDeck

	// Guardrail: Max 5 cards per deck
	if len(p.Decks[activeSlot]) >= 5 {
		return false
	}

	// Prevent Duplicates: Check if the card is already in the player's deck
	for _, dc := range p.Decks[activeSlot] {
		if dc.ID == cardID {
			return false
		}
	}

	if c, found := findCard(cardID); found {
		c.Owner = 0
		p.Decks[activeSlot] = append(p.Decks[activeSlot], c)
		PlaySound("click")
		UpdateAmbientMusic()
		return true
	}
	return false
}

func SetPlayerReady(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return false
	}
	pIndex := args[0].Int()

	// HOTSEAT SIMULATOR: Auto-generate a deck for Player 2 if they are empty
	if !Game.Multiplayer && pIndex == 1 && len(Game.Players[1].Decks[0]) == 0 {
		fmt.Println("[ENGINE] Generating Demo Deck for CPU...")
		Game.Players[1].ID = "Vbabe Bot"
		Game.Players[1].AvatarURL = Game.DemoPool[rand.Intn(len(Game.DemoPool))]
		Game.Players[1].ActiveDeck = 0
		for i := 0; i < 5; i++ {
			img := Game.DemoPool[i % len(Game.DemoPool)]
			simCard := GenerateCard(1000+i, fmt.Sprintf("Demo Babe %d", i+1), 60.0)
			simCard.Owner = 1
			simCard.Image = img
			Game.Players[1].Decks[0] = append(Game.Players[1].Decks[0], simCard)
		}
	}

	p := &Game.Players[pIndex]
	if len(p.Decks[p.ActiveDeck]) == 5 {
		Game.Players[pIndex].Ready = true
		fmt.Printf("[LOBBY] %s is READY.\n", Game.Players[pIndex].ID)
		PlaySound("click")
	}

	// Trigger UI Start Button if both are ready
	updateStartButton()
	return true
}

// SyncPlayerStats updates reputation and other metrics for players in the lobby
func SyncPlayerStats(this js.Value, args []js.Value) interface{} {
	if len(args) < 2 {
		return false
	}
	pIdx := args[0].Int()
	rep := args[1].Int()

	if pIdx >= 0 && pIdx < 2 {
		Game.Players[pIdx].Reputation = rep
	}
	return true
}

// SyncServerLoad updates the current count of active matches from the lobby
func SyncServerLoad(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return false
	}
	Game.ServerLoad = args[0].Int()
	return true
}

// SyncLatency updates the engine's network performance state from the JS WebSocket
func SyncLatency(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return false
	}
	ms := args[0].Int()
	Game.Latency = ms

	if ms < 100 {
		Game.NetworkHealth = "Excellent"
	} else if ms < 300 {
		Game.NetworkHealth = "Good"
	} else if ms < 500 {
		Game.NetworkHealth = "Poor"
	} else {
		Game.NetworkHealth = "Critical"
	}
	return true
}

// startLatencyMonitor runs in a background goroutine to periodically trigger pings via JS
func startLatencyMonitor() {
	go func() {
		for {
			// Ping the server every 15 seconds to monitor connection health
			time.Sleep(15 * time.Second)
			js.Global().Call("sendPing")
		}
	}()
}

// AutoBuildDeck picks the 5 strongest cards from inventory, prioritizing the highest possible 
// letter grade and maximizing the '+' count (number of cards in that tier).
func AutoBuildDeck(this js.Value, args []js.Value) interface{} {
	if len(Game.Inventory) < 5 {
		fmt.Println("[ENGINE] Not enough cards to auto-build.")
		return false
	}

	// 1. Create a copy of inventory to sort
	tempInv := make([]Card, len(Game.Inventory))
	copy(tempInv, Game.Inventory)

	// 2. Sort by Tactical Tiering
	// Primary: Highest Letter Grade (Bin) - e.g., A > B
	// Secondary: Power Sum * Scarcity (Battle Score)
	sort.Slice(tempInv, func(i, j int) bool {
		getMaxBin := func(card Card) int {
			maxP := 1
			for _, p := range card.Power {
				if p > maxP { maxP = p }
			}
			return (maxP - 1) / 100
		}

		binI := getMaxBin(tempInv[i])
		binJ := getMaxBin(tempInv[j])

		if binI != binJ {
			return binI > binJ
		}

		scoreI := float64(tempInv[i].Power[0]+tempInv[i].Power[1]+tempInv[i].Power[2]+tempInv[i].Power[3]) * tempInv[i].Rarity
		scoreJ := float64(tempInv[j].Power[0]+tempInv[j].Power[1]+tempInv[j].Power[2]+tempInv[j].Power[3]) * tempInv[j].Rarity
		return scoreI > scoreJ
	})

	// 3. Populate active deck
	p := &Game.Players[0]
	p.Decks[p.ActiveDeck] = []Card{}
	for i := 0; i < 5; i++ {
		c := tempInv[i]
		c.Owner = 0
		p.Decks[p.ActiveDeck] = append(p.Decks[p.ActiveDeck], c)
	}

	fmt.Printf("[ENGINE] Auto-Built Deck %d (Max Tier Strategy).\n", p.ActiveDeck+1)
	PlaySound("Gear_up_shot")
	return true
}

// SetTestingMode toggles the 100% win rate against AI for rapid development
func SetTestingMode(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return false
	}
	Game.TestingMode = args[0].Bool()
	fmt.Printf("[ENGINE] Testing Mode set to: %v\n", Game.TestingMode)
	return true
}

// SetHardMode toggles the tactical weighted scoring for the AI bot
func SetHardMode(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return false
	}
	Game.HardMode = args[0].Bool()
	fmt.Printf("[ENGINE] Hard Mode AI: %v\n", Game.HardMode)
	return true
}

// updateStartButton re-evaluates if the "Start Battle" button should be enabled
func updateStartButton() {
	canStart := (Game.Players[0].Ready && Game.Players[1].Ready) && (!Game.Maintenance || Game.IsAdmin)
	js.Global().Call("highlightStartButton", canStart)
}

// SetAdminState allows manual override of admin status (e.g., from server or testing)
func SetAdminState(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return false
	}
	Game.IsAdmin = args[0].Bool()
	fmt.Printf("[ENGINE] Admin State manually set to: %v\n", Game.IsAdmin)
	updateStartButton()
	return true
}

// SetMaintenanceState informs the engine about the arena's maintenance status
func SetMaintenanceState(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return false
	}
	Game.Maintenance = args[0].Bool()
	fmt.Printf("[ENGINE] Maintenance Mode set to: %v\n", Game.Maintenance)
	updateStartButton()
	return true
}

// SyncOpponentDeck populates a player's deck from a list of IDs (used for Multiplayer Handshakes)
func SyncOpponentDeck(this js.Value, args []js.Value) interface{} {
	if len(args) < 2 {
		return false
	}
	pIndex := args[0].Int()
	idsVal := args[1]

	if idsVal.Type() != js.TypeObject {
		return false
	}

	// Clear existing deck for the specified player
	Game.Players[pIndex].Decks[0] = []Card{}

	for i := 0; i < idsVal.Length(); i++ {
		id := idsVal.Index(i).Int()
		if c, found := findCard(id); found {
			c.Owner = pIndex
			Game.Players[pIndex].Decks[0] = append(Game.Players[pIndex].Decks[0], c)
		}
	}

	Game.Players[pIndex].Ready = true
	return true
}

// ForceActive allows spectators to bypass lobby requirements and enter combat mode
func ForceActive(this js.Value, args []js.Value) interface{} {
	Game.Phase = "Active"
	Game.Board = [9]*Card{} // Clear local board for fresh sync
	fmt.Println("[ENGINE] Phase forced to ACTIVE for Spectating.")
	return true
}

// SyncVaultBalance updates the local faucet state from a server broadcast
func SyncVaultBalance(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return false
	}
	newBalance := args[0].Float()
	Game.Faucet = newBalance
	Game.VaultLow = newBalance < 1000.0
	fmt.Printf("[ENGINE] Vault Balance Synced: %.2f $VBV\n", Game.Faucet)
	return true
}

// TriggerManualSync allows the UI to force a server-side re-sync of the player's on-chain stats.
func TriggerManualSync(this js.Value, args []js.Value) interface{} {
	wallet := Game.Players[0].Wallet
	if wallet == "" {
		fmt.Println("[ENGINE ERROR] Cannot trigger sync: No wallet connected.")
		return false
	}

	go func() {
		fmt.Printf("[ENGINE] Requesting manual stats re-sync for %s...\n", wallet)
		
		payload, _ := json.Marshal(map[string]string{"wallet": wallet})
		window := js.Global()

		// Construct fetch options
		options := window.Get("Object").New()
		options.Set("method", "POST")
		headers := window.Get("Object").New()
		headers.Set("Content-Type", "application/json")
		options.Set("headers", headers)
		options.Set("body", string(payload))

		// Execute fetch to the backend endpoint
		promise := window.Call("fetch", "/api/re-sync-stats", options)
		
		success := js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			fmt.Println("[ENGINE] Manual sync initiated successfully.")
			return nil
		})
		
		failure := js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			fmt.Printf("[ENGINE ERROR] Manual sync request failed: %v\n", args[0])
			return nil
		})

		promise.Call("then", success).Call("catch", failure)
	}()

	return true
}

// SyncRewards updates the multi-reward registry from server
func SyncRewards(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return false
	}
	jsMap := args[0]
	Game.Rewards = make(map[uint64]float64)

	keys := js.Global().Get("Object").Call("keys", jsMap)
	for i := 0; i < keys.Length(); i++ {
		k := keys.Index(i).String()
		id, _ := strconv.ParseUint(k, 10, 64)
		Game.Rewards[id] = jsMap.Get(k).Float() / 1000000.0 // Convert micro to base
	}

	fmt.Printf("[ENGINE] Multi-Rewards Synced: %d active assets\n", len(Game.Rewards))
	return true
}

// SyncRules updates the internal rule set from a server broadcast (Admin control)
func SyncRules(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return false
	}
	jsRules := args[0]
	if jsRules.Type() != js.TypeObject {
		return false
	}

	Game.Rules["Open"] = jsRules.Get("Open").Bool()
	Game.Rules["Same"] = jsRules.Get("Same").Bool()
	Game.Rules["Plus"] = jsRules.Get("Plus").Bool()

	fmt.Printf("[ENGINE] Rules Synchronized: %v\n", Game.Rules)
	return true
}

// SetBoardState bulk-loads the 3x3 grid and rules (used for Spectator Sync)
func SetBoardState(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return false
	}
	data := args[0]

	jsBoard := data.Get("board")
	if jsBoard.Type() == js.TypeObject {
		for i := 0; i < 9; i++ {
			val := jsBoard.Index(i)
			if val.IsNull() || val.IsUndefined() {
				Game.Board[i] = nil
				continue
			}
			id := val.Get("id").Int()
			owner := val.Get("owner").Int()
			if c, found := findCard(id); found {
				c.Owner = owner
				// Create a new pointer instance for the board
				heapCard := new(Card)
				*heapCard = c
				Game.Board[i] = heapCard
			}
		}
	}

	jsRules := data.Get("rules")
	if jsRules.Type() == js.TypeObject {
		Game.Rules["Open"] = jsRules.Get("Open").Bool()
		Game.Rules["Same"] = jsRules.Get("Same").Bool()
		Game.Rules["Plus"] = jsRules.Get("Plus").Bool()
	}

	fmt.Println("[ENGINE] Board and Rules Synced from Server.")
	return true
}

// findCard is a private helper to retrieve a card from the global inventory by ID
func findCard(id int) (Card, bool) {
	for _, c := range Game.Inventory {
		if c.ID == id {
			return c, true
		}
	}
	return Card{}, false
}

func ResetGame(this js.Value, args []js.Value) interface{} {
	Game.Phase = "Lobby"
	Game.Board = [9]*Card{}
	Game.Multiplayer = false
	Game.Turn = 0
	Game.Winner = -1
	Game.Scores = [2]int{0, 0}
	Game.SpecialFanfare = ""

	// Clear player readiness and decks
	for i := range Game.Players {
		Game.Players[i].Ready = false
		for d := 0; d < 4; d++ {
			Game.Players[i].Decks[d] = []Card{}
		}
	}

	fmt.Println("[ENGINE] Game Reset to Lobby. State Cleared.")
	PlaySound("click")
	return true
}

// -----------------------------------------------------------------------------
// 5. THE WAR ROOM (The Combat Grid)
// -----------------------------------------------------------------------------

func StartMatch(this js.Value, args []js.Value) interface{} {
	if Game.Maintenance && !Game.IsAdmin {
		fmt.Println("[ENGINE ERROR] Cannot start match: Maintenance in progress.")
		return false
	}

	if !Game.Players[0].Ready || !Game.Players[1].Ready {
		return false
	}

	// Optional: Check if StartMatch(true) was passed for Multiplayer
	Game.Multiplayer = false
	if len(args) > 0 && args[0].Type() == js.TypeBoolean {
		Game.Multiplayer = args[0].Bool()
	}

	if Game.Multiplayer {
		Game.Players[1].ID = "Opponent"
	}

	Game.Phase = "Active"
	Game.Turn = 0           // Player 1 starts
	Game.Board = [9]*Card{} // Clear board
	Game.Winner = -1
	Game.Scores = [2]int{0, 0}

	UpdateAmbientMusic()
	fmt.Println("=================================")
	fmt.Printf(" BATTLE START! Rules: %v\n", Game.Rules)
	fmt.Println("=================================")
	PlaySound("flip")
	return true
}

func PlaceCard(this js.Value, args []js.Value) interface{} {
	if Game.Phase != "Active" || len(args) < 2 {
		return false
	}

	gridIndex := args[0].Int()
	cardID := args[1].Int()

	// Reset combo flags for all cards on board at the start of a move
	for _, boardCard := range Game.Board {
		if boardCard != nil {
			boardCard.IsCombo = false
		}
	}

	// Reset AI score when the player takes their turn
	Game.AIScore = 0

	// Guardrails: Check if grid is valid and empty
	if gridIndex < 0 || gridIndex > 8 || Game.Board[gridIndex] != nil {
		fmt.Println("[BATTLE ERROR] Invalid or occupied slot.")
		return false
	}

	pIndex := Game.Turn

	p := &Game.Players[pIndex]
	for i, c := range p.Decks[p.ActiveDeck] {
		if c.ID == cardID {
			Game.Board[gridIndex] = &p.Decks[p.ActiveDeck][i]

			// Apply Visual Tier Effects based on the player's current reputation
			tier, color, _ := calculateTier(Game.Players[pIndex].Reputation)
			Game.Board[gridIndex].Tier = tier
			Game.Board[gridIndex].GlowColor = color

			fmt.Printf("[BATTLE] %s placed %s at Grid %d\n", Game.Players[pIndex].ID, c.Name, gridIndex)
			
			checkCaptures(&p.Decks[p.ActiveDeck][i], gridIndex) 
			PlaySound("Select-place-card")

			// Switch Turn
			if Game.Turn == 0 {
				Game.Turn = 1
				
				// TACTICAL REFACTOR: Implement 'Lag Guard' for AI triggers
				if !Game.Multiplayer {
					go func() {
						// If network is critical, wait a short moment for the socket to settle
						if Game.NetworkHealth == "Critical" {
							fmt.Printf("[LAG GUARD] Latency at %dms. Delaying AI response for stability...\n", Game.Latency)
							time.Sleep(2 * time.Second)
						}
						PerformAIMove()
					}()
				}
			} else {
				Game.Turn = 0
			}

			checkWinCondition()
			UpdateAmbientMusic()
			return true
		}
	}
	return false
}

// checkCaptures applies the combat logic for a newly placed card
func checkCaptures(placedCard *Card, gridIndex int) int {
	totalFlips := 0
	// Define relative indices for neighbors and corresponding power indices
	// Power: [Top, Right, Bottom, Left]
	// {offset_from_current_index, placed_card_power_index, neighbor_card_power_index, boundary_check_function}
	neighbors := []struct {
		offset           int
		placedPowerIdx   int
		neighborPowerIdx int
		boundaryCheck    func(int) bool // Function to check if neighbor is within bounds
	}{
		{-3, 0, 2, func(idx int) bool { return idx >= 3 }},   // Top: placed.Top vs neighbor.Bottom
		{+1, 1, 3, func(idx int) bool { return idx%3 != 2 }}, // Right: placed.Right vs neighbor.Left
		{+3, 2, 0, func(idx int) bool { return idx <= 5 }},   // Bottom: placed.Bottom vs neighbor.Top
		{-1, 3, 1, func(idx int) bool { return idx%3 != 0 }}, // Left: placed.Left vs neighbor.Right
	}

	// Groups to track rule matches (Value/Sum -> list of neighbor indices)
	sameGroups := make(map[int][]int)
	plusGroups := make(map[int][]int)

	var comboQueue []int // Indices of cards flipped by Same/Plus to start combos

	for _, n := range neighbors {
		neighborIndex := gridIndex + n.offset

		// Check if the neighbor index is within board bounds and the slot is occupied
		if n.boundaryCheck(gridIndex) && Game.Board[neighborIndex] != nil {
			neighborCard := Game.Board[neighborIndex]
			placedPower := placedCard.Power[n.placedPowerIdx]
			neighborPower := neighborCard.Power[n.neighborPowerIdx]

			// 1. Prepare Same Rule Data (Equality check)
			if Game.Rules["Same"] && placedPower == neighborPower {
				sameGroups[placedPower] = append(sameGroups[placedPower], neighborIndex)
			}

			// 2. Prepare Plus Rule Data (Sum check)
			if Game.Rules["Plus"] {
				sum := placedPower + neighborPower
				plusGroups[sum] = append(plusGroups[sum], neighborIndex)
			}

			// 3. Basic Capture (Direct Power Comparison)
			if neighborCard.Owner != placedCard.Owner && placedPower > neighborPower {
				if flipCard(neighborIndex, placedCard.Owner, "BASIC") {
					totalFlips++
				}
			}
		}
	}

	// 4. Process Same Rule triggers (Requires at least 2 matching sides)
	for _, indices := range sameGroups {
		if len(indices) >= 2 {
			for _, idx := range indices {
				if flipCard(idx, placedCard.Owner, "SAME") {
					totalFlips++
					comboQueue = append(comboQueue, idx)
				}
			}
		}
	}

	// 5. Process Plus Rule triggers (Requires at least 2 matching sums)
	for _, indices := range plusGroups {
		if len(indices) >= 2 {
			for _, idx := range indices {
				if flipCard(idx, placedCard.Owner, "PLUS") {
					totalFlips++
					comboQueue = append(comboQueue, idx)
				}
			}
		}
	}

	// 6. Process Combo Chain (Recursive Basic Captures)
	for len(comboQueue) > 0 {
		currentIndex := comboQueue[0]
		comboQueue = comboQueue[1:]
		currentCard := Game.Board[currentIndex]

		// Define neighbors for the combo card
		comboNeighbors := []struct {
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

		for _, cn := range comboNeighbors {
			nbIdx := currentIndex + cn.offset
			if cn.boundaryCheck(currentIndex) && Game.Board[nbIdx] != nil {
				neighbor := Game.Board[nbIdx]
				// Combo only triggers Basic Capture logic (Power Comparison)
				if neighbor.Owner != currentCard.Owner && currentCard.Power[cn.placedPowerIdx] > neighbor.Power[cn.neighborPowerIdx] {
					if flipCard(nbIdx, currentCard.Owner, "COMBO") {
						totalFlips++
						comboQueue = append(comboQueue, nbIdx)
					}
				}
			}
		}
	}
	return totalFlips
}

// flipCard is a helper to change ownership and log/sound the event
func flipCard(index int, newOwner int, reason string) bool {
	card := Game.Board[index]
	if card == nil || card.Owner == newOwner {
		return false
	}

	oldOwner := card.Owner
	card.Owner = newOwner
	fmt.Printf("[BATTLE] %s: %s flipped from P%d to P%d at Grid %d\n",
		reason, card.Name, oldOwner+1, newOwner+1, index)

	// Set combo flag for visual feedback in frontend if part of a chain
	if reason == "SAME" || reason == "PLUS" || reason == "COMBO" {
		card.IsCombo = true
	}

	// Update visual tier to match the new owner's reputation
	tier, color, _ := calculateTier(Game.Players[newOwner].Reputation)
	card.Tier = tier
	card.GlowColor = color

	// Occasionally play vocal reactions for tactical combos (40% chance)
	if reason == "COMBO" && rand.Intn(100) < 40 {
		if newOwner == 0 {
			// Player 1 just flipped an opponent card: Opponent grunts in frustration
			vocalPool := []string{"ugh_3.wav", "ehn.wav", "grah.wav"}
			PlaySound(vocalPool[rand.Intn(len(vocalPool))])
		} else {
			// Opponent (Bot) just flipped a player card: Bot laughs/taunts
			laughPool := []string{
				"cute/cutie-laugh-83616.mp3",
				"cute/girl-laugh-6689.mp3",
				"cute/hehehehe-288404.mp3",
				"cute/small-laugh-86064.mp3",
				"Lady/soft-laughing-6445.mp3",
				"Lady/woman-laugh-6421.mp3",
				"Witch/evil-laugh-7-103773.mp3",
				"Witch/evil-witch-laugh-140135.mp3",
				"Witch/witch-laugh-95203.mp3",
			}
				track := laughPool[rand.Intn(len(laughPool))]
				
				// ARCHETYPE RECOGNITION: Categorize the taunt for the finale
				if track == "cute/small-laugh-86064.mp3" || track == "Lady/soft-laughing-6445.mp3" || track == "Lady/woman-laugh-6421.mp3" {
					Game.SpecialFanfare = "Emotional"
					Game.Players[1].AvatarURL = "Public_assets/4kvisuals/Images/portraits/cute/Easy/Sally.webp"
				} else if track == "Witch/evil-witch-laugh-140135.mp3" || track == "Witch/witch-laugh-95203.mp3" {
					Game.SpecialFanfare = "Witch"
					Game.Players[1].AvatarURL = "Public_assets/4kvisuals/Images/portraits/Witch/Evil_jackpot_Jessica/Evil_jackpot_Jessica.webp"
				}
				
				PlaySound(track)
		}
	} else {
		PlaySound("capture")
	}
	return true
}

// checkWinCondition evaluates if the board is full, calculates scores, and declares the winner
func checkWinCondition() {
	// 1. Check if all 9 slots are filled
	for _, c := range Game.Board {
		if c == nil {
			return // Board not full, keep playing
		}
	}

	// 2. Board is full, transition phase
	Game.Phase = "Finished"
	p1Score := 0
	p2Score := 0

	// 3. Count cards on the board
	for _, c := range Game.Board {
		if c.Owner == 0 {
			p1Score++
		} else {
			p2Score++
		}
	}

	// 4. Count cards remaining in hands (1 card total remains between both players)
	for pIdx := 0; pIdx < 2; pIdx++ {
		p := &Game.Players[pIdx]
		for i := range p.Decks[p.ActiveDeck] {
			onBoard := false
			for _, bc := range Game.Board {
				if bc == &p.Decks[p.ActiveDeck][i] {
					onBoard = true
					break
				}
			}

			if !onBoard {
				if pIdx == 0 {
					p1Score++
				} else {
					p2Score++
				}
			}
		}
	}

	// TESTING OVERRIDE: Force win against bot for reward loop testing
	if Game.TestingMode && !Game.Multiplayer {
		p1Score = 10
		p2Score = 0
		fmt.Println("[TESTING] Testing Mode Active: Forcing Player 1 Victory.")
	}

	Game.Scores = [2]int{p1Score, p2Score}

	// 5. Declare Results
	if p1Score > p2Score {
		Game.Winner = 0
		fmt.Printf("[GAME OVER] Player 1 Wins %d-%d!\n", p1Score, p2Score)
		
		if Game.SpecialFanfare == "Witch" {
			PlaySound("opponet_losing_witch.wav")
			Game.Players[1].AvatarURL = "Public_assets/4kvisuals/Images/portraits/Witch/Evil_jackpot_Jessica/Evil_jackpot_Jessica.webp"
		} else if Game.SpecialFanfare == "Emotional" {
			PlaySound("opponent_losing_falter.wav")
			Game.Players[1].AvatarURL = "Public_assets/4kvisuals/Images/portraits/Lady/Hard/Beaten_Angelina/Beaten Angelina.webp"
		} else {
			// Standard Victory Fanfare Pool (.wav)
			vicPool := []string{
				"opponent_lose_2.wav", "opponent_lose_3.wav", "opponent_lose_4.wav", 
				"opponent_lose_5.wav", "opponent_lose.wav", "opponent_lost_1.wav", 
				"opponent_lost_3.wav", "opponent_lost.wav", "opponet_losing_witch.wav",
			}
			PlaySound(vicPool[rand.Intn(len(vicPool))])
		}
		
		SendReward(js.Null(), nil)
	} else if p2Score > p1Score {
		Game.Winner = 1
		fmt.Printf("[GAME OVER] Player 2 Wins %d-%d!\n", p2Score, p1Score)

		if Game.SpecialFanfare == "Witch" {
			PlaySound("opponent_winning_witch.wav")
		} else if Game.SpecialFanfare == "Emotional" {
			PlaySound("opponent_win_life_over.wav")
		} else {
			// Standard Defeat Fanfare Pool (.wav)
			lossPool := []string{
				"opponent_win_1.wav", "opponent_win_2.wav", "opponent_win_3.wav", 
				"opponent_win_4.wav", "opponent_win_5.wav", "opponent_win_6.wav", 
				"opponent_win.wav", "opponent_winning_witch.wav",
			}
			PlaySound(lossPool[rand.Intn(len(lossPool))])
		}
	} else {
		Game.Winner = 2
		fmt.Printf("[GAME OVER] DRAW! %d-%d\n", p1Score, p2Score)
	}
}

// PerformAIMove scans every possible slot and card to find the move with the most captures
func PerformAIMove() {
	// Guard: Only run if it's Player 2's turn and game is active
	if Game.Phase != "Active" || Game.Turn != 1 || Game.Multiplayer {
		return
	}

	// TACTICAL UX: Simulate "Thinking" pause
	// Random wait between 1 and 5 seconds before starting calculations
	delay := time.Duration(rand.Intn(4001)+1000) * time.Millisecond
	time.Sleep(delay)

	// 1. Find empty slots
	var emptySlots []int
	for i, c := range Game.Board {
		if c == nil {
			emptySlots = append(emptySlots, i)
		}
	}

	// 2. Find unplaced cards in AI hand
	var handIndices []int
	p2 := &Game.Players[1]
	for i := range p2.Decks[p2.ActiveDeck] {
		onBoard := false
		for _, bc := range Game.Board {
			if bc == &p2.Decks[p2.ActiveDeck][i] {
				onBoard = true
				break
			}
		}
		if !onBoard {
			handIndices = append(handIndices, i)
		}
	}

	if len(emptySlots) > 0 && len(handIndices) > 0 {
		bestSlot := emptySlots[0]
		bestCardIdx := handIndices[0]
		maxScore := -9999

		// TACTICAL EVALUATION: Scan for best move with real-time UI feedback (Jitter effect)
		for _, slot := range emptySlots {
			for _, handIdx := range handIndices {
				card := &p2.Decks[p2.ActiveDeck][handIdx]

				// 1. Calculate tactical score for this specific combination
				score := simulateCaptures(card, slot)

				// 2. Update engine state with current evaluation score to create the "jitter"
				Game.AIScore = score
				
				// 3. Trigger UI Sync to update the intensity meter in the browser
				js.Global().Call("syncUI")

				// 4. Delay to make the "thinking" process visible
				time.Sleep(75 * time.Millisecond)

				if score > maxScore {
					maxScore = score
					bestSlot = slot
					bestCardIdx = handIdx
				}
			}
		}

		// Final Decision: Hold the best score for a moment
		Game.AIScore = maxScore
		js.Global().Call("syncUI")
		time.Sleep(600 * time.Millisecond)

		// Execute the best move found
		Game.Board[bestSlot] = &p2.Decks[p2.ActiveDeck][bestCardIdx]
		actualFlips := checkCaptures(&p2.Decks[p2.ActiveDeck][bestCardIdx], bestSlot)
		fmt.Printf("[AI] Bot Move: Placed %s at Grid %d (Score: %d, Flips: %d)\n", p2.Decks[p2.ActiveDeck][bestCardIdx].Name, bestSlot, maxScore, actualFlips)
		PlaySound("Select-place-card")

		Game.Turn = 0
		checkWinCondition()
		js.Global().Call("syncUI")
	}
}

// simulateCaptures calculates how many cards would be flipped without modifying the board
func simulateCaptures(placedCard *Card, gridIndex int) int {
	totalScore := 0
	flipped := make(map[int]bool)

	// Helper for checking neighbors (identical to checkCaptures logic)
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

	// 1. Initial Scan
	for _, n := range neighbors {
		neighborIndex := gridIndex + n.offset
		if n.boundaryCheck(gridIndex) && Game.Board[neighborIndex] != nil {
			neighborCard := Game.Board[neighborIndex]
			pPower := placedCard.Power[n.placedPowerIdx]
			nPower := neighborCard.Power[n.neighborPowerIdx]

			if Game.Rules["Same"] && pPower == nPower {
				sameGroups[pPower] = append(sameGroups[pPower], neighborIndex)
			}
			if Game.Rules["Plus"] {
				sum := pPower + nPower
				plusGroups[sum] = append(plusGroups[sum], neighborIndex)
			}
			// Basic Capture (10 points per flip)
			if neighborCard.Owner != placedCard.Owner && pPower > nPower {
				flipped[neighborIndex] = true
				totalScore += 10
			}
		}
	}

	// 2. Rules check
	for _, indices := range sameGroups {
		if len(indices) >= 2 {
			totalScore += 50 // Rule trigger bonus (Tactical Priority)
			for _, idx := range indices {
				if Game.Board[idx].Owner != placedCard.Owner {
					flipped[idx] = true
					totalScore += 20 // Rule flip weight
					comboQueue = append(comboQueue, idx)
				}
			}
		}
	}
	for _, indices := range plusGroups {
		if len(indices) >= 2 {
			totalScore += 50 // Rule trigger bonus
			for _, idx := range indices {
				if Game.Board[idx].Owner != placedCard.Owner {
					flipped[idx] = true
					totalScore += 20 // Rule flip weight
					comboQueue = append(comboQueue, idx)
				}
			}
		}
	}

	// 3. Combo Chain Simulation
	for len(comboQueue) > 0 {
		currIdx := comboQueue[0]
		comboQueue = comboQueue[1:]
		currCard := Game.Board[currIdx]

		for _, n := range neighbors {
			nbIdx := currIdx + n.offset
			if n.boundaryCheck(currIdx) && Game.Board[nbIdx] != nil {
				neighbor := Game.Board[nbIdx]
				if neighbor.Owner != placedCard.Owner && !flipped[nbIdx] {
					if currCard.Power[n.placedPowerIdx] > neighbor.Power[n.neighborPowerIdx] {
						flipped[nbIdx] = true
						totalScore += 15 // Combo flip weight
						comboQueue = append(comboQueue, nbIdx)
					}
				}
			}
		}
	}

	if Game.HardMode {
		// Defensive penalty: avoid placing weak sides (power < 5) against open board slots
		for _, n := range neighbors {
			if n.boundaryCheck(gridIndex) && Game.Board[gridIndex+n.offset] == nil {
				power := placedCard.Power[n.placedPowerIdx]
				if power < 5 {
					totalScore -= (5 - power) * 5
				}
			}
		}
		return totalScore
	}

	return len(flipped)
}

// -----------------------------------------------------------------------------
// 6. THE STATE EXPORTER (The Camera)
// -----------------------------------------------------------------------------

// GetGameState sends a secure snapshot of the vault to the JavaScript UI
func GetGameState(this js.Value, args []js.Value) interface{} {
	// Define a snapshot structure for efficient serialization
	snapshot := struct {
		Phase       string             `json:"phase"`
		Turn        int                `json:"turn"`
		Rewards     map[uint64]float64 `json:"rewards"`
		Rules       map[string]bool    `json:"rules"`
		P1Avatar    string             `json:"p1_avatar"`
		P2Avatar    string             `json:"p2_avatar"`
		Board       [9]*Card           `json:"board"`
		Deck        []Card             `json:"deck"`
		Inventory   []Card             `json:"inventory"`
		Reputation  int                `json:"reputation"`
		ActiveSlot  int                `json:"active_deck"`
		Multiplayer bool               `json:"multiplayer"`
		Scores      [2]int             `json:"scores"`
		Winner      int                `json:"winner"`
		Faucet      float64            `json:"faucet"`
		Maintenance bool               `json:"maintenance"`
		TestingMode bool               `json:"testing_mode"`
		IsAdmin     bool               `json:"is_admin"`
		AIScore     int                `json:"ai_score"`
		ServerLoad  int                `json:"server_load"`
		SpecialFanfare string          `json:"special_fanfare"`
		DeckRating  string             `json:"deck_rating"`
		VaultLow    bool               `json:"vault_low"`
		Latency     int                `json:"latency"`
		NetworkHealth string           `json:"network_health"`
		ServerLoadColor string         `json:"server_load_color"`
		SiteVerified bool              `json:"site_verified"`
		Network     string             `json:"network"`
	}{
		Phase:       Game.Phase,
		Turn:        Game.Turn,
		Rewards:     Game.Rewards,
		Rules:       Game.Rules,
		P1Avatar:    Game.Players[0].AvatarURL,
		P2Avatar:    Game.Players[1].AvatarURL,
		Board:       Game.Board,
		Deck:        Game.Players[Game.Turn].Decks[Game.Players[Game.Turn].ActiveDeck],
		Inventory:   Game.Inventory,
		Reputation:  Game.Players[0].Reputation,
		ActiveSlot:  Game.Players[0].ActiveDeck,
		Multiplayer: Game.Multiplayer,
		Scores:      Game.Scores,
		Winner:      Game.Winner,
		Faucet:      Game.Faucet,
		Maintenance: Game.Maintenance,
		TestingMode: Game.TestingMode,
		IsAdmin:     Game.IsAdmin,
		AIScore:     Game.AIScore,
		ServerLoad:  Game.ServerLoad,
		SpecialFanfare: Game.SpecialFanfare,
		DeckRating:  calculateDeckRating(Game.Players[0].Decks[Game.Players[0].ActiveDeck]),
		VaultLow:    Game.VaultLow,
		Latency:     Game.Latency,
		NetworkHealth: Game.NetworkHealth,
		ServerLoadColor: calculateLoadColor(Game.ServerLoad),
		SiteVerified: Game.SiteVerified,
		Network:     Game.Network,
	}

	stateJSON, err := json.Marshal(snapshot)
	if err != nil {
		fmt.Printf("[ENGINE ERROR] State serialization failed: %v\n", err)
		return nil
	}

	// Use browser's native JSON.parse to efficiently materialize the JS object
	return js.Global().Get("JSON").Call("parse", string(stateJSON))
}

// -----------------------------------------------------------------------------
// 7. BROWSER BRIDGES & AUDIO
// -----------------------------------------------------------------------------

// calculateTier is an internal helper to determine ranking metadata
func calculateTier(rep int) (string, string, bool) {
	tier := "Iron"
	color := "#a19d94" // Iron Grey

	if rep >= 500 {
		tier = "Diamond"
		color = "#b9f2ff" // Diamond Blue
	} else if rep >= 300 {
		tier = "Gold"
		color = "#ffd700" // Classic Gold
	} else if rep >= 150 {
		tier = "Bronze"
		color = "#cd7f32" // Bronze Orange
	}

	return tier, color, rep >= 500
}

// calculateLoadColor determines the hex color based on the match count
func calculateLoadColor(load int) string {
	if load >= 25 {
		return "#ff0000" // Red (Heavy)
	} else if load >= 10 {
		return "#ffff00" // Yellow (Moderate)
	}
	return "#00ff00" // Green (Optimal)
}

// GetServerLoadColor returns the current load color to the UI
func GetServerLoadColor(this js.Value, args []js.Value) interface{} {
	color := calculateLoadColor(Game.ServerLoad)
	status := "Optimal"
	if Game.ServerLoad >= 25 {
		status = "Heavy"
	} else if Game.ServerLoad >= 10 {
		status = "Moderate"
	}
	return map[string]interface{}{"color": color, "status": status}
}

// GetTierInfo returns the tier name and thematic color for a given reputation score.
func GetTierInfo(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return nil
	}
	rep := args[0].Int()
	tier, color, bonus := calculateTier(rep)

	return map[string]interface{}{
		"tier":  tier,
		"color": color,
		"bonus": bonus,
	}
}

func ToggleLeaderboard(this js.Value, args []js.Value) interface{} {
	Game.ShowLeaderboard = !Game.ShowLeaderboard
	fmt.Printf("[ENGINE] Leaderboard Visible: %v\n", Game.ShowLeaderboard)
	PlaySound("click")
	return Game.ShowLeaderboard
}

func UpdateAmbientMusic() {
	var track string
	var category string

	// 1. Determine Category based on Game State
	if Game.Players[0].Wallet == "" {
		category = "not_connected"
		track = "Public_assets/Ambient_sfx/Not_connected_ambient"
	} else if len(Game.Players[0].Decks[Game.Players[0].ActiveDeck]) < 5 {
		category = "unbuilt"
		track = "Public_assets/Ambient_sfx/Unbuilt_deck_ambient"
	} else if Game.Phase == "Active" {
		category = "match"
		matchPool := []string{
			"2_player_ambient_1", "2_player_ambient_2", "2_player_ambient_3",
			"quick_play_ambient_1", "quick_play_ambient_2", "quick_play_ambient_3",
			"Tournament_game_ambient", "Tournament_game_ambient_2", "Tournament_game_ambient3", "Tournament_game_ambient4", "Tournament_game_ambient5",
		}
		track = "Public_assets/Ambient_sfx/" + matchPool[rand.Intn(len(matchPool))]
	} else {
		category = "menu"
		menuPool := []string{
			"ambient_menu_music_1", "ambient_menu_music_2", "ambient_menu_music_3", "ambient_menu_music_4",
		}
		track = "Public_assets/Ambient_sfx/" + menuPool[rand.Intn(len(menuPool))]
	}

	// 2. Only switch if the category or track has changed to prevent resetting audio on every UI click
	if Game.CurrentAmbientTrack == category && (category == "not_connected" || category == "unbuilt") {
		return
	}

	StopAmbient()
	Game.CurrentAmbientTrack = category
	PlayAmbient(track)
}

func StopAmbient() {
	if Game.AmbientAudio.Type() == js.TypeObject {
		Game.AmbientAudio.Call("pause")
		Game.AmbientAudio.Set("currentTime", 0)
	}
}

func PlayAmbient(path string) {
	fullPath := Game.AssetBase + path + ".mp3"
	audio := js.Global().Get("Audio").New(fullPath)
	if audio.Type() == js.TypeObject {
		audio.Set("loop", true)
		audio.Set("volume", 0.5) // Lower volume for background music
		Game.AmbientAudio = audio
		
		// Play requires a promise handle in modern browsers
		promise := audio.Call("play")
		if promise.Type() == js.TypeObject {
			promise.Call("catch", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
				fmt.Printf("[AUDIO] Ambient blocked by browser: %v\n", args[0])
				return nil
			}))
		}
		fmt.Printf("[AUDIO] Playing Ambient: %s\n", path)
	}
}

func SetAssetBase(this js.Value, args []js.Value) interface{} {
	if len(args) > 0 {
		Game.AssetBase = args[0].String()
		fmt.Printf("[ENGINE] Asset Base URL set to: %s\n", Game.AssetBase)
	}
	return nil
}

func PlaySound(name string) {
	// If the name doesn't already contain an extension, default to .mp3
	filename := name
	if !strings.Contains(name, ".") {
		filename += ".mp3"
	}

	path := Game.AssetBase + "Assets/" + filename
	audio := js.Global().Get("Audio").New(path)
	if audio.Type() == js.TypeObject {
		audio.Call("play")
	}
}

func registerFunctions() {
	js.Global().Set("connectWallet", js.FuncOf(connectWallet))
	js.Global().Set("disconnectWallet", js.FuncOf(disconnectWallet))
	js.Global().Set("toggleNetwork", js.FuncOf(toggleNetwork))
	js.Global().Set("SetAvatar", js.FuncOf(SetAvatar))
	js.Global().Set("SendReward", js.FuncOf(SendReward))

	js.Global().Set("ToggleRule", js.FuncOf(ToggleRule))
	js.Global().Set("AddToDeck", js.FuncOf(AddToDeck))
	js.Global().Set("AutoBuildDeck", js.FuncOf(AutoBuildDeck))
	js.Global().Set("PlaySelectSound", js.FuncOf(PlaySelectSound))
	js.Global().Set("SelectDeck", js.FuncOf(SelectDeck))
	js.Global().Set("RemoveFromDeck", js.FuncOf(RemoveFromDeck))
	js.Global().Set("SyncOpponentDeck", js.FuncOf(SyncOpponentDeck))
	js.Global().Set("SetPlayerReady", js.FuncOf(SetPlayerReady))

	js.Global().Set("StartMatch", js.FuncOf(StartMatch))
	js.Global().Set("PlaceCard", js.FuncOf(PlaceCard))
	js.Global().Set("GetGameState", js.FuncOf(GetGameState)) // Expose the Camera
	js.Global().Set("SetAdminState", js.FuncOf(SetAdminState))
	js.Global().Set("SyncPlayerStats", js.FuncOf(SyncPlayerStats))
	js.Global().Set("SyncServerLoad", js.FuncOf(SyncServerLoad))
	js.Global().Set("SyncLatency", js.FuncOf(SyncLatency))
	js.Global().Set("GetLevelLabelForDisplay", js.FuncOf(GetLevelLabelForDisplay))
	js.Global().Set("TriggerManualSync", js.FuncOf(TriggerManualSync))
	js.Global().Set("GetServerLoadColor", js.FuncOf(GetServerLoadColor))
	js.Global().Set("SetTestingMode", js.FuncOf(SetTestingMode))
	js.Global().Set("SetHardMode", js.FuncOf(SetHardMode))
	js.Global().Set("GetTierInfo", js.FuncOf(GetTierInfo))
	js.Global().Set("SyncRules", js.FuncOf(SyncRules))
	js.Global().Set("SyncRewards", js.FuncOf(SyncRewards))
	js.Global().Set("SyncVaultBalance", js.FuncOf(SyncVaultBalance))
	js.Global().Set("SetMaintenanceState", js.FuncOf(SetMaintenanceState))
	js.Global().Set("ForceActive", js.FuncOf(ForceActive))
	js.Global().Set("SetBoardState", js.FuncOf(SetBoardState))
	js.Global().Set("ResetGame", js.FuncOf(ResetGame))
	js.Global().Set("SetAssetBase", js.FuncOf(SetAssetBase))
	js.Global().Set("VerifySiteHandshake", js.FuncOf(VerifySiteHandshake))
	js.Global().Set("ImportARC72Card", js.FuncOf(ImportARC72Card))
}

// ImportARC72Card validates raw JSON metadata and converts it into a playable Card
func ImportARC72Card(this js.Value, args []js.Value) interface{} {
	if len(args) < 3 {
		return false
	}

	tokenID := args[0].Int()
	rawJSON := args[1].String()
	mintPrice := args[2].Float()

	// Optional Arguments for Advanced Scaling
	totalSupply := 0
	rarityWeight := 1.0
	timesSold := 0
	mintRound := 0

	if len(args) >= 6 {
		timesSold = args[4].Int()
		mintRound = args[5].Int()
	}
	if len(args) >= 4 {
		totalSupply = args[3].Int()
		rarityWeight = 1.0 + (500.0 / (float64(totalSupply) + 500.0))
	}

	var meta ARC72Metadata
	err := json.Unmarshal([]byte(rawJSON), &meta)

	// Default fallback values
	name := fmt.Sprintf("Babe #%d", tokenID)
	if err == nil && meta.Name != "" {
		name = meta.Name
	}

	// FIXED: Power Normalization (Unified 0-2599 Scale)
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

	// Placeholder for fetching detailed sales data on client-side
	// For now, we'll use mintPrice for all if no specific traits are found.
	// In a real scenario, client would fetch from NFT Navigator API like server.
	lastSale := mintPrice
	initialAsking := mintPrice // Placeholder for initial asking price
	currentAsking := mintPrice // Placeholder for current asking price

	// Prioritize current asking, then initial asking, then mint price for 'Bottom'
	bottomPrice := currentAsking
	if bottomPrice <= 0 {
		bottomPrice = initialAsking
	}
	if bottomPrice <= 0 {
		bottomPrice = mintPrice
	}

	// FIXED: Standardized Left (Age) Tiers
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

	// FIXED: Right (Sales Experience)
	expPower := timesSold * 25
	if expPower > 2599 {
		expPower = 2599
	}

	powers := [4]int{
		int(float64(norm(lastSale)) * rarityWeight),
		int(float64(expPower) * rarityWeight),
		int(float64(norm(bottomPrice)) * rarityWeight),
		int(float64(agePower) * rarityWeight),
	}

	// Clamp to scale
	for i := range powers {
		if powers[i] > 2599 {
			powers[i] = 2599
		}
	}

	// Advanced Attribute Parsing
	if err == nil {
		for _, attr := range meta.Attributes {
			val := 0
			switch v := attr.Value.(type) {
			case float64:
				val = int(v)
			case int:
				val = v
			}

			// Map specific traits to specific directions
			switch attr.TraitType {
			case "Attack", "Top":
				powers[0] = norm(float64(val))
			case "Speed", "Right":
				powers[1] = norm(float64(val))
			case "Defense", "Bottom":
				powers[2] = norm(float64(val))
			case "Charisma", "Left":
				powers[3] = norm(float64(val))
			}
		}
	}

	image := meta.Image
	if image == "" {
		image = fmt.Sprintf("Assets/cards/%d.png", tokenID)
	}

	newCard := Card{
		ID:     tokenID,
		Name:   name,
		Power:  powers,
		Image:  image,
		Rarity: rarityWeight,
	}

	Game.Inventory = append(Game.Inventory, newCard)
	fmt.Printf("[ENGINE] Imported ARC-72 Card: %s (Stats: %v, Rarity: %.2fx)\n", name, powers, rarityWeight)
	return true
}

// Helper to generate test inventory
func GenerateCard(id int, name string, price float64) Card {
	base := int(price / 10)
	if base < 2 {
		base = 2
	}
	if base > 9 {
		base = 9
	}
	return Card{ID: id, Name: name, Power: [4]int{base, base - 1, base + 1, base}, Image: fmt.Sprintf("Assets/cards/%d.png", id), Rarity: 1.0}
}

// getLevelFromValue maps a power value (1-2600) to an A-Z letter grade.
func getLevelFromValue(val int) string {
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	// Corrected Mapping: 1-100 = Z, 101-200 = Y, ..., 2501-2600 = A
	// Bin 0: 1-100 (Z) -> index 25
	// Bin 1: 101-200 (Y) -> index 24
	// Bin 25: 2501-2600 (A) -> index 0
	bin := (val - 1) / 100
	if bin < 0 { bin = 0 } // Handle 0 or negative values as lowest tier
	if bin > 25 { bin = 25 } // Handle values > 2600 as highest tier
	return string(alphabet[25-bin])
}

// GetLevelLabelForDisplay is a bridge function to expose getLevelFromValue to JS.
func GetLevelLabelForDisplay(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return "Z" // Default for invalid input
	}
	val := args[0].Int()
	return getLevelFromValue(val)
}

// calculateDeckRating computes the [Letter++] rating for a given deck.
func calculateDeckRating(deck []Card) string {
	if len(deck) == 0 {
		return "[Z]"
	}

	maxBin := -1
	// 1. Find the highest card tier (bin) in the deck
	for _, card := range deck {
		highestPower := 0
		for _, p := range card.Power {
			if p > highestPower {
				highestPower = p
			}
		}
		bin := (highestPower - 1) / 100
		if bin > maxBin { maxBin = bin }
	}

	if maxBin == -1 { return "[Z]" } // Should not happen with non-empty deck

	// 2. Map maxBin to Letter
	baseLetter := getLevelFromValue((maxBin * 100) + 1) // Get the letter for the start of the bin

	// 3. Count how many cards share this highest tier
	plusCount := 0
	for _, card := range deck {
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
	for i := 0; i < plusCount; i++ { suffix += "+" }
	
	return fmt.Sprintf("[%s%s]", baseLetter, suffix)
}

func main() {
	wait := make(chan struct{}, 0)

	fmt.Println("---------------------------------------")
	fmt.Println(" VirtualbabesTT Vault: SYNC ONLINE     ")
	fmt.Println(" Camera Exporter & Sim Deck Active     ")
	fmt.Println("---------------------------------------")

	// Seed Global Inventory with Demo Assets
	for i, path := range Game.DemoPool {
		if i >= 5 { break } // Seed first 5 as inventory
		c := GenerateCard(100+i, fmt.Sprintf("Babe %d", i+1), 50.0 + float64(i*10))
		c.Image = path
		Game.Inventory = append(Game.Inventory, c)
	}

	registerFunctions()
	<-wait
}
