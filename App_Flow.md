# Virtualbabes Arena: Application Flow & Architecture

## 1. Initialization & Security Handshake
*   **Hosting Context:** The frontend is a Carrd site (`vbvfaucet.carrd.co`) which embeds an iframe pointing to the game engine hosted on GitHub.
*   **Site Integrity:** The Go WASM engine (`main.go`) performs an internal `fetch` to `https://vbvfaucet.carrd.co/analytics.txt`.
*   **The "Buzz" Handshake:** The engine looks for the obscure token `BUZZ_BUZZ:[BUZZed]`. If found, `Game.SiteVerified` is set to `true`.
*   **Bridge Setup:** `wasm_exec.js` initializes the syscall/js bridge, enabling real-time communication between the browser UI and the Go state machine.

## 2. Connection & Identity
*   **Wallet Link:** User connects via WalletConnect. The wallet address is sent to the Go WASM engine via `connectWallet`.
*   **WebSocket Upgrader:** The engine initiates a connection to the backend (`server.go`) via `ws://[BACKEND_URL]:8082/ws`.
*   **Registry:** The server registers the client and checks the `ADMIN_WALLETS` environment variable. If matched, the client is granted `isAdmin` privileges.
*   **On-Chain Sync:** The server queries the Voi Indexer for the player's transaction history ($VBV transfers) to rebuild their **Wins**, **DNFs**, and **Reputation** without needing a local database.

## 3. The Lobby & Matchmaking
*   **State Sync:** The server broadcasts a `lobby_update` containing player lists, vault balances, and current arena rules.
*   **Deck Building:** Players import ARC-72 NFTs. `main.go` normalizes NFT attributes (Price/Scarcity) into Triple Triad Power stats (1-10).
*   **Challenge Handshake:** 
    *   P1 sends a challenge with their deck.
    *   P2 accepts and sends their deck.
    *   The server creates a **Shadow Board** and broadcasts `match_start` to both players.

## 4. Gameplay Logic (Triple Triad)
*   **Dual-Logic Validation:** 
    *   **Client (WASM):** Handles the UI, animations (Flips, Combos, Tiers), and local capture logic for smooth UX.
    *   **Server (Authority):** Every move is sent to the server. The server verifies card stats via `getVerifiedCard` (fetching from API) and updates its own board state to prevent cheating.
*   **AI (Hard Mode):** In single-player, the bot uses weighted scoring to prioritize **Same** and **Plus** rule triggers, providing a tactical challenge.
*   **UI Intensity:** During AI moves, the engine "jitters" the `AIScore` in real-time, allowing the UI to render a "Thinking..." meter.

## 5. Winning & Reward Oracle
*   **Victory Verification:** When the board is full, the server's `verifyWinner` calculates the final score. If P1 wins, a `MatchHistory` entry is cached.
*   **Reverse Sign Proof:** 
    *   The client requests a unique **Nonce** from the server.
    *   The user signs a 0-amount transaction with the nonce in the note field.
*   **The Oracle Check:** `handleReward` in `server.go` validates:
    1.  The match history exists (Server-verified win).
    2.  `SiteVerified` is true (Handshake passed).
    3.  The signature is valid and matches the winner's wallet.
    4.  The nonce in the signature matches the server's issued nonce.

## 6. Payout & Persistence
*   **Atomic Payout:** The server signs and broadcasts an ARC-200 `transfer` transaction from the vault to the player.
*   **Note Tagging:** The transaction is tagged with `VBT_WIN:[NONCE]` for permanent on-chain auditing.
*   **Reputation Update:** Wins increase reputation; DNFs (recorded via 0-amount `VBT_DNF:` transactions) decrease it.
*   **Vault Protection:** If the balance drops below **1000 $VBV**, the `VaultLow` warning is triggered, and admins are notified via logs.

## 7. Administrative Oversight
*   **Audit Logs:** Every admin action and security denial is recorded in `admin_audit.log` with a timestamp and current `ServerLoad`.
*   **Control Panel:** Admins can refill the vault, toggle maintenance mode, ban wallets, and broadcast system-wide messages.
*   **Health Reports:** Every 10 minutes, the server broadcasts an automated health report (Active Matches + Vault Balance).

---
*Document Version: 1.0.0 (Production Ready)*