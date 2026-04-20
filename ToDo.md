# Project Tasks: Virtualbabes Faucet

## Phase 1: Core Architecture & Game Engine
- [x] Initial Repository Investigation
- [x] Project Structure Documentation
- [x] Generate comprehensive `App_Flow.md`
- [x] Move Admin Wallet addresses to `.env` configuration
- [x] Implement Contextual Ambient Music Logic (BGM)
- [x] Implement Mandatory Avatar Selection phase (Setup -> Preview -> Crop -> Confirm) with Collection Filters
- [x] Refactor `server.go` locking strategy to prevent deadlocks in `handleGameProtocol`
- [x] Finalize `main.go` engine logic and exported JS bridges
- [x] Update and harden `wasm_exec.js` bridge for callback and async support

## Phase 2: Gameplay Mechanics & AI
- [x] Implement Triple Triad Core Rules (Same, Plus, Combo)
- [x] Implement "Hard Mode" AI logic with weighted tactical scoring
- [x] Add random wait time (1-5s) to AI turns before evaluation
- [x] Refactor `PerformAIMove` for real-time jitter/climb effects
- [x] Export AI Predicted Score in `GetGameState` for Intensity Meter
- [x] Implement Tier-based visual effects (Card Glows) in Engine
- [x] Refactor `PlaceCard` to support `is_combo` visual flags
- [x] Implement dynamic ServerLoad color logic (Green -> Yellow -> Red)

## Phase 3: Blockchain Integration & Persistence
- [x] Migrate Persistence Layer to Blockchain Transaction History (Indexer-based)
- [x] Clean up legacy file-persistence methods (`saveLeaderboard`)
- [x] Implement On-Chain DNF tracking (`VBT_DNF:`)
- [x] Support Fallback Asset ID for $VBV history aggregation
- [x] Implement Transaction Note Tagging (`VBT_WIN:`) to prevent double-counting
- [x] Implement 'Mardon Badge' logic for elite players
- [x] Export `ActiveMatchCount` in `lobby_update` for server load tracking
- [x] Implement Indexer-based global leaderboard query (Global $VBV History scan)
- [x] Ensure `matchHistory` is only cleared upon *confirmed* blockchain payout (Tx Confirmation Polling)
- [x] Add "Re-sync Stats" button logic to force Indexer refresh for specific wallets

## Phase 4: Security & Anti-Phishing
- [x] Implement 'Flys on the Wall' Site Verification (Using analytics.txt and BUZZ_BUZZ:[BUZZed])
- [x] Refactor `handleReward` to verify `SiteVerified` status from client
- [x] Harden CORS and Admin Auth against spoofing and timing attacks (Constant-time checks)
- [x] Harden `getVerifiedCard` against API spoofing/failures
- [x] Add sliding-window rate limiting for WebSocket messages
- [x] Transition from `X-Admin-Key` to Signature-based Admin Auth (Wallet-signed Admin Session)
- [x] Implement middleware for standard HTTP API rate-limiting (Leaky Bucket)

## Phase 5: Infrastructure & Observability
- [x] Configure default application port to 8082
- [x] Hardened Admin Auth check and environment handling
- [x] Configure CORS for `https://vbvfaucet.carrd.co/` production domain
- [x] Refactor `handleGetAdminLogs` with security event filtering
- [x] Refactor `handleGetAdminLogs` to include `FaucetBalance` for system overview
- [x] Refactor `logAdminAudit` to include `server_load` in every entry
- [x] Update the `handleSystemMessage` to automatically broadcast server load every 10 minutes
- [x] Refactor admin dashboard UI for 'Low Balance' warning (Threshold: 1000 $VBV)

## Phase 6: Final Polish & Optimization
- [ ] Optimize card metadata fetching (Batch API requests/Long-term caching)
- [ ] Implement `AssetBase` logic for dynamic GitHub CDN asset loading in production
- [x] Add 'Network Status' indicator for WebSocket latency tracking (LatencyMonitor)
- [x] Implement dynamic ARC-72 inventory caching in `server.go` to reduce Indexer calls
- [ ] Create production `systemd` or `Docker` deployment configurations
- [x] Finalize UI for Cinematic Leaderboard (Top 10 Still / 11+ Scrolling logic)
- [x] Map Carrd UI Buttons to WASM functions (Connect, Sync, Reward)
- [x] Implement Multi-Deck Manager with Reputation Unlocks (Drag & Drop)
- [x] Refactor Hall of Fame UI to display 'Best Deck Rating' next to Win Count
- [x] Add Refresh and Return buttons to Hall of Fame UI
- [x] Implement 'Gear Up' sound effect for deck selection
- [x] Implement 'Select-place-card' sound effect for selection and placement
- [x] Refactor `checkWinCondition` to trigger random victory/loss fanfares (.wav)
- [x] Implement occasional vocal reactions/taunts for COMBO captures (Grunts & Laughs)
- [x] Implement multi-archetype 'Special Fanfare' triggers (Emotional & Witch)
- [x] Export `SpecialFanfare` archetype in `GetGameState` for dynamic UI avatars
- [x] Move Avatar URL logic into WASM Engine (GitHub-First)
- [x] Implement 'Rarity' power weighting in `ImportARC72Card` based on supply
- [x] Overhaul Power Scale to 0-2599 with Z-A Level Caps
- [x] Refine Level Mapping (1-100=Z, 2501-2600=A)
- [x] Move `getLevelFromValue` to WASM as `GetLevelLabelForDisplay` bridge
- [x] Move `calculateDeckRating` to WASM and export in `GetGameState`
- [x] Refactor Power Ratings: Left (Legacy Tiers), Right (Sales Experience), Top/Bottom (VOI Value)
- [x] Implement Voi Network Block-Round Legacy Bonuses (0-1M: +3, 1M-3M: +2, 3M-6M: +1)
- [x] Unify math scales between main.go and server.go (0-2599)
- [x] Fix Base32 Signature comparison bug in handleReward
- [x] Enable P2 reward payouts in server logic
- [x] Patch Rate-Limiter for X-Forwarded-For support
- [x] Architect Server-Side Shadow Match for PvE rewards
- [x] Implement 'Auto-Build' deck functionality in Engine
- [x] Final Sanity Check: Fixed `main.go` type errors for successful build
- [x] Refactor `.gitignore` to allow `server.go` but block `.env`
- [x] Security Audit: Refactor `.gitignore` for production safety
- [x] Create `.env.example` template for public repository
- [x] Implement '+' suffix Deck Level Rating system
- [x] Harden asset pathing case-sensitivity (assets/ -> Assets/)
- [x] Refactor Deck Manager UI to display Rarity Multiplier badges

## Future Considerations
- [ ] Implement ARC-200 Token Staking for enhanced Reputation multipliers
- [ ] Add "Tournament Mode" with automated bracket generation
- [ ] Multi-language support for international players