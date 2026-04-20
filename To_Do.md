# VirtualbabesTT: Launch Roadmap

## Phase 1: Local Preparation 🛠️
- [ ] **Sync Dependencies**: Run `go mod tidy` to ensure all Algorand SDK and WebSocket modules are cached.
- [ ] **Configure .env**: Create a production `.env` file with the following:
    - `FAUCET_MNEMONIC`: (Your 25-word secret)
    - `VAULT_ADDRESS`: `2A3NWJMYQ7AWJ5KIMYJKWEZS37FWND3PEXT3XPS6QONL6MDRJO257C7WEI`
    - `ADMIN_KEY`: (Secure password for the Admin Panel)
    - `ALGOD_URL_VOI`: `http://127.0.0.1:3536` (For local `func` node)
    - `ALGOD_TOKEN_VOI`: (Your local node token)
    - `SERVER_PORT`: `8080`
    - `ALLOWED_ORIGINS`: `https://<your-github-io-url>,https://<your-carrd-url>`

## Phase 2: Asset Management 🎨
- [ ] **Verify Audio**: Ensure `assets/*.mp3` (click, flip, capture) are in the root directory.
- [ ] **Verify Cards**: Ensure `assets/cards/` contains placeholder images for the inventory.
- [ ] **WalletConnect**: Replace the placeholder `PROJECT_ID` in `index.html` (line 805) with your actual ID from cloud.walletconnect.com.

## Phase 3: Compilation 🏗️
- [ ] **Build WASM**: Run the WASM build command to generate a fresh `main.wasm`.
- [ ] **Build Server**: Compile `server.go` for your target server OS.

## Phase 4: Backend Deployment 🚀
- [ ] **Host Backend**: Deploy the compiled server to a VPS or hosting service (Render, Railway, etc.).
- [ ] **SSL Setup**: Ensure your backend has an SSL certificate (WSS required for HTTPS frontends).
- [ ] **Node Connectivity**: Test that the backend can reach your local `func` node or fallback to public endpoints.

## Phase 5: Frontend Deployment 🌐
- [ ] **GitHub Pages**: Upload `index.html`, `main.wasm`, `wasm_exec.js`, and the `assets/` folder to GitHub.
- [ ] **WASM MIME Type**: Verify that your host serves `.wasm` files as `application/wasm`.
- [ ] **Carrd Embed**: 
    - Update `BACKEND_URL` in `index.html` to point to your live server.
    - Embed the GitHub Pages URL into your Carrd site via an IFrame.

## Phase 6: Final Verification ✅
- [ ] **Vault Refill**: Use the Admin Panel to set the initial `faucet_balance`.
- [ ] **Admin Sync**: Use "Sync Global Rules" to ensure the Lobby matches your desired rules.
- [ ] **Test Loop**: Play a match against the AI, win, and verify the multi-reward atomic payout on the block explorer.
- [ ] **Audit Logs**: Check `win_audit.log` and `admin_audit.log` for correct entry creation.

## Phase 7: Post-Launch 📈
- [ ] **Leaderboard Maintenance**: Periodically monitor the `leaderboard.json` for integrity.
- [ ] **Vault Monitoring**: Watch the UI warning indicator for the "Vault Depleted" status.